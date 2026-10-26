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
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

func TestMessages(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")
	fs := e.store.(*fakeStorage)
	if err := fs.CreateIMAPAcct("bob@example.org"); err != nil {
		t.Fatal(err)
	}
	// acct, not GetIMAPAcct: a test-side grab must not count against
	// assertAccountBalance, which measures handler leaks only.
	fu := fs.acct("bob@example.org")
	const base = "/api/v1/users/bob@example.org/mailboxes/INBOX/messages"

	// One message already in the mailbox, with an envelope to report.
	fu.plant("INBOX", imap.Envelope{Subject: "hi",
		MessageId: "<m1@example.org>", InReplyTo: "<m0@example.org>",
		From: []*imap.Address{{PersonalName: "Alice", MailboxName: "alice", HostName: "example.org"}},
		Cc:   []*imap.Address{{MailboxName: "carol", HostName: "example.org"}}},
		"From: alice@example.org\r\nSubject: hi\r\n\r\nbody\r\n")

	rec := do(t, e, "GET", base, tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: code %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Messages []struct {
			UID          uint32   `json:"uid"`
			Flags        []string `json:"flags"`
			Size         uint32   `json:"size"`
			Subject      string   `json:"subject"`
			From         []string `json:"from"`
			CC           []string `json:"cc"`
			MessageID    string   `json:"message_id"`
			InReplyTo    string   `json:"in_reply_to"`
			InternalDate string   `json:"internal_date"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 1 {
		t.Fatalf("messages: %s", rec.Body.String())
	}
	m := list.Messages[0]
	if m.Subject != "hi" || m.Size == 0 ||
		len(m.From) != 1 || m.From[0] != "alice@example.org" {
		t.Fatalf("envelope: %s", rec.Body.String())
	}
	// The CLI prints the whole envelope; the route must not drop the rest of
	// it, and "date" must stop being the arrival date's alias.
	if len(m.CC) != 1 || m.CC[0] != "carol@example.org" ||
		m.MessageID != "<m1@example.org>" || m.InReplyTo != "<m0@example.org>" ||
		m.InternalDate == "" {
		t.Fatalf("envelope fields: %s", rec.Body.String())
	}
	uid := m.UID

	// A flag-less message serialises flags as [], like the empty list does.
	var rawList struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawList); err != nil {
		t.Fatal(err)
	}
	if _, ok := rawList.Messages[0]["flags"].([]interface{}); !ok {
		t.Fatalf("flags must serialise as [], not null: %s", rec.Body.String())
	}

	rec = do(t, e, "GET", base+"/"+itoa(uid), tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: code %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "From: alice@example.org\r\nSubject: hi\r\n\r\nbody\r\n" {
		t.Fatalf("body: %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/rfc822+text" {
		t.Fatalf("content type: %s", ct)
	}

	// uid 0 is a destructive wildcard (go-imap-mess ResolveSeq rewrites a
	// zero uid bound to the mailbox's highest uid, mailbox.go:60-65): it must
	// never reach the store, on the read path or the destructive one.
	rec = do(t, e, "GET", base+"/0", tok, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("get uid 0: code %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, e, "DELETE", base+"/0", tok, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete uid 0: code %d: %s", rec.Code, rec.Body.String())
	}

	rec = do(t, e, "POST", base, tok,
		`{"flags":["\\Seen"],"date":"2026-10-26T10:00:00Z","raw":"From: b@example.org\r\n\r\nsecond\r\n"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("append: code %d: %s", rec.Code, rec.Body.String())
	}
	// The append reports the uid it created (Status UidNext-1, the CLI's
	// instrument), not just the mailbox it was told about.
	var app struct {
		Mailbox string `json:"mailbox"`
		UID     uint32 `json:"uid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &app); err != nil {
		t.Fatal(err)
	}
	if app.Mailbox != "INBOX" || app.UID != 2 {
		t.Fatalf("append must report the created uid: %s", rec.Body.String())
	}
	if len(fu.mboxes["INBOX"]) != 2 {
		t.Fatalf("message not appended: %d", len(fu.mboxes["INBOX"]))
	}

	// An empty message is refused, as the CLI refuses it (imap.go:569-571).
	rec = do(t, e, "POST", base, tok, `{"raw":""}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "raw is required") {
		t.Fatalf("empty append: code %d: %s", rec.Code, rec.Body.String())
	}

	rec = do(t, e, "GET", base+"/99999", tok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing message: code %d", rec.Code)
	}

	rec = do(t, e, "DELETE", base+"/"+itoa(uid), tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: code %d: %s", rec.Code, rec.Body.String())
	}
	// The delete reports the destruction, not the request: two messages were
	// in the mailbox, one is gone.
	var del struct {
		UID     uint32 `json:"uid"`
		Deleted uint32 `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &del); err != nil {
		t.Fatal(err)
	}
	if del.UID != uid || del.Deleted != 1 {
		t.Fatalf("delete must report what it removed: %s", rec.Body.String())
	}
	if len(fu.mboxes["INBOX"]) != 1 {
		t.Fatalf("message not deleted: %d", len(fu.mboxes["INBOX"]))
	}

	// A uid that is not there is a 404 (mess.ErrNoMessages), not a 200
	// claiming a deletion that destroyed nothing.
	rec = do(t, e, "DELETE", base+"/99999", tok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete of a missing message: code %d: %s", rec.Code, rec.Body.String())
	}

	big := `{"raw":"` + strings.Repeat("a", 32*1024*1024+1) + `"}`
	rec = do(t, e, "POST", base, tok, big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize append: code %d", rec.Code)
	}

	// Escaped quotes inflate the JSON to ~2x the raw length, past the
	// decoder's headroom, so the transport-length precheck is what answers
	// 413 here; without it the decode dies on syntax and the client gets 400.
	bigEsc := `{"raw":"` + strings.Repeat(`\"`, 16*1024*1024+40000) + `"}`
	rec = do(t, e, "POST", base, tok, bigEsc)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize escaped append: code %d: %s", rec.Code, rec.Body.String())
	}

	// A body over the cap must never be served truncated as if complete:
	// the fetch reads maxMsgSize+1 and the cap being hit is a 413.
	e.maxMsgSize = 8
	rec = do(t, e, "GET", base+"/2", tok, "")
	e.maxMsgSize = 32 * 1024 * 1024
	if rec.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(rec.Body.String(), "max_msg_size") {
		t.Fatalf("oversize get: code %d: %s", rec.Code, rec.Body.String())
	}

	// APPENDLIMIT exceeded on append is the store's ErrTooBig, a 413.
	limit := uint32(4)
	if err := fu.SetMessageLimit(&limit); err != nil {
		t.Fatal(err)
	}
	rec = do(t, e, "POST", base, tok, `{"raw":"12345"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("appendlimit: code %d: %s", rec.Code, rec.Body.String())
	}
	if err := fu.SetMessageLimit(nil); err != nil {
		t.Fatal(err)
	}

	// A literal that fails mid-read — go-imap-sql hands out a live literal
	// over the decompressed extStore blob (fetch.go:230-251), so the read can
	// die partway — is a read failure, not a 200 serving a prefix of the body
	// as if it were the message.
	fu.failBodyAt = 4
	rec = do(t, e, "GET", base+"/2", tok, "")
	fu.failBodyAt = 0
	if rec.Code != http.StatusInternalServerError ||
		!strings.Contains(rec.Body.String(), "read message body") {
		t.Fatalf("body read failure: code %d: %s", rec.Code, rec.Body.String())
	}

	// A producer that returns without closing ch must not pin the handler
	// goroutine (EarlyStop's drain in Task 10 depends on every request
	// returning): drive the contract-violating producer 20 times and demand
	// an answer every time.
	fu.listNoClose = true
	for i := 0; i < 20; i++ {
		rec = do(t, e, "GET", base, tok, "")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("unclosed producer: code %d: %s", rec.Code, rec.Body.String())
		}
	}
	fu.listNoClose = false

	// Every mailbox handle the handlers borrowed was closed, so a dropped
	// defer mb.Close() goes red here as Logout does for accounts.
	fu.mu.Lock()
	opens, closes := fu.opens, fu.closes
	fu.mu.Unlock()
	if opens != closes {
		t.Fatalf("leaked mailbox handles: %d GetMailbox, %d Close", opens, closes)
	}
	assertAccountBalance(t, e)
}
