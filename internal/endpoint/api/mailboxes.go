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
	"strings"

	imap "github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
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
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	var req struct {
		Name       string `json:"name"`
		SpecialUse string `json:"special_use"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body", err.Error(), nil)
		return
	}
	if req.Name == "" {
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
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	name := r.PathValue("mbox")
	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	// go-imap-sql refuses this too, with a bare errors.New (user.go:228), which
	// would reach the client as a 500 for what is a refusal.
	if strings.EqualFold(name, "INBOX") {
		e.log.Msg("mailbox delete refused", "user", username, "mailbox", name,
			"admin", ctxAdminUser(r.Context()))
		writeErr(w, http.StatusBadRequest, "INBOX cannot be deleted", "", nil)
		return
	}

	// Deleting a mailbox destroys its contents and the CLI says so before it
	// asks (internal/cli/ctl/imap.go:485-516 prints the message count and prompts;
	// -y skips). A token API has no prompt to port, so it reports the size it is
	// about to destroy, in the response and in the log the operator audits.
	var messages uint32
	if st, err := u.Status(name, []imap.StatusItem{imap.StatusMessages}); err == nil {
		messages = st.Messages
	}
	// left_behind exists to warn about stranded children, so a failed
	// enumeration must not pass as the reassuring zero: log it and report 0
	// with the failure on the record.
	leftBehind, chErr := countChildren(u, name)
	if chErr != nil {
		e.log.Msg("mailbox children count failed", "user", username, "mailbox", name,
			"admin", ctxAdminUser(r.Context()), "error", chErr.Error())
	}

	if err := u.DeleteMailbox(name); err != nil {
		e.log.Msg("mailbox delete failed", "user", username, "mailbox", name,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to delete mailbox", err.Error(), nil)
		return
	}
	e.log.Msg("mailbox deleted", "user", username, "mailbox", name, "messages", messages,
		"left_behind", leftBehind, "admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": name, "messages": messages, "left_behind": leftBehind,
	})
}

func (e *Endpoint) handleMboxesRename(w http.ResponseWriter, r *http.Request) {
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	name := r.PathValue("mbox")
	var req struct {
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body", err.Error(), nil)
		return
	}
	if req.NewName == "" {
		writeErr(w, http.StatusBadRequest, "new_name is required", "", nil)
		return
	}
	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	// CreateMailbox maps a duplicate onto ErrMailboxAlreadyExists; RenameMailbox
	// does not — go-imap-sql's rename is a bare UPDATE against a
	// UNIQUE(uid, name) index (sql.go:140, sql.go:294), so a collision reaches
	// the client as a 500. Answer the same 409 the create path gives.
	boxes, err := u.ListMailboxes(false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list mailboxes", err.Error(), nil)
		return
	}
	moved := 0
	prefix := name + "."
	for _, b := range boxes {
		if strings.EqualFold(b.Name, req.NewName) {
			writeErr(w, http.StatusConflict, "mailbox already exists", req.NewName, nil)
			return
		}
		if strings.HasPrefix(b.Name, prefix) {
			moved++
		}
	}

	if err := u.RenameMailbox(name, req.NewName); err != nil {
		e.log.Msg("mailbox rename failed", "user", username, "mailbox", name,
			"new_name", req.NewName, "admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to rename mailbox", err.Error(), nil)
		return
	}
	// go-imap-sql cascades a rename to children (renameMboxChilds, user.go:276)
	// and re-creates an empty INBOX when you rename INBOX (user.go:288), so the
	// response must not imply one mailbox moved.
	e.log.Msg("mailbox renamed", "user", username, "mailbox", name, "new_name", req.NewName,
		"moved", moved, "admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name": req.NewName, "moved": moved,
	})
}

// countChildren counts the mailboxes under name (go-imap-sql's hierarchy
// separator is '.', MailboxPathSep in user.go:14, and DeleteMailbox deletes
// exactly one row, so a parent's children survive its deletion).
func countChildren(u imapbackend.User, name string) (int, error) {
	boxes, err := u.ListMailboxes(false)
	if err != nil {
		return 0, err
	}
	prefix := name + "."
	n := 0
	for _, b := range boxes {
		if strings.HasPrefix(b.Name, prefix) {
			n++
		}
	}
	return n, nil
}
