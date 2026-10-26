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
	"net/http"
	"sort"
)

type userEntry struct {
	Username    string  `json:"username"`
	Creds       bool    `json:"creds"`
	Storage     bool    `json:"storage"`
	Appendlimit *uint32 `json:"appendlimit"`
}

func (e *Endpoint) handleUsersList(w http.ResponseWriter, r *http.Request) {
	credsList, err := e.creds.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list credentials", err.Error(), nil)
		return
	}
	ms, err := e.manageableStorage()
	if err != nil {
		writeErr(w, http.StatusNotImplemented, "storage management unavailable", err.Error(), nil)
		return
	}
	acctList, err := ms.ListIMAPAccts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list IMAP accounts", err.Error(), nil)
		return
	}

	// The two stores normalize addresses differently (precis case mapping in
	// pass_table, lower-casing in go-imap-sql), so the union is keyed by this
	// endpoint's own normalization. A name that fails to normalize keeps its
	// raw form: a list endpoint must not drop an account over it.
	key := func(raw string) string {
		n, err := e.normalize(raw)
		if err != nil {
			return raw
		}
		return n
	}

	set := make(map[string]*userEntry, len(credsList)+len(acctList))
	for _, u := range credsList {
		k := key(u)
		set[k] = &userEntry{Username: k, Creds: true}
	}
	for _, a := range acctList {
		k := key(a)
		entry, ok := set[k]
		if !ok {
			entry = &userEntry{Username: k}
			set[k] = entry
		}
		entry.Storage = true
	}

	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)

	res := make([]userEntry, 0, len(names))
	for _, n := range names {
		entry := set[n]
		if entry.Storage {
			u, _, err := e.userFor(n)
			if err != nil {
				// The row is still reported; only the appendlimit is missing.
				e.log.Msg("failed to read account state", "user", n, "error", err.Error())
			} else {
				if lu, ok := u.(limitUser); ok {
					entry.Appendlimit = lu.CreateMessageLimit()
				}
				_ = u.Logout()
			}
		}
		res = append(res, *entry)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"users": res})
}
