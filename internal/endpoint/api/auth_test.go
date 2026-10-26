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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
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

// TestTokenRejectsPayloadShape pins the payload framing: the separator is
// counted from the right, an empty subject is not a subject, and exp is parsed
// strictly (see parseExp) so that a payload cannot carry an exp that outlives
// tokenTTL.
func TestTokenRejectsPayloadShape(t *testing.T) {
	e := testAuthEndpoint(t)

	// sign is the module's own signature over an arbitrary payload, i.e. the
	// strongest thing a client can have if it ever gets a valid token.
	sign := func(payload string) string {
		t.Helper()
		mac := hmac.New(sha256.New, e.secret)
		if _, err := mac.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		return payload + "." + hex.EncodeToString(mac.Sum(nil))
	}
	b64 := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	future := time.Now().Unix() + int64((tokenTTL + time.Hour).Seconds())

	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"no separator", b64("admin@example.org")},
		{"empty subject", b64("|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))},
		{"exp signed", b64("admin@example.org|+" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))},
		{"exp leading zeros", b64("admin@example.org|0" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))},
		{"exp saturating", b64("admin@example.org|999999999999999999999999")},
		{"exp far future", b64("admin@example.org|" + strconv.FormatInt(future, 10))},
	} {
		if user, err := e.verifyToken(sign(tc.payload)); !errors.Is(err, errBadToken) {
			t.Errorf("%s: want errBadToken, got %v (subject %q)", tc.name, err, user)
		}
	}

	// The normal case must still work with an exp exactly at the boundary.
	tok, err := e.makeToken("admin@example.org", time.Now().Add(tokenTTL))
	if err != nil {
		t.Fatal(err)
	}
	if user, err := e.verifyToken(tok); err != nil || user != "admin@example.org" {
		t.Fatalf("token at the TTL boundary rejected: %v (%q)", err, user)
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

func TestRateLimiterSweepPrunesQuietKeys(t *testing.T) {
	r := newRateLimiter(10, 20*time.Millisecond)
	for i := 0; i < 4100; i++ {
		if !r.Allow(strconv.Itoa(i/256) + "." + strconv.Itoa(i%256)) {
			t.Fatal("distinct keys must not be limited")
		}
	}
	if len(r.attempts) != 4100 {
		t.Fatalf("expected 4100 tracked keys, got %d", len(r.attempts))
	}
	// Once every entry is out of the window, the next allow sweeps the map
	// back down to the key being touched.
	time.Sleep(30 * time.Millisecond)
	if !r.Allow("10.0.0.1") {
		t.Fatal("swept key must be allowed again")
	}
	if len(r.attempts) != 1 {
		t.Fatalf("sweep must drop quiet keys, got %d tracked keys", len(r.attempts))
	}
}
