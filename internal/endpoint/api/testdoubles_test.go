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
	"bytes"
	"errors"
	"io"
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
	// gets counts GetIMAPAcct hand-outs; tests compare it against the
	// logouts the handed-out accounts receive, so a leaked account object
	// fails the suite.
	gets int
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{accts: map[string]*fakeUser{}}
}

func (f *fakeStorage) GetOrCreateIMAPAcct(username string) (imapbackend.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
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
	f.gets++
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

type fakeMsg struct {
	uid   uint32
	flags []string
	date  time.Time
	body  []byte
	env   imap.Envelope
}

// fakeUser implements imapbackend.User with an in-memory mailbox set; the
// later handler tests (messages, mailboxes, limits) drive it directly.
type fakeUser struct {
	name    string
	mu      sync.Mutex
	mboxes  map[string][]fakeMsg
	special map[string]string
	limit   *uint32
	// logouts counts Logout calls so a handler that borrows an account and
	// forgets to release it fails the suite (see fakeStorage.gets).
	logouts int
}

var (
	_ imapbackend.User    = (*fakeUser)(nil)
	_ imapbackend.Mailbox = (*fakeMailbox)(nil)
)

// newFakeUser seeds INBOX the way go-imap-sql does on CreateUser.
func newFakeUser(name string) *fakeUser {
	return &fakeUser{
		name:    name,
		mboxes:  map[string][]fakeMsg{"INBOX": nil},
		special: map[string]string{},
	}
}

func (u *fakeUser) Username() string { return u.name }

func (u *fakeUser) Status(mbox string, items []imap.StatusItem) (*imap.MailboxStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	msgs, ok := u.mboxes[mbox]
	if !ok {
		return nil, imapbackend.ErrNoSuchMailbox
	}
	return &imap.MailboxStatus{Name: mbox, Messages: uint32(len(msgs))}, nil
}

func (u *fakeUser) SetSubscribed(mbox string, subscribed bool) error { return nil }

func (u *fakeUser) ListMailboxes(subscribed bool) ([]imap.MailboxInfo, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	res := make([]imap.MailboxInfo, 0, len(u.mboxes))
	names := make([]string, 0, len(u.mboxes))
	for name := range u.mboxes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		attrs := []string{}
		if sp, ok := u.special[name]; ok {
			attrs = append(attrs, sp)
		}
		res = append(res, imap.MailboxInfo{Name: name, Attributes: attrs})
	}
	return res, nil
}

func (u *fakeUser) GetMailbox(name string, readOnly bool, conn imapbackend.Conn) (*imap.MailboxStatus, imapbackend.Mailbox, error) {
	u.mu.Lock()
	_, ok := u.mboxes[name]
	u.mu.Unlock()
	if !ok {
		return nil, nil, imapbackend.ErrNoSuchMailbox
	}
	return &imap.MailboxStatus{Name: name}, &fakeMailbox{user: u, name: name}, nil
}

func (u *fakeUser) CreateMailbox(name string) error { return u.createMailbox(name, "") }

func (u *fakeUser) CreateMailboxSpecial(name, specialUseAttr string) error {
	return u.createMailbox(name, specialUseAttr)
}

func (u *fakeUser) createMailbox(name, special string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, ok := u.mboxes[name]; ok {
		return imapbackend.ErrMailboxAlreadyExists
	}
	u.mboxes[name] = nil
	if special != "" {
		u.special[name] = special
	}
	return nil
}

func (u *fakeUser) DeleteMailbox(name string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, ok := u.mboxes[name]; !ok {
		return imapbackend.ErrNoSuchMailbox
	}
	delete(u.mboxes, name)
	delete(u.special, name)
	return nil
}

func (u *fakeUser) RenameMailbox(existingName, newName string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	msgs, ok := u.mboxes[existingName]
	if !ok {
		return imapbackend.ErrNoSuchMailbox
	}
	if _, ok := u.mboxes[newName]; ok {
		return imapbackend.ErrMailboxAlreadyExists
	}
	u.mboxes[newName] = msgs
	delete(u.mboxes, existingName)
	return nil
}

func (u *fakeUser) Logout() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.logouts++
	return nil
}

func (u *fakeUser) CreateMessage(mbox string, flags []string, date time.Time, body imap.Literal, selected imapbackend.Mailbox) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	msgs, ok := u.mboxes[mbox]
	if !ok {
		return imapbackend.ErrNoSuchMailbox
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	uid := uint32(1)
	for _, m := range msgs {
		if m.uid >= uid {
			uid = m.uid + 1
		}
	}
	u.mboxes[mbox] = append(msgs, fakeMsg{uid: uid, flags: flags, date: date, body: b})
	return nil
}

func (u *fakeUser) CreateMessageLimit() *uint32 { return u.limit }

func (u *fakeUser) SetMessageLimit(v *uint32) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.limit = v
	return nil
}

type fakeMailbox struct {
	user *fakeUser
	name string
}

func (m *fakeMailbox) Name() string              { return m.name }
func (m *fakeMailbox) Close() error              { return nil }
func (m *fakeMailbox) Poll(expunge bool) error   { return nil }
func (m *fakeMailbox) Expunge() error            { return nil }
func (m *fakeMailbox) Idle(done <-chan struct{}) {}

func (m *fakeMailbox) Info() (*imap.MailboxInfo, error) {
	m.user.mu.Lock()
	defer m.user.mu.Unlock()
	attrs := []string{}
	if sp, ok := m.user.special[m.name]; ok {
		attrs = append(attrs, sp)
	}
	return &imap.MailboxInfo{Name: m.name, Attributes: attrs}, nil
}

func (m *fakeMailbox) ListMessages(uid bool, set *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)
	m.user.mu.Lock()
	defer m.user.mu.Unlock()
	for _, msg := range m.user.mboxes[m.name] {
		if !set.Contains(msg.uid) {
			continue
		}
		out := &imap.Message{SeqNum: msg.uid, Uid: msg.uid,
			Flags: msg.flags, InternalDate: msg.date, Size: uint32(len(msg.body))}
		for _, item := range items {
			switch item {
			case imap.FetchEnvelope:
				out.Envelope = &msg.env
			case imap.FetchRFC822:
				out.Body = map[*imap.BodySectionName]imap.Literal{
					&imap.BodySectionName{}: literal{bytes.NewReader(msg.body)},
				}
			}
		}
		ch <- out
	}
	return nil
}

func (m *fakeMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	return nil, nil
}

func (m *fakeMailbox) UpdateMessagesFlags(uid bool, set *imap.SeqSet, operation imap.FlagsOp, silent bool, flags []string) error {
	return nil
}

func (m *fakeMailbox) CopyMessages(uid bool, set *imap.SeqSet, dest string) error { return nil }
