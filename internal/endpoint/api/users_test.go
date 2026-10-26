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
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/foxcpp/maddy/framework/module"
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
	// The test's own fetch must be released too, so the gets/logouts balance
	// below covers it.
	if err := u.Logout(); err != nil {
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

	// Every account the handler borrowed from the store must be released.
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

func TestUsersCreate(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"dave@example.org","password":"pppppppppp","storage":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if err := e.creds.AuthPlain("dave@example.org", "pppppppppp"); err != nil {
		t.Fatal("credential not created:", err)
	}
	u, err := e.store.GetIMAPAcct("dave@example.org")
	if err != nil {
		t.Fatal("mailbox not created:", err)
	}
	fu := u.(*fakeUser)
	for _, name := range []string{"Sent", "Drafts", "Archive", "Junk", "Trash"} {
		if _, ok := fu.special[name]; !ok {
			t.Fatalf("special-use folder %s not created", name)
		}
	}
}

func TestUsersCreateNormalizesUsername(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"Dave@Example.ORG","password":"pppppppppp","storage":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Username != "dave@example.org" {
		t.Fatalf("must store the normalized address: %s", res.Username)
	}
	if err := e.creds.AuthPlain("dave@example.org", "pppppppppp"); err != nil {
		t.Fatal("credential created under the raw spelling:", err)
	}

	// The same normalization applies to DELETE.
	rec = do(t, e, "DELETE", "/api/v1/users/Dave@Example.ORG", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete by raw spelling: code %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsersCreateAppendlimitNeedsStorage(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	// The creds-only path returns before reading req.Appendlimit, so a
	// request carrying the limit there used to get a 201, no limit and no
	// signal. The refusal must name the reason and create nothing.
	for _, body := range []string{
		`{"username":"dave@example.org","password":"pppppppppp","appendlimit":1024}`,
		`{"username":"dave@example.org","password":"pppppppppp","storage":false,"appendlimit":1024}`,
	} {
		rec := do(t, e, "POST", "/api/v1/users", tok, body)
		if rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "appendlimit requires storage:true") {
			t.Fatalf("%s: code %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	if err := e.creds.AuthPlain("dave@example.org", "pppppppppp"); !errors.Is(err, module.ErrUnknownCredentials) {
		t.Fatal("a refused create must not leave a credential")
	}

	// storage:true with appendlimit must still be accepted and applied.
	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"dave@example.org","password":"pppppppppp","storage":true,"appendlimit":1024}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	u, err := e.store.GetIMAPAcct("dave@example.org")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Logout() }()
	if lim := u.(*fakeUser).CreateMessageLimit(); lim == nil || *lim != 1024 {
		t.Fatalf("appendlimit not applied: %v", lim)
	}
}

func TestUsersCreateConflict(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")
	// bob has credentials from loginEndpoint, so this must be refused.
	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"bob@example.org","password":"pppppppppp"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsersCreateRollsBackCreds(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	fs := e.store.(*fakeStorage)
	fs.createErr = errors.New("database is locked")

	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"dave@example.org","password":"pppppppppp","storage":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if err := e.creds.AuthPlain("dave@example.org", "pppppppppp"); !errors.Is(err, module.ErrUnknownCredentials) {
		t.Fatal("credential must be rolled back when the mailbox cannot be created")
	}
	if !strings.Contains(rec.Body.String(), `"partial":[]`) {
		t.Fatalf("a clean rollback must report an empty partial: %s", rec.Body.String())
	}
}

func TestUsersCreateRollsBackCleanly(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	fs := e.store.(*fakeStorage)
	fs.createErr = errors.New("database is locked")

	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"dave@example.org","password":"pppppppppp","storage":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"partial":[]`) {
		t.Fatalf("nothing may survive a failed storage create: %s", rec.Body.String())
	}
	if err := e.creds.AuthPlain("dave@example.org", "pppppppppp"); !errors.Is(err, module.ErrUnknownCredentials) {
		t.Fatal("credential must not survive the rollback")
	}
	accts, err := fs.ListIMAPAccts()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accts {
		if a == "dave@example.org" {
			t.Fatal("mailbox must not survive the rollback")
		}
	}
}

func TestUsersCreateRollsBackMailbox(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	fs := e.store.(*fakeStorage)
	// The account is created, then folder creation fails: the mailbox itself
	// must be rolled back, not stranded behind the rolled-back credential.
	fs.mboxErr = errors.New("folder creation failed")

	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"dave@example.org","password":"pppppppppp","storage":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"partial":[]`) {
		t.Fatalf("both halves must be rolled back: %s", rec.Body.String())
	}
	accts, err := fs.ListIMAPAccts()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accts {
		if a == "dave@example.org" {
			t.Fatal("the mailbox must be rolled back, not stranded")
		}
	}
}

func TestUsersCreateReportsStrandedCreds(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	fs := e.store.(*fakeStorage)
	fs.mboxErr = errors.New("folder creation failed")
	e.creds.(*fakeCreds).deleteErr = errors.New("database is locked")

	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"dave@example.org","password":"pppppppppp","storage":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"partial":["creds"]`) {
		t.Fatalf("the stranded credential must be named: %s", rec.Body.String())
	}
	// The mailbox half was still rolled back...
	accts, err := fs.ListIMAPAccts()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accts {
		if a == "dave@example.org" {
			t.Fatal("the mailbox must be rolled back even when the credential is not")
		}
	}
	// ...and the credential is what is left behind.
	if err := e.creds.AuthPlain("dave@example.org", "pppppppppp"); err != nil {
		t.Fatal("the stranded credential must still authenticate:", err)
	}
}

func TestUsersDeleteUnmanageableStorage(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	e.store = &storageOnly{f: e.store.(*fakeStorage)}

	rec := do(t, e, "DELETE", "/api/v1/users/bob@example.org", tok, "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"deleted":["creds"]`) || !strings.Contains(body, `"failed":["storage"]`) {
		t.Fatalf("must report the split outcome: %s", body)
	}
}

func TestUsersDeleteMissing(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	rec := do(t, e, "DELETE", "/api/v1/users/nobody@example.org", tok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleting a missing user is a 404: code %d: %s", rec.Code, rec.Body.String())
	}
	// Both halves are missing, so "deleted" is empty and must serialize as
	// [], not null: the client branches on membership.
	if !strings.Contains(rec.Body.String(), `"deleted":[`) {
		t.Fatalf("an empty deleted half must serialize as an array: %s", rec.Body.String())
	}
}

func TestUsersCreateStorageListFailure(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	fs := e.store.(*fakeStorage)
	// The global knob breaks ListIMAPAccts, so the existence lookup cannot
	// finish; a failed listing must not read as "no mailbox exists".
	fs.err = errors.New("database is locked")

	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"dave@example.org","password":"pppppppppp","storage":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "failed to look up user") {
		t.Fatalf("a failed storage listing must not read as 'no mailbox': %s", rec.Body.String())
	}
	if err := e.creds.AuthPlain("dave@example.org", "pppppppppp"); !errors.Is(err, module.ErrUnknownCredentials) {
		t.Fatal("no credential may be created when the existence lookup fails")
	}
}

func TestUsersCreateRejectsBareLocalpart(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	rec := do(t, e, "POST", "/api/v1/users", tok,
		`{"username":"postmaster","password":"pppppppppp","storage":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a bare localpart must be refused: code %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsersDeletePartial(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")
	if err := e.store.(*fakeStorage).CreateIMAPAcct("bob@example.org"); err != nil {
		t.Fatal(err)
	}
	fs := e.store.(*fakeStorage)
	fs.err = errors.New("database is locked")

	rec := do(t, e, "DELETE", "/api/v1/users/bob@example.org", tok, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"creds"`) || !strings.Contains(body, `"storage"`) {
		t.Fatalf("must report both stores: %s", body)
	}
	if err := e.creds.AuthPlain("bob@example.org", "hunter3hunter3"); !errors.Is(err, module.ErrUnknownCredentials) {
		t.Fatal("credential must be deleted even though storage failed")
	}
}

func TestUserPassword(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	rec := do(t, e, "PUT", "/api/v1/users/bob@example.org/password", tok, `{"password":"newnewnew"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if err := e.creds.AuthPlain("bob@example.org", "newnewnew"); err != nil {
		t.Fatal("password not changed")
	}

	rec = do(t, e, "PUT", "/api/v1/users/ghost@example.org/password", tok, `{"password":"newnewnew"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing user: code %d", rec.Code)
	}
	rec = do(t, e, "PUT", "/api/v1/users/bob@example.org/password", tok, `{"password":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: code %d", rec.Code)
	}

	// A password change must not create an account: pass_table.SetUserPassword
	// inserts (its table's SetKey runs the INSERT first), so this 404 is
	// enforced by the handler, not by the double.
	rec = do(t, e, "PUT", "/api/v1/users/ghost@example.org/password", tok, `{"password":"newnewnew"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing user: code %d", rec.Code)
	}
	if err := e.creds.AuthPlain("ghost@example.org", "newnewnew"); err == nil {
		t.Fatal("password change created a credential for a nonexistent user")
	}
}

func TestUserAppendlimitValidation(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")
	if err := e.store.(*fakeStorage).CreateIMAPAcct("bob@example.org"); err != nil {
		t.Fatal(err)
	}
	u, _ := e.store.GetIMAPAcct("bob@example.org")
	fu := u.(*fakeUser)

	// An absent value must not clear the limit; null must.
	require := func(body string, want int, wantSet bool) {
		t.Helper()
		rec := do(t, e, "PUT", "/api/v1/users/bob@example.org/appendlimit", tok, body)
		if rec.Code != want {
			t.Fatalf("%s: code %d: %s", body, rec.Code, rec.Body.String())
		}
		if lim := fu.CreateMessageLimit(); (lim != nil) != wantSet {
			t.Fatalf("%s: limit set %v, want set %v", body, lim != nil, wantSet)
		}
	}
	require(`{"value":1024}`, http.StatusOK, true)
	if lim := fu.CreateMessageLimit(); lim == nil || *lim != 1024 {
		t.Fatalf("1024: %v", lim)
	}
	require(`{"value":0}`, http.StatusOK, true) // 0 is a real limit, not "unset"
	if lim := fu.CreateMessageLimit(); lim == nil || *lim != 0 {
		t.Fatalf("0: %v", lim)
	}
	require(`{}`, http.StatusBadRequest, true)            // absent must not clear
	require(`{"value":"x"}`, http.StatusBadRequest, true) // not a number
	require(`{"value":null}`, http.StatusOK, false)       // null clears
}

func TestUserAppendlimit(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")
	if err := e.store.(*fakeStorage).CreateIMAPAcct("bob@example.org"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, e, "PUT", "/api/v1/users/bob@example.org/appendlimit", tok, `{"value":4096}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	u, _ := e.store.GetIMAPAcct("bob@example.org")
	if lim := u.(*fakeUser).CreateMessageLimit(); lim == nil || *lim != 4096 {
		t.Fatalf("limit not set: %v", lim)
	}

	rec = do(t, e, "PUT", "/api/v1/users/bob@example.org/appendlimit", tok, `{"value":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: code %d", rec.Code)
	}
	if lim := u.(*fakeUser).CreateMessageLimit(); lim != nil {
		t.Fatal("limit must be cleared by null")
	}

	rec = do(t, e, "PUT", "/api/v1/users/ghost@example.org/appendlimit", tok, `{"value":1}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing account: code %d", rec.Code)
	}
}
