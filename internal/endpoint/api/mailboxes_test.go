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
	"testing"
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

	rec = do(t, e, "PUT", "/api/v1/users/bob@example.org/mailboxes/Mailing%20Lists", tok,
		`{"new_name":"Lists"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: code %d: %s", rec.Code, rec.Body.String())
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
}
