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
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func loginEndpoint(t *testing.T) *Endpoint {
	t.Helper()
	e := testAuthEndpoint(t)
	e.creds = newFakeCreds()
	e.store = newFakeStorage()
	if err := e.creds.CreateUser("admin@example.org", "hunter2hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := e.creds.CreateUser("bob@example.org", "hunter3hunter3"); err != nil {
		t.Fatal(err)
	}
	e.limiter = newRateLimiter(e.loginLimit, e.loginWindow)
	e.serv.Handler = e.handler()
	return e
}

func do(t *testing.T, e *Endpoint, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.serv.Handler.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, e *Endpoint, user, pass string) string {
	t.Helper()
	rec := do(t, e, "POST", "/api/v1/login", "", `{"username":"`+user+`","password":"`+pass+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: code %d body %s", user, rec.Code, rec.Body.String())
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Token == "" {
		t.Fatal("empty token")
	}
	return res.Token
}

func TestLogin(t *testing.T) {
	e := loginEndpoint(t)

	rec := do(t, e, "POST", "/api/v1/login", "", `{"username":"admin@example.org","password":"hunter2hunter2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
		Username  string    `json:"username"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Username != "admin@example.org" {
		t.Fatalf("subject: %s", res.Username)
	}
	if time.Until(res.ExpiresAt) < time.Hour {
		t.Fatalf("expires too soon: %s", res.ExpiresAt)
	}
}

func TestLoginDenied(t *testing.T) {
	e := loginEndpoint(t)

	// bob has a valid mailbox password but is not in admins.
	rec := do(t, e, "POST", "/api/v1/login", "", `{"username":"bob@example.org","password":"hunter3hunter3"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin login: code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not an administrator") {
		t.Fatalf("body: %s", rec.Body.String())
	}

	rec = do(t, e, "POST", "/api/v1/login", "", `{"username":"admin@example.org","password":"wrong"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password: code %d", rec.Code)
	}
}

func TestLoginRateLimited(t *testing.T) {
	e := loginEndpoint(t)
	e.loginLimit = 2
	e.limiter = newRateLimiter(2, time.Minute)

	for i := 0; i < 2; i++ {
		if rec := do(t, e, "POST", "/api/v1/login", "", `{"username":"admin@example.org","password":"nope"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: code %d", i, rec.Code)
		}
	}
	rec := do(t, e, "POST", "/api/v1/login", "", `{"username":"admin@example.org","password":"hunter2hunter2"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit not enforced: code %d", rec.Code)
	}
}

func TestRequireAdmin(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	// Task 4 registers no admin route (the first one arrives in Task 5), so the
	// middleware is exercised through a probe handler.
	probe := e.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"admin": ctxAdminUser(r.Context())})
	})
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/probe", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		probe.ServeHTTP(rec, req)
		return rec
	}

	if rec := call(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code %d", rec.Code)
	}
	if rec := call(tok + "x"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: code %d", rec.Code)
	}
	rec := call(tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token rejected: code %d", rec.Code)
	}
	var res struct {
		Admin string `json:"admin"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Admin != "admin@example.org" {
		t.Fatalf("context subject: %s", res.Admin)
	}
}

func TestAuthHeadersAndEnvelope(t *testing.T) {
	e := loginEndpoint(t)
	tok := login(t, e, "admin@example.org", "hunter2hunter2")

	// The auth scheme is case-insensitive.
	req := httptest.NewRequest("GET", "/api/v1/probe", nil)
	req.Header.Set("Authorization", "bearer "+tok)
	rec := httptest.NewRecorder()
	e.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"admin": ctxAdminUser(r.Context())})
	}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("lowercase scheme: code %d", rec.Code)
	}

	// A missing account and a wrong password must answer identically.
	unknown := do(t, e, "POST", "/api/v1/login", "", `{"username":"nobody@example.org","password":"hunter2hunter2"}`)
	wrong := do(t, e, "POST", "/api/v1/login", "", `{"username":"admin@example.org","password":"hunter2hunter3"}`)
	if unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("login answers differ: %d %s vs %d %s",
			unknown.Code, unknown.Body.String(), wrong.Code, wrong.Body.String())
	}
	if len(dummyHash) == 0 {
		t.Fatal("no dummy hash to spend the equal-cost comparison on")
	}

	// Router misses and wrong methods use the JSON envelope, and 429 says when
	// to come back.
	rec = do(t, e, "GET", "/api/v1/nope", tok, "")
	if rec.Code != http.StatusNotFound ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("404 envelope: code %d type %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = do(t, e, "GET", "/api/v1/login", tok, "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("405 envelope: code %d", rec.Code)
	}
	for i := 0; i < e.loginLimit+2; i++ {
		do(t, e, "POST", "/api/v1/login", "", `{"username":"nobody@example.org","password":"x"}`)
	}
	rec = do(t, e, "POST", "/api/v1/login", "", `{"username":"nobody@example.org","password":"x"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("429: code %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestHealth(t *testing.T) {
	e := loginEndpoint(t)
	rec := do(t, e, "GET", "/api/v1/health", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("body: %s", rec.Body.String())
	}
}
