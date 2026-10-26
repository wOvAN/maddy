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
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	errBadToken = errors.New("malformed or unsigned token")
	errExpired  = errors.New("expired token")
)

// makeToken builds a stateless bearer token: base64url(username|exp) and a
// hex HMAC-SHA256 over exactly that payload.
//
// Tokens are not stored anywhere on purpose. A SIGUSR2 reload builds a brand
// new container and a brand new instance of this module, so any in-memory
// session map would log every administrator out on every config reload. The
// price is that a token cannot be revoked before it expires; revocation is
// done by changing the secret directive and reloading.
func (e *Endpoint) makeToken(username string, exp time.Time) (string, error) {
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(username + "|" + strconv.FormatInt(exp.Unix(), 10)))
	mac := hmac.New(sha256.New, e.secret)
	if _, err := mac.Write([]byte(payload)); err != nil {
		return "", err
	}
	return payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func (e *Endpoint) verifyToken(token string) (string, error) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || payload == "" || sig == "" {
		return "", errBadToken
	}

	want, err := hex.DecodeString(sig)
	if err != nil {
		return "", errBadToken
	}
	mac := hmac.New(sha256.New, e.secret)
	if _, err := mac.Write([]byte(payload)); err != nil {
		return "", errBadToken
	}
	if subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return "", errBadToken
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", errBadToken
	}
	user, expStr, ok := strings.Cut(string(raw), "|")
	if !ok || user == "" {
		return "", errBadToken
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", errBadToken
	}
	if time.Now().Unix() > exp {
		return "", errExpired
	}
	return user, nil
}

// rateLimiter is a fixed-window attempt counter, used for login attempts only.
type rateLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	attempts map[string][]time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, attempts: make(map[string][]time.Time)}
}

func (r *rateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	kept := r.attempts[key][:0]
	for _, t := range r.attempts[key] {
		if now.Sub(t) < r.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.max {
		r.attempts[key] = kept
		return false
	}
	r.attempts[key] = append(kept, now)
	return true
}
