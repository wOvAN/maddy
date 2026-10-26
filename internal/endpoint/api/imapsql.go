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
	imap "github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
)

// deleter is the go-imap-sql Mailbox capability to delete messages; the CLI
// reaches it by asserting the concrete type on the selected mailbox
// (mbox.(*imapsql.Mailbox), internal/cli/ctl/imap.go:628). Asserting the
// method set is the same reachability test on every real mailbox, and it lets
// the test double speak it.
type deleter interface {
	DelMessages(uid bool, seq *imap.SeqSet) error
}

// deleteMessages mirrors "maddy imap-msgs remove". The CLI opens the mailbox
// read-only (GetMailbox's readOnly=true, imap.go:617 — the flag drives \Seen
// marking in ListMessages, go-imap-sql fetch.go:22, and Close on a management
// handle is a no-op); we open it read-write because the mutation is not a read,
// and the flag is harmless here precisely because DelMessages ignores it.
func (e *Endpoint) deleteMessages(u imapbackend.User, mbox string, uid uint32) error {
	_, mb, err := u.GetMailbox(mbox, false, nil)
	if err != nil {
		return err
	}
	defer func() { _ = mb.Close() }()

	d, ok := mb.(deleter)
	if !ok {
		return errUnsupported
	}
	return d.DelMessages(true, seqset(uid))
}
