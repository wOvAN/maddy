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
	"strings"
	"time"

	"github.com/foxcpp/maddy/framework/module"
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
		e.log.Println("login rate limit exceeded for", remoteIP(r))
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
	if errors.Is(err, module.ErrUnknownCredentials) || err != nil {
		// The credential check itself is what costs time; do not branch on the
		// error kind in a way that leaks whether the account exists.
		e.log.Println("failed login attempt for", req.Username, "from", remoteIP(r))
		writeErr(w, http.StatusUnauthorized, "invalid username or password", "", nil)
		return
	}

	if !e.isAdmin(req.Username) {
		e.log.Println("login by non-administrator", req.Username, "from", remoteIP(r))
		writeErr(w, http.StatusForbidden, "not an administrator", "", nil)
		return
	}

	exp := time.Now().Add(tokenTTL)
	token, err := e.makeToken(req.Username, exp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to issue token", err.Error(), nil)
		return
	}
	e.log.Println("login by", req.Username, "from", remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":      token,
		"expires_at": exp,
		"username":   req.Username,
	})
}

func (e *Endpoint) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeErr(w, http.StatusUnauthorized, "missing bearer token", "", nil)
			return
		}
		user, err := e.verifyToken(token)
		if err != nil {
			msg := "invalid token"
			if errors.Is(err, errExpired) {
				msg = "expired token"
			}
			writeErr(w, http.StatusUnauthorized, msg, "", nil)
			return
		}
		if !e.isAdmin(user) {
			writeErr(w, http.StatusForbidden, "not an administrator", "", nil)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxAdmin, user)))
	}
}

func (e *Endpoint) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":    serverVersion(),
		"start_time": e.startTime,
	})
}
