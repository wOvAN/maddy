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

func (e *Endpoint) mailboxFor(u imapbackend.User, name string) (*imap.MailboxStatus, imapbackend.Mailbox, error) {
	status, mb, err := u.GetMailbox(name, true, nil)
	if err != nil {
		return nil, nil, err
	}
	return status, mb, nil
}

func (e *Endpoint) handleMsgsList(w http.ResponseWriter, r *http.Request) {
	username, mbox := r.PathValue("user"), r.PathValue("mbox")
	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	_, mb, err := e.mailboxFor(u, mbox)
	if err != nil {
		writeErr(w, statusForStoreError(err), "failed to open mailbox", err.Error(), nil)
		return
	}
	defer func() { _ = mb.Close() }()

	set := &imap.SeqSet{}
	set.AddRange(1, 0) // 1:* — all messages, as "maddy imap-msgs list" defaults to
	ch := make(chan *imap.Message, 32)
	errCh := make(chan error, 1)
	go func() {
		// The backend owns the close ("when the function returns, ch must be
		// closed", backend/mailbox.go) — go-imap-sql and the test double both
		// close it; a close here panics. Same shape as internal/cli/ctl/imap.go:747.
		errCh <- mb.ListMessages(true, set, []imap.FetchItem{
			imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate,
			imap.FetchRFC822Size, imap.FetchEnvelope,
		}, ch)
	}()

	var msgs []map[string]interface{}
	for msg := range ch {
		entry := map[string]interface{}{
			"uid":   msg.Uid,
			"seq":   msg.SeqNum,
			"flags": msg.Flags,
			"size":  msg.Size,
			"date":  msg.InternalDate,
		}
		if msg.Envelope != nil {
			entry["subject"] = msg.Envelope.Subject
			entry["date"] = msg.Envelope.Date
			entry["from"] = addresses(msg.Envelope.From)
			entry["to"] = addresses(msg.Envelope.To)
		}
		msgs = append(msgs, entry)
	}
	if err := <-errCh; err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list messages", err.Error(), nil)
		return
	}
	if msgs == nil {
		msgs = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": msgs})
}

func (e *Endpoint) handleMsgsGet(w http.ResponseWriter, r *http.Request) {
	username, mbox := r.PathValue("user"), r.PathValue("mbox")
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "uid must be a number", "", nil)
		return
	}

	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	_, mb, err := e.mailboxFor(u, mbox)
	if err != nil {
		writeErr(w, statusForStoreError(err), "failed to open mailbox", err.Error(), nil)
		return
	}
	defer func() { _ = mb.Close() }()

	ch := make(chan *imap.Message, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- mb.ListMessages(true, seqset(uint32(uid)), []imap.FetchItem{imap.FetchRFC822}, ch)
	}()

	var body []byte
	for msg := range ch {
		for _, lit := range msg.Body {
			b, err := io.ReadAll(io.LimitReader(lit, e.maxMsgSize))
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "failed to read message body", err.Error(), nil)
				return
			}
			body = b
		}
	}
	if err := <-errCh; err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to read message", err.Error(), nil)
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
	var req struct {
		Flags []string `json:"flags"`
		Date  string   `json:"date"`
		Raw   string   `json:"raw"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, e.maxMsgSize+1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body", err.Error(), nil)
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
	e.log.Msg("message appended", "user", username, "mailbox", mbox,
		"admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusCreated, map[string]interface{}{"mailbox": mbox})
}

func (e *Endpoint) handleMsgsDelete(w http.ResponseWriter, r *http.Request) {
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	mbox := r.PathValue("mbox")
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "uid must be a number", "", nil)
		return
	}

	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	// Deleting messages interrupts active IMAP sessions, exactly as the CLI
	// subcommand warns.
	if err := e.deleteMessages(u, mbox, uint32(uid)); err != nil {
		e.log.Msg("message delete failed", "user", username, "mailbox", mbox, "uid", uid,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to delete message", err.Error(), nil)
		return
	}
	e.log.Msg("message deleted", "user", username, "mailbox", mbox, "uid", uid,
		"admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": uid})
}
