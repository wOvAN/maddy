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
	"io"
	"net/http"
)

func (e *Endpoint) handleMboxesList(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("user")
	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	boxes, err := u.ListMailboxes(false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list mailboxes", err.Error(), nil)
		return
	}
	res := make([]map[string]string, 0, len(boxes))
	for _, b := range boxes {
		entry := map[string]string{"name": b.Name}
		for _, attr := range b.Attributes {
			if isSpecialUse(attr) {
				entry["special_use"] = attr
			}
		}
		res = append(res, entry)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"mailboxes": res})
}

func (e *Endpoint) handleMboxesCreate(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("user")
	var req struct {
		Name       string `json:"name"`
		SpecialUse string `json:"special_use"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required", "", nil)
		return
	}
	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	if req.SpecialUse != "" {
		app, ok := u.(applier)
		if !ok {
			writeErr(w, http.StatusNotImplemented, "storage backend does not support SPECIAL-USE mailboxes", "", nil)
			return
		}
		err = app.CreateMailboxSpecial(req.Name, req.SpecialUse)
	} else {
		err = u.CreateMailbox(req.Name)
	}
	if err != nil {
		e.log.Msg("mailbox create failed", "user", username, "mailbox", req.Name,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to create mailbox", err.Error(), nil)
		return
	}
	e.log.Msg("mailbox created", "user", username, "mailbox", req.Name,
		"admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusCreated, map[string]interface{}{"name": req.Name})
}

func (e *Endpoint) handleMboxesDelete(w http.ResponseWriter, r *http.Request) {
	username, name := r.PathValue("user"), r.PathValue("mbox")
	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	if err := u.DeleteMailbox(name); err != nil {
		e.log.Msg("mailbox delete failed", "user", username, "mailbox", name,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to delete mailbox", err.Error(), nil)
		return
	}
	e.log.Msg("mailbox deleted", "user", username, "mailbox", name,
		"admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": name})
}

func (e *Endpoint) handleMboxesRename(w http.ResponseWriter, r *http.Request) {
	username, name := r.PathValue("user"), r.PathValue("mbox")
	var req struct {
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.NewName == "" {
		writeErr(w, http.StatusBadRequest, "new_name is required", "", nil)
		return
	}
	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	if err := u.RenameMailbox(name, req.NewName); err != nil {
		e.log.Msg("mailbox rename failed", "user", username, "mailbox", name,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to rename mailbox", err.Error(), nil)
		return
	}
	e.log.Msg("mailbox renamed", "user", username, "mailbox", name, "to", req.NewName,
		"admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusOK, map[string]interface{}{"name": req.NewName})
}
