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
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	imap "github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
	"github.com/foxcpp/maddy/framework/address"
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

// createAcctFolders mirrors what "maddy imap-acct create" does: INBOX comes
// from CreateIMAPAcct, then the five special-use folders are created, falling
// back to a plain CREATE on a backend without SPECIAL-USE support
// (internal/cli/ctl/imapacct.go:239-260).
func (e *Endpoint) createAcctFolders(u imapbackend.User) error {
	app, _ := u.(applier)
	for _, box := range [][2]string{
		{"Sent", imap.SentAttr}, {"Trash", imap.TrashAttr}, {"Junk", imap.JunkAttr},
		{"Drafts", imap.DraftsAttr}, {"Archive", imap.ArchiveAttr},
	} {
		var err error
		if app != nil {
			err = app.CreateMailboxSpecial(box[0], box[1])
		} else {
			err = u.CreateMailbox(box[0])
		}
		if err != nil && !errors.Is(err, imapbackend.ErrMailboxAlreadyExists) {
			return err
		}
	}
	return nil
}

func (e *Endpoint) handleUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string  `json:"username"`
		Password    string  `json:"password"`
		Storage     *bool   `json:"storage"`
		Appendlimit *uint32 `json:"appendlimit"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body", err.Error(), nil)
		return
	}
	if !address.Valid(req.Username) || len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "username must be an address and password at least 8 characters", "", nil)
		return
	}
	// creds and storage store exactly the string they are handed (address
	// normalization is configured on the imap/creds *consumers*, not inside
	// these modules), so an admin posting "Dave@Example.org" would create an
	// account that IMAP login, which normalizes before lookup, can never
	// reach. Persist the normalized spelling.
	username, err := e.normalize(req.Username)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	// address.Valid deliberately accepts a bare localpart ("postmaster") and the
	// username profile normalizes it to "postmaster@", a key shape the CLI never
	// produces (pass_table stores the raw name). One spelling per account, so
	// the API requires a full address.
	if _, _, err := address.Split(username); err != nil {
		writeErr(w, http.StatusBadRequest, "username must be a full address", err.Error(), nil)
		return
	}

	// appendlimit is a property of the IMAP account; the creds-only path below
	// never reads it, so a client that sends it there gets a 201, no limit and
	// no signal. Refuse the combination instead of dropping the field.
	if req.Appendlimit != nil && (req.Storage == nil || !*req.Storage) {
		writeErr(w, http.StatusBadRequest, "appendlimit requires storage:true", "", nil)
		return
	}

	if exists, err := e.userExists(username); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to look up user", err.Error(), nil)
		return
	} else if exists {
		writeErr(w, http.StatusConflict, "user already exists", "", nil)
		return
	}

	if err := e.creds.CreateUser(username, req.Password); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create credentials", err.Error(), nil)
		return
	}

	if req.Storage == nil || !*req.Storage {
		e.log.Msg("user created", "user", username, "storage", false,
			"admin", ctxAdminUser(r.Context()))
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"username": username, "creds": true, "storage": false,
		})
		return
	}

	ms, err := e.manageableStorage()
	acctCreated := false
	if err == nil {
		err = ms.CreateIMAPAcct(username)
		if err == nil {
			acctCreated = true
			var u imapbackend.User
			u, err = e.store.GetIMAPAcct(username)
			if err == nil {
				defer func() { _ = u.Logout() }()
				err = e.createAcctFolders(u)
				if err == nil && req.Appendlimit != nil {
					if lu, ok := u.(limitUser); ok {
						err = lu.SetMessageLimit(req.Appendlimit)
					} else {
						err = errors.New("storage backend does not support per-account APPENDLIMIT")
					}
				}
			}
		}
	}
	if err == nil {
		e.log.Msg("user created", "user", username, "storage", true,
			"admin", ctxAdminUser(r.Context()))
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"username": username, "creds": true, "storage": true,
		})
		return
	}

	// There is no transaction between the credentials store and the storage
	// store; the CLI has the same split. Undo *both* halves — rolling back only
	// the credential strands a real mailbox (go-imap-sql's CreateUser creates
	// INBOX and Archive), and that address is then uncreatable through the API
	// forever, because userExists reports the mailbox. Report exactly what
	// survived: partial lists the stores that still hold a record.
	partial := []string{}
	if e.creds.DeleteUser(username) != nil {
		partial = append(partial, "creds")
	}
	if acctCreated && ms != nil && ms.DeleteIMAPAcct(username) != nil {
		partial = append(partial, "storage")
	}
	if len(partial) != 0 {
		e.log.Msg("user create left state behind", "user", username,
			"admin", ctxAdminUser(r.Context()), "partial", strings.Join(partial, ","))
	}
	writeErr(w, http.StatusInternalServerError, "failed to create IMAP account", err.Error(),
		map[string]interface{}{"partial": partial})
}

func (e *Endpoint) handleUsersDelete(w http.ResponseWriter, r *http.Request) {
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	ms, msErr := e.manageableStorage()

	// Initialized, not nil: a delete that removed only storage must serialize
	// "deleted":[], not "deleted":null, because the client branches on membership.
	deleted, failed := []string{}, []string{}

	credsErr := e.creds.DeleteUser(username)
	if credsErr == nil {
		deleted = append(deleted, "creds")
	} else {
		failed = append(failed, "creds")
	}

	if msErr != nil {
		// Joined, not dropped: the handler answers with
		// statusForStoreError(credsErr) and credsErr.Error(), so an unrecorded
		// error on this path is a nil dereference.
		failed = append(failed, "storage")
		credsErr = errors.Join(credsErr, msErr)
	} else if err := ms.DeleteIMAPAcct(username); err != nil {
		failed = append(failed, "storage")
		credsErr = errors.Join(credsErr, err)
	} else {
		deleted = append(deleted, "storage")
	}

	if len(failed) != 0 {
		e.log.Msg("user delete incomplete", "user", username,
			"admin", ctxAdminUser(r.Context()), "failed", strings.Join(failed, ","))
		writeErr(w, statusForStoreError(credsErr), "failed to delete user", credsErr.Error(),
			map[string]interface{}{"deleted": deleted, "failed": failed})
		return
	}
	e.log.Msg("user deleted", "user", username, "admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": deleted})
}

func (e *Endpoint) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body", err.Error(), nil)
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters", "", nil)
		return
	}
	// pass_table.SetUserPassword has no existence check: it hashes and calls
	// table.SetKey, whose first statement is the table's INSERT. Setting a
	// password for an address nobody has therefore *creates* the credential, so
	// the endpoint must look the account up itself or it answers 200 for a
	// mailbox that does not exist and logs a change that never happened.
	exists, err := e.credsExist(username)
	if err != nil {
		e.log.Msg("user password change failed", "user", username,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, http.StatusInternalServerError, "failed to look up user", err.Error(), nil)
		return
	}
	if !exists {
		e.log.Msg("user password change refused", "user", username, "admin", ctxAdminUser(r.Context()))
		writeErr(w, http.StatusNotFound, "no such user", "", nil)
		return
	}
	if err := e.creds.SetUserPassword(username, req.Password); err != nil {
		e.log.Msg("user password change failed", "user", username,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, statusForStoreError(err), "failed to set password", err.Error(), nil)
		return
	}
	e.log.Msg("user password changed", "user", username, "admin", ctxAdminUser(r.Context()))
	writeJSON(w, http.StatusOK, map[string]interface{}{"username": username})
}

func (e *Endpoint) handleUserAppendlimit(w http.ResponseWriter, r *http.Request) {
	username, err := e.normalize(r.PathValue("user"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed username", err.Error(), nil)
		return
	}
	// A *uint32 cannot tell {"value":null} (clear, per spec) from {} (absent),
	// and an absent field must not silently clear a quota: decode the field
	// presence separately, the way the CLI's --value flag does. RawMessage (not
	// *RawMessage) on purpose: the json package nulls pointer fields, which
	// would make null indistinguishable from absent.
	var req struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body", err.Error(), nil)
		return
	}
	if req.Value == nil {
		writeErr(w, http.StatusBadRequest, "value is required (null clears the limit)", "", nil)
		return
	}
	// nil is the "no limit" spelling (the CLI passes it to clear), so it is
	// allocated only for a real number.
	var value *uint32
	if string(req.Value) != "null" {
		v := new(uint32)
		if err := json.Unmarshal(req.Value, v); err != nil {
			writeErr(w, http.StatusBadRequest, "value must be a number or null", err.Error(), nil)
			return
		}
		value = v
	}

	u, code, err := e.userFor(username)
	if err != nil {
		writeErr(w, code, "failed to get IMAP account", err.Error(), nil)
		return
	}
	defer func() { _ = u.Logout() }()

	lu, ok := u.(limitUser)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "storage backend does not support per-account APPENDLIMIT", "", nil)
		return
	}
	if err := lu.SetMessageLimit(value); err != nil {
		e.log.Msg("user appendlimit change failed", "user", username,
			"admin", ctxAdminUser(r.Context()), "error", err.Error())
		writeErr(w, http.StatusInternalServerError, "failed to set APPENDLIMIT", err.Error(), nil)
		return
	}
	e.log.Msg("user appendlimit changed", "user", username, "admin", ctxAdminUser(r.Context()),
		"value", value)
	writeJSON(w, http.StatusOK, map[string]interface{}{"username": username, "appendlimit": value})
}
