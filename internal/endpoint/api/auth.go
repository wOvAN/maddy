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
	"sync"
	"time"

	"github.com/foxcpp/maddy/framework/container"
)

var (
	errBadToken = errors.New("malformed or unsigned token")
	errExpired  = errors.New("expired token")
)

// tokenTTL is the token lifetime issued by handleLogin; verifyToken refuses
// anything signed for longer, so the two cannot drift apart.
const tokenTTL = 12 * time.Hour

// The endpoint is started and stopped by the container; EarlyStop (api.go)
// asserts the reload half of the same contract.
var _ container.LifetimeModule = &Endpoint{}

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
	return payload + "." + hex.EncodeToString(hmacSHA256(e.secret, payload)), nil
}

func hmacSHA256(secret []byte, msg string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
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
	// hmac.Equal is constant-time and length-checked; the signature is verified
	// over the payload bytes before any of them are parsed.
	if !hmac.Equal(hmacSHA256(e.secret, payload), want) {
		return "", errBadToken
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", errBadToken
	}
	// The accepted encoding must be canonical: base64url tolerates non-zero
	// padding bits and hex tolerates upper case, so one logical token would
	// otherwise have many byte-distinct spellings. That defeats any future
	// per-token revocation list, rotation tracking or log dedup.
	if base64.RawURLEncoding.EncodeToString(raw) != payload ||
		hex.EncodeToString(want) != sig {
		return "", errBadToken
	}
	// Split at the LAST separator: a payload split from the front would silently
	// misread exp for a subject containing the separator.
	sep := strings.LastIndexByte(string(raw), '|')
	if sep <= 0 {
		return "", errBadToken
	}
	user, expStr := string(raw)[:sep], string(raw)[sep+1:]
	exp, err := parseExp(expStr)
	if err != nil {
		return "", errBadToken
	}
	now := time.Now().Unix()
	if now > exp {
		return "", errExpired
	}
	// The token is signed, so a client cannot pick exp — but the minting side
	// must not be the only place that enforces the TTL: reject an exp beyond
	// the lifetime this module issues, so a bug in makeToken (or a caller that
	// passes a huge exp) cannot mint a near-eternal token.
	if exp > now+int64(tokenTTL.Seconds())+60 {
		return "", errBadToken
	}
	return user, nil
}

// parseExp parses the exp field of a token payload strictly: plain decimal, no
// sign, no leading zeros, at most 19 digits. strconv.ParseInt accepts "+5",
// "-5" and 25-digit strings that saturate to MaxInt64; a saturated exp would
// outlive every reload, so the format is checked before parsing.
func parseExp(s string) (int64, error) {
	if s == "" || len(s) > 19 || (len(s) > 1 && s[0] == '0') {
		return 0, errBadToken
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errBadToken
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

// rateLimiter is a sliding-window attempt counter, used for login attempts
// only: each allowed attempt is remembered and counted against the window.
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
	if len(kept) == 0 {
		// Forget keys whose window has passed, so the map is bounded by the
		// number of clients active inside one window rather than by the number
		// of client IPs ever seen (the port can be published).
		delete(r.attempts, key)
	} else {
		r.attempts[key] = kept
	}
	// ponytail: prune-on-touch only forgets keys that come back, and a flood
	// of distinct source IPs, one request each, is exactly the shape that
	// never comes back; a full sweep past 4096 keys keeps the map bounded
	// regardless. Upgrade path: a periodic sweep goroutine if 4096 is hit in
	// practice.
	if len(r.attempts) > 4096 {
		for k, ts := range r.attempts {
			live := ts[:0]
			for _, t := range ts {
				if now.Sub(t) < r.window {
					live = append(live, t)
				}
			}
			if len(live) == 0 {
				delete(r.attempts, k)
				continue
			}
			r.attempts[k] = live
		}
	}
	if len(kept) >= r.max {
		return false
	}
	r.attempts[key] = append(kept, now)
	return true
}
