//go:build integration && cgo && !nosqlite3

/*
Maddy Mail Server - Composable all-in-one email server.
Copyright © 2019-2026 Max Mazurov <fox.cpp@disroot.org>, Maddy Mail Server contributors

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

package tests_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sqliteprovider "github.com/foxcpp/maddy/internal/sqlite"
	"github.com/foxcpp/maddy/tests"
)

func TestAPIAccountManagement(tt *testing.T) {
	if !sqliteprovider.IsTranspiled {
		tt.Skip("Test is unstable with original SQLite")
	}

	tt.Parallel()
	t := tests.NewT(tt)

	t.DNS(nil)
	t.Env("API_SECRET=0123456789abcdef0123456789abcdef")
	t.Port("api")

	cfg := `
		auth.pass_table local_authdb {
			table sql_table {
				driver sqlite3
				dsn credentials.db
				table_name passwords
			}
		}

		storage.imapsql local_mailboxes {
			driver sqlite3
			dsn imapsql.db
		}

		api tcp://127.0.0.1:{env:TEST_PORT_api} {
			creds &local_authdb
			storage &local_mailboxes
			admins postmaster@maddy.test
			secret {env:API_SECRET}
		}
	`
	t.Config(cfg)
	t.Run(1)
	defer t.Close()

	base := "http://127.0.0.1:" + strconv.Itoa(int(t.Port("api")))
	// This test does not pool connections across the reload.
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   60 * time.Second,
	}
	do := func(method, path, token string, payload []byte) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, bytes.NewReader(payload))
		require.NoError(t, err)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		return resp, body
	}

	// The admin account is a mail account like any other: create it the
	// supported way first, so login has a credential to check.
	t.MustRunCLI("creds", "create", "--cfg-block", "local_authdb",
		"--password", "hunter2hunter2", "postmaster@maddy.test")
	t.MustRunCLI("imap-acct", "create", "--cfg-block", "local_mailboxes",
		"postmaster@maddy.test")

	login, err := json.Marshal(map[string]string{
		"username": "postmaster@maddy.test", "password": "hunter2hunter2",
	})
	require.NoError(t, err)

	resp, body := do(http.MethodPost, "/api/v1/login", "", login)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tok struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(body, &tok))
	require.NotEmpty(t, tok.Token)

	resp, _ = do(http.MethodPost, "/api/v1/users", tok.Token,
		[]byte(`{"username":"new@maddy.test","password":"pppppppppp","storage":true}`))
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	out := t.MustRunCLI("imap-acct", "list", "--cfg-block", "local_mailboxes")
	require.Contains(t, out, "new@maddy.test")
	out = t.MustRunCLI("creds", "list", "--cfg-block", "local_authdb")
	require.Contains(t, out, "new@maddy.test")

	// A token issued before a reload must keep working, and the port must
	// survive: the endpoint is stateless, the secret comes from the config, and
	// netresource hands the listening socket to the new container.
	t.Config(cfg)

	resp, body = do(http.MethodPost, "/api/v1/login", "", login)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Contains(t, string(body), "postmaster@maddy.test")

	resp, body = do(http.MethodGet, "/api/v1/users", tok.Token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "new@maddy.test")

	// Messages, through the real go-imap-sql backend: this is the only place
	// the mb.(*imapsql.Mailbox).DelMessages assertion in imapsql.go runs at
	// all, and the reported count is what makes go-imap-sql's
	// delMarked-without-mboxId-filter visible to an operator.
	const raw = "From: sender@maddy.test\r\n" +
		"To: postmaster@maddy.test\r\n" +
		"Subject: api reload probe\r\n" +
		"Message-Id: <api-reload-probe@maddy.test>\r\n" +
		"\r\n" +
		"Hello from the management API.\r\n"
	msgsPath := "/api/v1/users/postmaster@maddy.test/mailboxes/INBOX/messages"
	resp, body = do(http.MethodPost, msgsPath, tok.Token,
		[]byte(`{"raw":`+strconv.Quote(raw)+`}`))
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var appended struct {
		Mailbox string `json:"mailbox"`
		UID     uint32 `json:"uid"`
	}
	require.NoError(t, json.Unmarshal(body, &appended))
	require.Equal(t, "INBOX", appended.Mailbox)
	require.NotZero(t, appended.UID, "the store must report the uid it assigned")

	msgPath := msgsPath + "/" + strconv.FormatUint(uint64(appended.UID), 10)
	resp, body = do(http.MethodGet, msgPath, tok.Token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Subject: api reload probe")
	require.Contains(t, string(body), "Hello from the management API.")

	resp, body = do(http.MethodDelete, msgPath, tok.Token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var deleted struct {
		UID     uint32 `json:"uid"`
		Deleted uint32 `json:"deleted"`
	}
	require.NoError(t, json.Unmarshal(body, &deleted))
	require.Equal(t, appended.UID, deleted.UID)
	require.Equal(t, uint32(1), deleted.Deleted)

	// The mailbox is empty afterwards, listed through the endpoint's own read
	// path: the same module instance, the same database, the code under review
	// (the CLI subcommand it replaces is not part of this surface).
	resp, body = do(http.MethodGet, msgsPath, tok.Token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{"messages":[],"truncated":false}`, string(body))

	// Deleting through the API removes both halves.
	resp, _ = do(http.MethodDelete, "/api/v1/users/new@maddy.test", tok.Token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out = t.MustRunCLI("imap-acct", "list", "--cfg-block", "local_mailboxes")
	require.NotContains(t, out, "new@maddy.test")
	out = t.MustRunCLI("creds", "list", "--cfg-block", "local_authdb")
	require.NotContains(t, out, "new@maddy.test")
}
