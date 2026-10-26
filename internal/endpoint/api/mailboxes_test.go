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
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

func TestMailboxes(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")
	if err := e.store.(*fakeStorage).CreateIMAPAcct("bob@example.org"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes", tok,
		`{"name":"Mailing Lists","special_use":"\\Archive"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: code %d: %s", rec.Code, rec.Body.String())
	}

	rec = do(t, e, "GET", "/api/v1/users/bob@example.org/mailboxes", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: code %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Mailboxes []struct {
			Name       string `json:"name"`
			SpecialUse string `json:"special_use"`
		} `json:"mailboxes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Mailboxes) != 2 { // INBOX + the new one
		t.Fatalf("mailboxes: %+v", res.Mailboxes)
	}
	if res.Mailboxes[1].Name != "Mailing Lists" || res.Mailboxes[1].SpecialUse != "\\Archive" {
		t.Fatalf("special-use not reported: %+v", res.Mailboxes[1])
	}

	rec = do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes", tok, `{"name":"Mailing Lists"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: code %d", rec.Code)
	}

	// A malformed body and a missing field are different 400s, like the user
	// routes answer them.
	rec = do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes", tok, `{nope`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "malformed request body") {
		t.Fatalf("malformed body: code %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes", tok, `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "name is required") {
		t.Fatalf("missing field: code %d: %s", rec.Code, rec.Body.String())
	}

	// go-imap-sql accepts only the five real SPECIAL-USE attributes and
	// answers anything else with ErrUnsupportedSpecialAttr (user.go:194-203);
	// the mapping to a 501 lives in statusForStoreError.
	rec = do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes", tok,
		`{"name":"X","special_use":"\\Bogus"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("bogus special-use: code %d: %s", rec.Code, rec.Body.String())
	}

	rec = do(t, e, "PUT", "/api/v1/users/bob@example.org/mailboxes/Mailing%20Lists", tok,
		`{"new_name":"Lists"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: code %d: %s", rec.Code, rec.Body.String())
	}

	// Renaming onto an existing name is a 409, not the 500 a bare
	// UNIQUE(uid, name) violation would be against go-imap-sql.
	rec = do(t, e, "PUT", "/api/v1/users/bob@example.org/mailboxes/Lists", tok,
		`{"new_name":"INBOX"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rename onto existing: code %d: %s", rec.Code, rec.Body.String())
	}

	// INBOX cannot be deleted; go-imap-sql refuses it with a bare error that
	// would reach the client as a 500.
	rec = do(t, e, "DELETE", "/api/v1/users/bob@example.org/mailboxes/INBOX", tok, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete INBOX: code %d: %s", rec.Code, rec.Body.String())
	}

	// A name containing a literal "/" is reachable only percent-encoded:
	// ServeMux matches the escaped path and PathValue unescapes it.
	rec = do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes", tok, `{"name":"Foo/Bar"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create Foo/Bar: code %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, e, "PUT", "/api/v1/users/bob@example.org/mailboxes/Foo%2FBar", tok,
		`{"new_name":"Baz/Qux"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename Foo/Bar: code %d: %s", rec.Code, rec.Body.String())
	}
	// The raw-slash form of the same path is not a route: the table cannot
	// match across "/".
	rec = do(t, e, "DELETE", "/api/v1/users/bob@example.org/mailboxes/Baz/Qux", tok, "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such endpoint") {
		t.Fatalf("raw slash form: code %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, e, "DELETE", "/api/v1/users/bob@example.org/mailboxes/Baz%2FQux", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete Baz/Qux: code %d: %s", rec.Code, rec.Body.String())
	}

	rec = do(t, e, "DELETE", "/api/v1/users/bob@example.org/mailboxes/Lists", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: code %d: %s", rec.Code, rec.Body.String())
	}

	rec = do(t, e, "DELETE", "/api/v1/users/bob@example.org/mailboxes/Lists", tok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete of missing mailbox: code %d", rec.Code)
	}

	rec = do(t, e, "GET", "/api/v1/users/ghost@example.org/mailboxes", tok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing user: code %d", rec.Code)
	}

	assertAccountBalance(t, e)
}

// TestMailboxHierarchy pins the go-imap-sql semantics the doubles mirror: a
// rename cascades to the children (renameMboxChilds) and a delete removes
// exactly one row, so a parent's children survive its deletion.
func TestMailboxHierarchy(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")
	if err := e.store.(*fakeStorage).CreateIMAPAcct("bob@example.org"); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"A", "A.B", "A.B.C"} {
		rec := do(t, e, "POST", "/api/v1/users/bob@example.org/mailboxes", tok,
			`{"name":"`+name+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s: code %d: %s", name, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, e, "PUT", "/api/v1/users/bob@example.org/mailboxes/A", tok, `{"new_name":"C"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: code %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Name  string `json:"name"`
		Moved int    `json:"moved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Name != "C" || res.Moved != 2 {
		t.Fatalf("rename must report the children it moved: %+v", res)
	}
	if names := mboxNames(t, e, tok); !sameNames(names, []string{"C", "C.B", "C.B.C", "INBOX"}) {
		t.Fatalf("after rename: %v", names)
	}

	// Plant a message so the delete response's "messages" field is asserted
	// with a non-zero value: every double mailbox held 0 messages before, so a
	// handler that stopped calling Status would stay green.
	e.store.(*fakeStorage).acct("bob@example.org").plant("C",
		imap.Envelope{Subject: "x"}, "From: a@example.org\r\nSubject: x\r\n\r\nx\r\n")

	rec = do(t, e, "DELETE", "/api/v1/users/bob@example.org/mailboxes/C", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: code %d: %s", rec.Code, rec.Body.String())
	}
	var del struct {
		Deleted    string `json:"deleted"`
		Messages   uint32 `json:"messages"`
		LeftBehind int    `json:"left_behind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &del); err != nil {
		t.Fatal(err)
	}
	if del.Deleted != "C" || del.Messages != 1 || del.LeftBehind != 2 {
		t.Fatalf("delete must report the contents it destroyed: %+v", del)
	}
	if names := mboxNames(t, e, tok); !sameNames(names, []string{"C.B", "C.B.C", "INBOX"}) {
		t.Fatalf("after delete: %v", names)
	}

	assertAccountBalance(t, e)
}

// mboxNames lists the user's mailbox names in the order the store reports them.
func mboxNames(t *testing.T, e *Endpoint, tok string) []string {
	t.Helper()
	rec := do(t, e, "GET", "/api/v1/users/bob@example.org/mailboxes", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: code %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Mailboxes []struct {
			Name string `json:"name"`
		} `json:"mailboxes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Mailboxes))
	for _, m := range res.Mailboxes {
		names = append(names, m.Name)
	}
	return names
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// assertAccountBalance fails the suite if an account object was borrowed from
// the store and not released with Logout: every GetIMAPAcct hand-out
// (fakeStorage.gets) must be matched by a Logout (fakeUser.logouts), so a
// mailbox handler that drops its defer goes red here.
func assertAccountBalance(t *testing.T, e *Endpoint) {
	t.Helper()
	fs := e.store.(*fakeStorage)
	fs.mu.Lock()
	gets := fs.gets
	accts := make([]*fakeUser, 0, len(fs.accts))
	for _, u := range fs.accts {
		accts = append(accts, u)
	}
	fs.mu.Unlock()
	logouts := 0
	for _, u := range accts {
		u.mu.Lock()
		logouts += u.logouts
		u.mu.Unlock()
	}
	if gets != logouts {
		t.Fatalf("leaked account objects: %d GetIMAPAcct, %d Logout", gets, logouts)
	}
}
