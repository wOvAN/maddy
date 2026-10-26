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
	"errors"
	"strings"
	"testing"
	"time"
)

func testAuthEndpoint(t *testing.T) *Endpoint {
	t.Helper()
	e, err := configureTestEndpoint(t, cfgEndpointOpts{
		admins: []string{"admin@example.org"},
		secret: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestTokenRoundTrip(t *testing.T) {
	e := testAuthEndpoint(t)
	tok, err := e.makeToken("admin@example.org", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	user, err := e.verifyToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if user != "admin@example.org" {
		t.Fatalf("unexpected subject: %s", user)
	}
}

func TestTokenRejects(t *testing.T) {
	e := testAuthEndpoint(t)
	other, err := configureTestEndpoint(t, cfgEndpointOpts{
		admins: []string{"admin@example.org"},
		secret: "fedcba9876543210fedcba9876543210",
	})
	if err != nil {
		t.Fatal(err)
	}

	expired, err := e.makeToken("admin@example.org", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := e.makeToken("admin@example.org", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Flip the last signature nibble to a guaranteed-different value so the
	// tampered token is never identical to the original one.
	flip := byte('0')
	if last := tok[len(tok)-1]; last == flip {
		flip = '1'
	}

	for _, tc := range []struct {
		name  string
		ep    *Endpoint
		token string
		want  error
	}{
		{"empty", e, "", errBadToken},
		{"no dot", e, "deadbeef", errBadToken},
		{"expired", e, expired, errExpired},
		{"tampered", e, tok[:len(tok)-1] + string(flip), errBadToken},
		{"other secret", other, tok, errBadToken},
	} {
		if _, err := tc.ep.verifyToken(tc.token); !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}
}

func TestTokenIsNotPortableToOtherSubject(t *testing.T) {
	e := testAuthEndpoint(t)
	tok, err := e.makeToken("admin@example.org", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// The subject is inside the signed payload, so editing it must break the
	// signature rather than be trusted.
	parts := strings.SplitN(tok, ".", 2)
	user, err := e.verifyToken(strings.Repeat("0", len(parts[0])) + "." + parts[1])
	if err == nil {
		t.Fatalf("unsigned payload accepted, subject: %s", user)
	}
}

func TestRateLimiterWindow(t *testing.T) {
	r := newRateLimiter(2, 50*time.Millisecond)
	if !r.Allow("1.2.3.4") || !r.Allow("1.2.3.4") {
		t.Fatal("first two attempts must pass")
	}
	if r.Allow("1.2.3.4") {
		t.Fatal("third attempt must be limited")
	}
	if !r.Allow("5.6.7.8") {
		t.Fatal("limiting must be per key")
	}
	time.Sleep(60 * time.Millisecond)
	if !r.Allow("1.2.3.4") {
		t.Fatal("window must expire")
	}
}
