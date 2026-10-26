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

	set := make(map[string]*userEntry, len(credsList)+len(acctList))
	for _, u := range credsList {
		set[u] = &userEntry{Username: u, Creds: true}
	}
	for _, a := range acctList {
		entry, ok := set[a]
		if !ok {
			entry = &userEntry{Username: a}
			set[a] = entry
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
			if u, err := e.store.GetIMAPAcct(n); err == nil {
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
