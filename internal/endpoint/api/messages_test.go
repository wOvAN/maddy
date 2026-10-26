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
	if err := e.store.(*fakeStorage).CreateIMAPAcct("bob@example.org"); err != nil {
		t.Fatal(err)
	}
	u, _ := e.store.GetIMAPAcct("bob@example.org")
	fu := u.(*fakeUser)

	// One message already in the mailbox, with an envelope to report.
	fu.plant("INBOX", imap.Envelope{Subject: "hi",
		From: []*imap.Address{{PersonalName: "Alice", MailboxName: "alice", HostName: "example.org"}}},
		"From: alice@example.org\r\nSubject: hi\r\n\r\nbody\r\n")

	rec := do(t, e, "GET", "/api/v1/users/bob@example.org/mailboxes/INBOX/messages", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: code %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Messages []struct {
			UID     uint32   `json:"uid"`
			Flags   []string `json:"flags"`
			Size    uint32   `json:"size"`
			Subject string   `json:"subject"`
			From    []string `json:"from"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 1 {
		t.Fatalf("messages: %s", rec.Body.String())
	}
	if list.Messages[0].Subject != "hi" || list.Messages[0].Size == 0 ||
		len(list.Messages[0].From) != 1 || list.Messages[0].From[0] != "alice@example.org" {
		t.Fatalf("envelope: %s", rec.Body.String())
	}
	uid := list.Messages[0].UID

	rec = do(t, e, "GET", "/api/v1/users/bob@example.org/mailboxes/INBOX/messages/"+itoa(uid), tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: code %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "From: alice@example.org\r\nSubject: hi\r\n\r\nbody\r\n" {
		t.Fatalf("body: %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/rfc822+text" {
		t.Fatalf("content type: %s", ct)
	}

	rec = do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes/INBOX/messages", tok,
		`{"flags":["\\Seen"],"date":"2026-10-26T10:00:00Z","raw":"From: b@example.org\r\n\r\nsecond\r\n"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("append: code %d: %s", rec.Code, rec.Body.String())
	}
	if len(fu.mboxes["INBOX"]) != 2 {
		t.Fatalf("message not appended: %d", len(fu.mboxes["INBOX"]))
	}

	rec = do(t, e, "GET", "/api/v1/users/bob@example.org/mailboxes/INBOX/messages/99999", tok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing message: code %d", rec.Code)
	}

	// The fake storage is not go-imap-sql, so EXPUNGE-style deletion is not
	// available; the same route deletes for real on a go-imap-sql backend,
	// which tests/api_test.go covers.
	rec = do(t, e, "DELETE", "/api/v1/users/bob@example.org/mailboxes/INBOX/messages/"+itoa(uid), tok, "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("delete on a non-imapsql backend: code %d", rec.Code)
	}

	big := `{"raw":"` + strings.Repeat("a", 32*1024*1024+1) + `"}`
	rec = do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes/INBOX/messages", tok, big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize append: code %d", rec.Code)
	}
}
