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
	"net/http"

	"github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
	imapsql "github.com/foxcpp/go-imap-sql"
	"github.com/foxcpp/maddy/framework/module"
)

var errUnsupported = errors.New("storage backend does not support this operation")

// literal adapts a byte slice to the imap.Literal contract that
// imapbackend.User.CreateMessage expects (io.Reader plus Len).
type literal struct{ *bytes.Reader }

func (l literal) Len() int { return l.Reader.Len() }

func seqset(uid uint32) *imap.SeqSet {
	set := &imap.SeqSet{}
	set.AddNum(uid)
	return set
}

func addresses(list []*imap.Address) []string {
	res := make([]string, 0, len(list))
	for _, a := range list {
		res = append(res, a.Address())
	}
	return res
}

func isSpecialUse(attr string) bool {
	switch attr {
	case imap.SentAttr, imap.TrashAttr, imap.JunkAttr, imap.DraftsAttr, imap.ArchiveAttr:
		return true
	}
	return false
}

// statusForStoreError maps the sentinels produced by the credentials and
// storage modules onto HTTP statuses, so a client can tell "not there" from
// "this backend cannot do that" from "the database broke".
func statusForStoreError(err error) int {
	switch {
	case errors.Is(err, imapbackend.ErrNoSuchMailbox),
		errors.Is(err, imapsql.ErrUserDoesntExists),
		errors.Is(err, module.ErrUnknownCredentials):
		return http.StatusNotFound
	case errors.Is(err, imapbackend.ErrMailboxAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, errUnsupported):
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
