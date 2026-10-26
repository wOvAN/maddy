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

type usersListRes struct {
	Users []struct {
		Username    string  `json:"username"`
		Creds       bool    `json:"creds"`
		Storage     bool    `json:"storage"`
		Appendlimit *uint32 `json:"appendlimit"`
	} `json:"users"`
}

func TestUsersList(t *testing.T) {
	e := loginEndpoint(t)
	if err := e.store.(*fakeStorage).CreateIMAPAcct("bob@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.(*fakeStorage).CreateIMAPAcct("admin@example.org"); err != nil {
		t.Fatal(err)
	}
	u, err := e.store.GetIMAPAcct("bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	limit := uint32(1024)
	if err := u.(*fakeUser).SetMessageLimit(&limit); err != nil {
		t.Fatal(err)
	}
	// carol exists in creds only.
	if err := e.creds.CreateUser("carol@example.org", "xoxoxoxo"); err != nil {
		t.Fatal(err)
	}

	tok := login(t, e, "admin@example.org", "hunter2hunter2")
	rec := do(t, e, "GET", "/api/v1/users", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}

	var res usersListRes
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Users) != 3 {
		t.Fatalf("want 3 users, got %d: %s", len(res.Users), rec.Body.String())
	}
	want := []string{"admin@example.org", "bob@example.org", "carol@example.org"}
	for i, name := range want {
		if res.Users[i].Username != name {
			t.Fatalf("user %d: want %s, got %s", i, name, res.Users[i].Username)
		}
	}
	if !res.Users[2].Creds || res.Users[2].Storage {
		t.Fatal("carol must be creds-only")
	}
	if !res.Users[1].Storage || *res.Users[1].Appendlimit != 1024 {
		t.Fatal("bob's applitmit must be reported")
	}
}
