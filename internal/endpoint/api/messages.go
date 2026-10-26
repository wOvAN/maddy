/*
Maddy Mail Server - Composable all-in-one email server.
Copyright © 2019-2020 Max Mazurov <fox.cpp@disroot.org>, Maddy Mail Server contributors

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
)

// parseUIDParam reads the {uid} path value. uid 0 is refused, not forwarded:
// go-imap-mess's ResolveSeq rewrites a zero bound of a UID sequence set to the
// mailbox's highest UID (handle.uidMap[len-1], mailbox.go:60-65), so a
// pass-through 0 makes GET .../messages/0 read the newest message and
// DELETE .../messages/0 hard-delete it — a destructive wildcard. 0 is a
// reserved sentinel in responses ("the store would not say", see the append
// uid); input 0 is refused. Keep them coherent.
func parseUIDParam(w http.ResponseWriter, r *http.Request) (uint32, bool) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "uid must be a number", "", nil)
		return 0, false
	}
	if uid == 0 {
		writeErr(w, http.StatusBadRequest, "uid must be greater than zero",
			"uid 0 resolves to the newest message", nil)
		return 0, false
	}
	return uint32(uid), true
}

func (e *Endpoint) handleMsgsList(w http.ResponseWriter, r *http.Request) {
	username, mbox := r.PathValue("user"), r.PathValue("mbox")
	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	_, mb, err := u.GetMailbox(mbox, true, nil)
	if err != nil {
		writeErr(w, statusForStoreError(err), "failed to open mailbox", err.Error(), nil)
		return
	}
	defer func() { _ = mb.Close() }()

	set := &imap.SeqSet{}
	set.AddRange(1, 0) // 1:* — all messages, as "maddy imap-msgs list" defaults to
	fetched, err := fetchMessages(mb, true, set, []imap.FetchItem{
		imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate,
		imap.FetchRFC822Size, imap.FetchEnvelope,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list messages", err.Error(), nil)
		return
	}

	msgs := make([]map[string]interface{}, 0, len(fetched))
	for _, msg := range fetched {
		flags := msg.Flags
		if flags == nil {
			flags = []string{}
		}
		// "date" is the envelope date (when the message was sent) and
		// "internal_date" the arrival date, the pair "maddy imap-msgs list"
		// prints (internal/cli/ctl/imap.go:770-795); a list fetch always
		// carries the envelope, so one shared "date" key was the arrival
		// date only in name.
		entry := map[string]interface{}{
			"uid":           msg.Uid,
			"seq":           msg.SeqNum,
			"flags":         flags,
			"size":          msg.Size,
			"internal_date": msg.InternalDate,
		}
		if msg.Envelope != nil {
			entry["date"] = msg.Envelope.Date
			entry["subject"] = msg.Envelope.Subject
			entry["from"] = addresses(msg.Envelope.From)
			entry["to"] = addresses(msg.Envelope.To)
			entry["cc"] = addresses(msg.Envelope.Cc)
			entry["bcc"] = addresses(msg.Envelope.Bcc)
			entry["message_id"] = msg.Envelope.MessageId
			entry["in_reply_to"] = msg.Envelope.InReplyTo
		}
		msgs = append(msgs, entry)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": msgs})
}

func (e *Endpoint) handleMsgsGet(w http.ResponseWriter, r *http.Request) {
	username, mbox := r.PathValue("user"), r.PathValue("mbox")
	uid, ok := parseUIDParam(w, r)
	if !ok {
		return
	}

	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	_, mb, err := u.GetMailbox(mbox, true, nil)
	if err != nil {
		writeErr(w, statusForStoreError(err), "failed to open mailbox", err.Error(), nil)
		return
	}
	defer func() { _ = mb.Close() }()

	fetched, err := fetchMessages(mb, true, seqset(uid), []imap.FetchItem{imap.FetchRFC822})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to read message", err.Error(), nil)
		return
	}
	var body []byte
	for _, msg := range fetched {
		body = drainBody(msg, e.maxMsgSize)
	}
	if int64(len(body)) > e.maxMsgSize {
		// The literal did not fit the cap, so body is a truncation of it;
		// never report a truncated body as the complete message.
		writeErr(w, http.StatusRequestEntityTooLarge, "message exceeds max_msg_size", "", nil)
		return
	}
	if body == nil {
		writeErr(w, http.StatusNotFound, "no such message", "", nil)
		return
	}
	w.Header().Set("Content-Type", "application/rfc822+text")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (e *Endpoint) handleMsgsAppend(w http.ResponseWriter, r *http.Request) {
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	mbox := r.PathValue("mbox")
	// A max-size raw string full of quotes inflates to ~2x of JSON and blows
	// the decoder's +64 KiB headroom, so the post-decode length check is
	// unreachable for it — the decode fails on syntax first and the client
	// gets a 400 for an oversized body. Refuse on the transport length
	// (which httptest/net/http populate, -1 for chunked) before decoding;
	// this is also the cap on the per-request decode allocation.
	if r.ContentLength > e.maxMsgSize+1<<16 {
		writeErr(w, http.StatusRequestEntityTooLarge, "message exceeds max_msg_size", "", nil)
		return
	}
	var req struct {
		Flags []string `json:"flags"`
		Date  string   `json:"date"`
		Raw   string   `json:"raw"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, e.maxMsgSize+1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body", err.Error(), nil)
		return
	}
	if req.Raw == "" {
		// "maddy imap-msgs append" refuses an empty body ("Empty message,
		// refusing to continue", internal/cli/ctl/imap.go:571).
		writeErr(w, http.StatusBadRequest, "raw is required", "", nil)
		return
	}
	if int64(len(req.Raw)) > e.maxMsgSize {
		writeErr(w, http.StatusRequestEntityTooLarge, "message exceeds max_msg_size", "", nil)
		return
	}
	var date time.Time
	if req.Date != "" {
		var err error
		date, err = time.Parse(time.RFC3339, req.Date)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "date must be RFC 3339", err.Error(), nil)
			return
		}
	}

	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	if err := u.CreateMessage(mbox, req.Flags, date, literal{bytes.NewReader([]byte(req.Raw))}, nil); err != nil {
		e.log.Msg("message append failed", "user", username, "mailbox", mbox,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to append message", err.Error(), nil)
		return
	}
	// Report the uid the message got, the CLI's instrument: it prints
	// Status(mbox).UidNext after the append (internal/cli/ctl/imap.go:574,584),
	// and UidNext-1 is the uid just assigned. 0 means "appended, the
	// store would not say" — the reserved sentinel parseUIDParam refuses as
	// input.
	var created uint32
	if st, err := u.Status(mbox, []imap.StatusItem{imap.StatusUidNext}); err == nil && st.UidNext > 0 {
		created = st.UidNext - 1
	}
	e.log.Msg("message appended", "user", username, "mailbox", mbox, "uid", created,
		"admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusCreated, map[string]interface{}{"mailbox": mbox, "uid": created})
}

// msgCount reads the mailbox's message count; ok=false means the store would
// not say.
func msgCount(u imapbackend.User, mbox string) (uint32, bool) {
	st, err := u.Status(mbox, []imap.StatusItem{imap.StatusMessages})
	if err != nil {
		return 0, false
	}
	return st.Messages, true
}

func (e *Endpoint) handleMsgsDelete(w http.ResponseWriter, r *http.Request) {
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	mbox := r.PathValue("mbox")
	uid, ok := parseUIDParam(w, r)
	if !ok {
		return
	}

	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	// DelMessages reports no count and, on go-imap-sql, a uid that is not
	// present is a silent success (ResolveSeq's uid branch does no membership
	// check, markUid marks zero rows, delMarked deletes them,
	// go-imap-sql sql.go:504-526), so the response must report the
	// destruction, not the request: count the mailbox before and after, the
	// Status pair the CLI reads after a mutation (imap.go:574-584). deleted=0
	// with a 200 means "the store would not say, or nothing matched". The
	// delete is HARD: rows are marked, their ids read and DELETE FROM msgs
	// runs, plus the extStore blob — EXPUNGE, irrecoverable.
	before, known := msgCount(u, mbox)

	// Deleting messages interrupts active IMAP sessions, exactly as the CLI
	// subcommand warns.
	if err := e.deleteMessages(u, mbox, uid); err != nil {
		e.log.Msg("message delete failed", "user", username, "mailbox", mbox, "uid", uid,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to delete message", err.Error(), nil)
		return
	}
	var deleted uint32
	if known {
		if after, ok := msgCount(u, mbox); ok && before > after {
			deleted = before - after
		}
	}
	e.log.Msg("message deleted", "user", username, "mailbox", mbox, "uid", uid,
		"deleted", deleted, "admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusOK, map[string]interface{}{"uid": uid, "deleted": deleted})
}
