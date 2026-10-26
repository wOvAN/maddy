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
	imapbackend "github.com/emersion/go-imap/backend"
	imapsql "github.com/foxcpp/go-imap-sql"
)

// deleteMessages mirrors "maddy imap-msgs remove": deleting messages is a
// go-imap-sql specific capability, reached by the same type assertion on the
// selected mailbox that internal/cli/ctl/imap.go:625 uses.
func (e *Endpoint) deleteMessages(u imapbackend.User, mbox string, uid uint32) error {
	_, mb, err := u.GetMailbox(mbox, false, nil)
	if err != nil {
		return err
	}
	defer func() { _ = mb.Close() }()

	mbSQL, ok := mb.(*imapsql.Mailbox)
	if !ok {
		return errUnsupported
	}
	return mbSQL.DelMessages(true, seqset(uid))
}
