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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/foxcpp/maddy/framework/module"
	"golang.org/x/crypto/bcrypt"
)

type ctxKey int

const ctxAdmin ctxKey = iota

// serverVersion mirrors the root package's GetVersion without importing it:
// the root package blank-imports this one, so a direct import would be a
// cycle. build.sh binaries carry their version in an ldflags variable the
// root package owns, so they report "go-build" here.
// ponytail: health reports "go-build" for release binaries; move the version
// to a shared package (and build.sh's -X flag) if /health must name it.
func serverVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return "go-build"
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (e *Endpoint) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !e.limiter.Allow(remoteIP(r)) {
		e.log.Msg("login rate limit exceeded", "ip", remoteIP(r))
		w.Header().Set("Retry-After", strconv.Itoa(int(e.loginWindow.Seconds())))
		writeErr(w, http.StatusTooManyRequests, "too many login attempts", "", nil)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body", err.Error(), nil)
		return
	}

	err := e.creds.AuthPlain(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, module.ErrUnknownCredentials) {
			// pass_table answers "no such user" before it hashes anything, so
			// the two failure paths would differ by ~45ms of bcrypt and
			// /login would be a latency oracle for "does this address have a
			// credentials record". Burn the same cost on both.
			checkAgainstDummyHash(req.Password)
		}
		e.log.Msg("login failed", "user", req.Username, "ip", remoteIP(r))
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErr(w, http.StatusUnauthorized, "invalid username or password", "", nil)
		return
	}

	admin, err := e.normalize(req.Username)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	if !e.isAdmin(admin) {
		// The password was right and the address is not an administrator: the
		// 403 discloses that the account exists by design, because an
		// administrator is a mail account like any other.
		e.log.Msg("login by non-administrator", "user", admin, "ip", remoteIP(r))
		writeErr(w, http.StatusForbidden, "not an administrator", "", nil)
		return
	}

	exp := time.Now().Add(tokenTTL)
	token, err := e.makeToken(admin, exp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to issue token", err.Error(), nil)
		return
	}
	e.log.Msg("login", "user", admin, "ip", remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":      token,
		"expires_at": exp,
		"username":   admin,
	})
}

func (e *Endpoint) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const scheme = "Bearer "
		header := r.Header.Get("Authorization")
		// The auth-scheme token is case-insensitive (RFC 7235); only the
		// secret after it is not.
		if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, "missing bearer token", "", nil)
			return
		}
		user, err := e.verifyToken(header[len(scheme):])
		if err != nil {
			msg := "invalid token"
			if errors.Is(err, errExpired) {
				msg = "expired token"
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, msg, "", nil)
			return
		}
		// Store the normalized subject: every handler downstream uses it for
		// creds/storage lookups and for the audit trail, and the client
		// controls the spelling inside its own signed token.
		admin, err := e.normalize(user)
		if err != nil || !e.isAdmin(admin) {
			writeErr(w, http.StatusForbidden, "not an administrator", "", nil)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxAdmin, admin)))
	}
}

// checkAgainstDummyHash spends one bcrypt comparison on a password that cannot
// match, so that a missing account and a wrong password take the same time.
// golang.org/x/crypto is already a maddy dependency (auth.pass_table uses it).
var dummyHash = []byte("$2a$10$NV0t9gIecyMGEImWU2KBPugGnaxu1MdzP8QActmDCHU/OQTRJIffm")

func checkAgainstDummyHash(password string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}

func (e *Endpoint) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":    serverVersion(),
		"start_time": e.startTime,
	})
}
