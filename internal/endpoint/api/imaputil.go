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
	"fmt"
	"io"
	"net/http"

	"github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
	mess "github.com/foxcpp/go-imap-mess"
	imapsql "github.com/foxcpp/go-imap-sql"
	"github.com/foxcpp/maddy/framework/log"
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
		errors.Is(err, module.ErrUnknownCredentials),
		// A uid set that matched nothing (go-imap-mess ResolveSeq,
		// mailbox.go:41,55); DelMessages propagates it, so "delete a uid
		// that is not there" is this arm.
		errors.Is(err, mess.ErrNoMessages):
		return http.StatusNotFound
	case errors.Is(err, imapbackend.ErrMailboxAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, errUnmanageable), errors.Is(err, errUnsupported),
		errors.Is(err, imapsql.ErrUnsupportedSpecialAttr):
		return http.StatusNotImplemented
	// APPENDLIMIT exceeded on append (go-imap-sql mailbox.go:368-381).
	case errors.Is(err, imapbackend.ErrTooBig):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}

// fetchMessages runs a message fetch to completion. The backend owns ch
// (go-imap backend/mailbox.go:29), so the consumer must not close it — and must
// not depend on the close alone: a producer that returns without closing would
// pin this handler goroutine forever, and EarlyStop's drain would never finish.
// The done channel makes the producer's return end the loop, and the recover is
// this repo's rule for any goroutine a module starts (api.go:255), paired there
// with a log line, so a recovered panic is logged, not just answered.
func fetchMessages(l *log.Logger, mb imapbackend.Mailbox, byUID bool, set *imap.SeqSet, items []imap.FetchItem) ([]*imap.Message, error) {
	ch := make(chan *imap.Message, 32)
	errCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				l.Error("message fetch panicked", fmt.Errorf("%v", p))
				errCh <- fmt.Errorf("message fetch panicked: %v", p)
			}
		}()
		errCh <- mb.ListMessages(byUID, set, items, ch)
	}()

	var msgs []*imap.Message
	for {
		select {
		case msg, ok := <-ch:
			if ok {
				msgs = append(msgs, msg)
				continue
			}
			return msgs, <-errCh // the producer closed ch
		case <-done:
			for { // producer returned; take what is buffered, never block on ch
				select {
				case msg, ok := <-ch:
					if !ok {
						return msgs, <-errCh
					}
					msgs = append(msgs, msg)
				default:
					return msgs, <-errCh
				}
			}
		}
	}
}

// drainBody concatenates the fetched body literals, reading at most
// max+1 bytes: a result longer than max is the truncation signal, so a
// truncated body never reaches the client as if it were complete. The read
// error is returned, not dropped: go-imap-sql hands out a live literal over
// the decompressed extStore blob (fetch.go:230-251), so the read can fail
// mid-body, and a failed read is a 500, not a 200 with a prefix.
func drainBody(msg *imap.Message, max int64) ([]byte, error) {
	var out []byte
	for _, lit := range msg.Body {
		b, err := io.ReadAll(io.LimitReader(lit, max+1))
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
	return out, nil
}
