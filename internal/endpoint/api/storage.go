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
	"errors"
	"net/http"

	imapbackend "github.com/emersion/go-imap/backend"
	"github.com/foxcpp/maddy/framework/address"
	"github.com/foxcpp/maddy/framework/module"
)

// The handlers below talk to these narrow interfaces instead of
// imapbackend.User/Mailbox directly. Two reasons: the go-imap backend
// interfaces are wide (a test double needs a dozen methods), and every
// capability maddy exposes is optional, expressed by a type assertion. Keeping
// all of those assertions in one file means a handler can say "this backend
// cannot do that" with one error kind.

type applier interface {
	CreateMailboxSpecial(name, specialUseAttr string) error
}

type limitUser interface {
	imapbackend.User
	CreateMessageLimit() *uint32
	SetMessageLimit(val *uint32) error
}

func (e *Endpoint) manageableStorage() (module.ManageableStorage, error) {
	ms, ok := e.store.(module.ManageableStorage)
	if !ok {
		return nil, errors.New("storage backend does not support accounts management")
	}
	return ms, nil
}

// userFor resolves the account a handler wants to work with. It normalizes
// the username (a malformed one is a 400, not a store lookup), maps a miss
// onto an HTTP status through statusForStoreError, and returns the account
// borrowed from the store: the caller must release it with Logout().
func (e *Endpoint) userFor(raw string) (imapbackend.User, int, error) {
	username, err := e.normalize(raw)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	u, err := e.store.GetIMAPAcct(username)
	if err != nil {
		return nil, statusForStoreError(err), err
	}
	return u, 0, nil
}

// userExists scans both stores for the account; PlainUserDB and
// ManageableStorage expose no lookup-by-name, and maddy is a
// small-installation server, so a linear scan of the two lists is the whole
// job.
func (e *Endpoint) userExists(username string) (bool, error) {
	users, err := e.creds.ListUsers()
	if err != nil {
		return false, err
	}
	ms, err := e.manageableStorage()
	if err == nil {
		if accts, err2 := ms.ListIMAPAccts(); err2 == nil {
			users = append(users, accts...)
		}
	}
	for _, u := range users {
		if address.Equal(u, username) {
			return true, nil
		}
	}
	return false, nil
}
