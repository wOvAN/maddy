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
	"sort"
	"sync"
	"time"

	imap "github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
	imapsql "github.com/foxcpp/go-imap-sql"
	"github.com/foxcpp/maddy/framework/module"
)

type fakeCreds struct {
	mu    sync.Mutex
	users map[string]string
	err   error
}

func newFakeCreds() *fakeCreds {
	return &fakeCreds{users: map[string]string{}}
}

func (f *fakeCreds) AuthPlain(username, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if p, ok := f.users[username]; !ok || p != password {
		return module.ErrUnknownCredentials
	}
	return nil
}

func (f *fakeCreds) ListUsers() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	res := make([]string, 0, len(f.users))
	for u := range f.users {
		res = append(res, u)
	}
	sort.Strings(res)
	return res, nil
}

func (f *fakeCreds) CreateUser(username, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, ok := f.users[username]; ok {
		return errors.New("user already exists")
	}
	f.users[username] = password
	return nil
}

func (f *fakeCreds) SetUserPassword(username, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, ok := f.users[username]; !ok {
		return module.ErrUnknownCredentials
	}
	f.users[username] = password
	return nil
}

func (f *fakeCreds) DeleteUser(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, ok := f.users[username]; !ok {
		return module.ErrUnknownCredentials
	}
	delete(f.users, username)
	return nil
}

type fakeStorage struct {
	mu    sync.Mutex
	accts map[string]*fakeUser
	err   error
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{accts: map[string]*fakeUser{}}
}

func (f *fakeStorage) GetOrCreateIMAPAcct(username string) (imapbackend.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.accts[username]
	if !ok {
		u = newFakeUser(username)
		f.accts[username] = u
	}
	return u, nil
}

func (f *fakeStorage) GetIMAPAcct(username string) (imapbackend.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	u, ok := f.accts[username]
	if !ok {
		return nil, imapsql.ErrUserDoesntExists
	}
	return u, nil
}

func (f *fakeStorage) IMAPExtensions() []string { return nil }

func (f *fakeStorage) ListIMAPAccts() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	res := make([]string, 0, len(f.accts))
	for u := range f.accts {
		res = append(res, u)
	}
	sort.Strings(res)
	return res, nil
}

func (f *fakeStorage) CreateIMAPAcct(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, ok := f.accts[username]; ok {
		return errors.New("acct already exists")
	}
	f.accts[username] = newFakeUser(username)
	return nil
}

func (f *fakeStorage) DeleteIMAPAcct(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, ok := f.accts[username]; !ok {
		return imapsql.ErrUserDoesntExists
	}
	delete(f.accts, username)
	return nil
}

// fakeUser implements imapbackend.User with an empty mailbox set; handler
// tests only use it as an opaque account handle returned by fakeStorage.
type fakeUser struct {
	username string
}

var _ imapbackend.User = (*fakeUser)(nil)

func newFakeUser(username string) *fakeUser {
	return &fakeUser{username: username}
}

func (u *fakeUser) Username() string { return u.username }

func (u *fakeUser) Status(mbox string, items []imap.StatusItem) (*imap.MailboxStatus, error) {
	return nil, errors.New("not implemented")
}

func (u *fakeUser) SetSubscribed(mbox string, subscribed bool) error {
	return errors.New("not implemented")
}

func (u *fakeUser) CreateMessage(mbox string, flags []string, date time.Time, body imap.Literal, selectedMailbox imapbackend.Mailbox) error {
	return errors.New("not implemented")
}

func (u *fakeUser) ListMailboxes(subscribed bool) ([]imap.MailboxInfo, error) {
	return nil, nil
}

func (u *fakeUser) GetMailbox(name string, readOnly bool, conn imapbackend.Conn) (*imap.MailboxStatus, imapbackend.Mailbox, error) {
	return nil, nil, imapbackend.ErrNoSuchMailbox
}

func (u *fakeUser) CreateMailbox(name string) error {
	return errors.New("not implemented")
}

func (u *fakeUser) DeleteMailbox(name string) error {
	return errors.New("not implemented")
}

func (u *fakeUser) RenameMailbox(existingName, newName string) error {
	return errors.New("not implemented")
}

func (u *fakeUser) Logout() error { return nil }
