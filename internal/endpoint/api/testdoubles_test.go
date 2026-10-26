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
	"strings"
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
	// deleteErr is a DeleteUser-only fault knob so a test can strand a
	// credential on purpose; nil means DeleteUser behaves normally.
	deleteErr error
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
	// Faithful to pass_table.SetUserPassword: no existence check, and the
	// table's SetKey runs the INSERT first, so a missing user gets a
	// credential. The 404 for a nonexistent user is the handler's job.
	f.users[username] = password
	return nil
}

func (f *fakeCreds) DeleteUser(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.deleteErr != nil {
		return f.deleteErr
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
	// createErr fails only CreateIMAPAcct: the global err knob would also
	// break ListIMAPAccts, and the create handler's existence check lists
	// first, so a create-only fault is the only way to drive the
	// create-rollback path.
	createErr error
	// mboxErr is stamped onto every account CreateIMAPAcct builds, so a test
	// can make folder creation fail on a user the handler creates mid-request.
	mboxErr error
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

// acct hands a test the fakeUser behind an account without counting it as a
// store hand-out, so assertAccountBalance keeps measuring handler leaks only.
func (f *fakeStorage) acct(username string) *fakeUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accts[username]
}

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
	if f.createErr != nil {
		return f.createErr
	}
	if _, ok := f.accts[username]; ok {
		return errors.New("acct already exists")
	}
	u := newFakeUser(username)
	u.mboxErr = f.mboxErr
	f.accts[username] = u
	return nil
}

// storageOnly is a module.Storage without the ManageableStorage role, so a
// test can drive the "backend cannot manage accounts" path (errUnmanageable)
// over the same in-memory state as fakeStorage.
type storageOnly struct {
	f *fakeStorage
}

func (s *storageOnly) GetOrCreateIMAPAcct(username string) (imapbackend.User, error) {
	return s.f.GetOrCreateIMAPAcct(username)
}

func (s *storageOnly) GetIMAPAcct(username string) (imapbackend.User, error) {
	return s.f.GetIMAPAcct(username)
}

func (s *storageOnly) IMAPExtensions() []string { return s.f.IMAPExtensions() }

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
	// mboxErr makes CreateMailboxSpecial fail on purpose; nil by default.
	mboxErr error
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
	// go-imap-sql accepts only the five real SPECIAL-USE attributes and
	// answers anything else with ErrUnsupportedSpecialAttr (user.go:194-203);
	// mirror it so a bogus attribute cannot reach the store.
	if specialUseAttr != "" && !isSpecialUse(specialUseAttr) {
		return imapsql.ErrUnsupportedSpecialAttr
	}
	u.mu.Lock()
	mboxErr := u.mboxErr
	u.mu.Unlock()
	if mboxErr != nil {
		return mboxErr
	}
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

// DeleteMailbox deletes exactly one row, faithful to go-imap-sql's deleteMbox
// (sql.go:288, DELETE FROM mboxes WHERE uid = ? AND name = ?): the children of
// a deleted parent are not touched and survive with a dangling prefix.
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

// RenameMailbox mirrors go-imap-sql's RenameMailbox (user.go:291): the rename
// cascades to the children, whose names are rewritten from
// existingName+MailboxPathSep to newName+MailboxPathSep. The separator is "."
// (MailboxPathSep, user.go:14) — the "." in this double's names is the real
// hierarchy separator, so "A.B" is a child of "A".
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
	oldPrefix := existingName + "."
	newPrefix := newName + "."
	for name, m := range u.mboxes {
		if strings.HasPrefix(name, oldPrefix) {
			delete(u.mboxes, name)
			u.mboxes[newPrefix+name[len(oldPrefix):]] = m
		}
	}
	u.mboxes[newName] = msgs
	delete(u.mboxes, existingName)
	if sp, ok := u.special[existingName]; ok {
		delete(u.special, existingName)
		u.special[newName] = sp
	}
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

// plant adds a message with a known envelope to a mailbox, for tests that
// assert on listed envelopes or on the contents a delete is about to destroy.
func (u *fakeUser) plant(mbox string, env imap.Envelope, body string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	uid := uint32(1)
	for _, m := range u.mboxes[mbox] {
		if m.uid >= uid {
			uid = m.uid + 1
		}
	}
	u.mboxes[mbox] = append(u.mboxes[mbox], fakeMsg{uid: uid, env: env, body: []byte(body)})
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
