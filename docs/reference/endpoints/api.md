# HTTP management API endpoint

Module 'api' is a listener that exposes a JSON HTTP API for management of
credentials, IMAP accounts, mailboxes and messages, using the module instances
referenced in the block.

Unlike the maddyctl subcommands, which re-parse the configuration file and open
the databases in a second process, this endpoint talks to the running server's
own module instances in-process.

## Configuration

Multiple listen addresses can be given. TLS endpoints (`tls://`) are not
supported and are rejected when the configuration is parsed.

```
api tcp://127.0.0.1:8081 {
    creds &local_authdb
    storage &local_mailboxes
    admins postmaster@example.org
    secret ...
    login_rate_limit 10
    login_rate_window 5m
    max_msg_size 32M
    debug no
}
```

## Configuration directives

### creds _module-reference_
**Required.**

Credentials module used to verify logins and to create, change and remove
accounts. Must implement the `PlainUserDB` role, e.g. `&local_authdb`, or an
inline module block.

---

### storage _module-reference_
**Required.**

IMAP storage module used for all account, mailbox and message operations. Must
implement the `Storage` role. Operations that manage accounts
(user list/create/delete) additionally require `ManageableStorage`; against a
backend that does not implement it, listing and deleting users answer `501`
while creating a user answers `500`, with its credentials half rolled back and
listed in `partial`.

---

### admins _addresses..._
**Required.**

Addresses allowed to log in. An administrator is a mail account like any other:
login checks the password against `creds` and only then matches the address
against this list, so listing an address here makes that mailbox's password an
administrator password. Addresses are normalized (`auto` normalization, as for
`auth_map_normalize`) before comparison.

---

### secret _string_
**Required.**

Key used to sign bearer tokens (HMAC-SHA256). At least 16 bytes, e.g. generated
by `openssl rand -hex 32`. Changing it invalidates every issued token, which is
the only way to revoke one.

---

### login_rate_limit _integer_
Default: `10`

Number of `POST /api/v1/login` attempts (successful or not) allowed per peer
address within `login_rate_window`. Must be positive.

---

### login_rate_window _duration_
Default: `5m`

Sliding window for `login_rate_limit`. Must be positive. It is also the value
reported in the `Retry-After` header of a `429` answer.

---

### max_msg_size _size_
Default: `32M`

Size limit used by the message routes: the accepted size on append and the
served size on a message fetch. See "Limits" below.

---

### debug _boolean_
Default: global directive value

Enable verbose logging.

## Authentication

`POST /api/v1/login` with `{"username":..., "password":...}` (request body
limited to 8 KiB) verifies the password against `creds` and, for an address
listed in `admins`, issues

```
{"token":"<base64url payload>.<hex hmac>","expires_at":"...","username":"..."}
```

Tokens are valid for 12 hours. Every route except `GET /api/v1/health` requires
`Authorization: Bearer <token>`. A missing, malformed or expired token is `401`,
a valid token for an address that is no longer in `admins` is `403`.

A login rate limit is applied to `/api/v1/login` only; every other route is
gated by the token alone.

## Routes

All paths are below `/api/v1`. Bodies are JSON, UTF-8, except the raw message
fetch. Path parameters must be percent-encoded: a mailbox name containing `/` is
addressable as a single path segment only (`mailboxes/Foo%2FBar`), the raw-slash
form of the same path matches no route and is a `404`. The `{user}` address is
normalized as for login; mailbox names are not normalized, because the storage
backend compares them case-sensitively. The hierarchy separator is `.`, so
`a.b` is a child of `a`.

| Route | Request body | Response |
| --- | --- | --- |
| `POST /api/v1/login` | `{username, password}` | `200` `{token, expires_at, username}` |
| `GET /api/v1/health` | – | `200` `{version, start_time}` |
| `GET /api/v1/users` | – | `200` `{users:[{username, creds, storage, appendlimit}]}` |
| `POST /api/v1/users` | `{username, password, storage?, appendlimit?}` | `201` `{username, creds, storage}` |
| `DELETE /api/v1/users/{user}` | – | `200` `{deleted:["creds","storage"]}` |
| `PUT /api/v1/users/{user}/password` | `{password}` | `200` `{username}` |
| `PUT /api/v1/users/{user}/appendlimit` | `{value: 1048576}` / `{value: null}` | `200` `{username, appendlimit}` |
| `GET /api/v1/users/{user}/mailboxes` | – | `200` `{mailboxes:[{name, special_use?}]}` |
| `POST /api/v1/users/{user}/mailboxes` | `{name, special_use?}` | `201` `{name}` |
| `PUT /api/v1/users/{user}/mailboxes/{mbox}` | `{new_name}` | `200` `{name:"<new name>", moved}` |
| `DELETE /api/v1/users/{user}/mailboxes/{mbox}` | – | `200` `{deleted:"<mbox>", messages, left_behind}` |
| `GET /api/v1/users/{user}/mailboxes/{mbox}/messages` | – | `200` `{messages:[...]}` |
| `POST /api/v1/users/{user}/mailboxes/{mbox}/messages` | `{raw, flags?, date?}` | `201` `{mailbox, uid}` |
| `GET /api/v1/users/{user}/mailboxes/{mbox}/messages/{uid}` | – | `200` raw RFC 822 body, `application/rfc822+text` |
| `DELETE /api/v1/users/{user}/mailboxes/{mbox}/messages/{uid}` | – | `200` `{uid, deleted:<count>}` |

An error answer is `{"error":"..."}`, carrying `detail` with the underlying
module error text when the failure came from a module (a login, token, rate
limit or request-parsing failure has no module error and therefore no `detail`),
plus the route-specific fields listed above where they apply. The codes are:
`400` malformed request; `401` bad login credentials,
or a missing/malformed/expired token; `403` authenticated but not an
administrator; `404` no such user/mailbox/message; `409` already exists; `413`
over a size limit; `429` login rate limit exceeded (with `Retry-After`); `500` a
store call failed; `501` this storage backend cannot perform the operation.
An unregistered path is
`404 {"error":"no such endpoint","detail":"<path>"}`; a registered path used
with a method it does not declare is answered by the router with `405`, an
`Allow` header and a plain-text body, not the JSON envelope.

Three answers use the key `deleted` with three different meanings: a string (the
deleted mailbox name) in mailbox delete, an array of store names (`"creds"`,
`"storage"`) in user delete, and a message count in message delete. Rename's
`name` is the new name.

## Users and credentials

`GET /api/v1/users` is the union of the credentials list and the IMAP account
list; `creds` and `storage` say which half each address exists in.
`appendlimit` is the account's per-account APPENDLIMIT; it is `null` for an
address with no IMAP account, for an account with no limit, for a backend
without per-account APPENDLIMIT, and for an account whose state the endpoint
could not read (that failure is logged, not reported).

`POST /api/v1/users` requires a full address and a password of at least 8
characters, and answers `409` if the address exists in either store. With
`storage` true it creates the credential, then the IMAP account, then the five
special-use folders (`Sent`, `Trash`, `Junk`, `Drafts`, `Archive`), then applies
`appendlimit`. There is no transaction between the two stores, so a failure at
any step after the first rolls back both halves and answers

```
500 {"error":"failed to create IMAP account","detail":"...","partial":[]}
```

`partial` lists the stores that still hold a record, so the usual answer is the
empty array: the rollback removed both halves. A non-empty `partial` (e.g.
`["storage"]`) says the rollback itself failed and that store kept the record.
A storage backend without account management takes this same path, with
`detail` saying so and an empty `partial` once the credential is rolled back.

`DELETE /api/v1/users/{user}` always attempts both halves. A `200` answers
`{deleted:["creds","storage"]}`, listing only the stores it actually removed. If
either half fails the answer is an error carrying both `deleted` and `failed`,
and both arrays are always present, so `"deleted":[]` means "this half removed
nothing"; deleting an address that exists in only one store is therefore
reported as a failure (`deleted:["creds"], failed:["storage"]` for a
credentials-only address).

`PUT /api/v1/users/{user}/password` answers `404` for an address that has no
credential: the underlying credentials module inserts on a password change, so
without that check the endpoint would silently create a credential for an
address nobody has. A success answers `{username}` and nothing else.

`PUT /api/v1/users/{user}/appendlimit` requires the `value` field to be present;
`{"value":null}` clears the limit and an absent `value` is a `400`, so a
malformed request cannot clear a quota. A success answers
`{username, appendlimit}`, echoing the value it was given; the stored value is
not read back.

## Mailboxes

`special_use` is optional and must be one of the IMAP SPECIAL-USE attributes
(`\Sent`, `\Trash`, `\Junk`, `\Drafts`, `\Archive`); anything else is `501`, as
is any special-use request against a backend without SPECIAL-USE support.
`GET .../mailboxes` reports only `name` and, when set, `special_use`; the
hierarchy attributes (`\Marked`, `\HasChildren`) are not reported, so an
implicitly created parent folder is not distinguishable from a leaf.

`moved` (rename) and `left_behind` (delete) count the mailboxes under the
operated name, `messages` (delete) its message count; all three are read
*before* the operation runs, i.e. they are "what the backend is expected to
cascade", not a post-mortem. They undercount when the listing and the operation
disagree: a concurrent mutation, a parent name with a trailing separator, or a
child that differs from the parent prefix in case only (the counts use
case-sensitive prefix matching, the backend's cascade does not).

Renaming `INBOX` is allowed and is not a no-op: the backend renames the INBOX
row, cascades its children and re-creates an empty INBOX in its place, so the
rename succeeds and INBOX keeps existing. Deleting `INBOX` is refused with `400`.

## Messages

`GET .../messages` lists the whole mailbox by UID. Each entry carries `uid`,
`seq`, `flags`, `size`, `internal_date` (arrival) and, when the backend returned
an envelope, `date` (sent), `subject`, `from`, `to`, `cc`, `bcc`, `message_id`,
`in_reply_to`; an entry without an envelope omits those eight keys, and absent
means empty. `flags`, `from`, `to`, `cc`, `bcc` and `messages` are always
arrays, never `null`. A message with no `Date` header reports
`date` as `0001-01-01T00:00:00Z`, indistinguishable from a real year-1 date.

`POST .../messages` appends `raw` with the given `flags` and an optional RFC 3339
`date`. `uid` in the answer is the UID the message got; `0` means "appended, the
backend would not say". `0` is never a valid request UID: `GET`/`DELETE`
with `messages/0` is a `400`, because the backend resolves a zero UID bound to
the highest UID in the mailbox, i.e. the newest message.

`DELETE .../messages/{uid}` is destructive in the IMAP sense of EXPUNGE: the
backend marks the row, deletes it and drops the stored blob, and active IMAP
sessions for that mailbox are interrupted, as the `maddy imap-msgs remove`
subcommand warns. `DelMessages` reports no count, so `deleted` is the mailbox's
message-count delta: `{"deleted":0}` with a `200` conflates "nothing matched"
and "the count probe failed". In a mailbox that holds messages, deleting a UID
that is not present is not an error; in an empty mailbox the delete is a `404`,
because there the backend reports "no messages matched".

## Limits

`max_msg_size` bounds the message routes on the transport: append refuses a
request whose encoded JSON body is over `max_msg_size + 64KiB` (`413`), so a raw
message below the limit is refused when its JSON encoding is over it, and
`GET .../messages/{uid}` refuses to serve a body over the limit (`413`) rather
than answering a truncated message. The JSON bodies of the other routes are
limited to 1 MiB, the login body to 8 KiB.

A storage backend's own APPENDLIMIT/size limits apply on top of these and
produce `413` as well.

## Security

- The endpoint can read and destroy every mail on the server, so the bind
  address is the trust boundary: bind loopback (`tcp://127.0.0.1:8081`) or a
  `unix://` socket.
- TLS is not supported; `tls://` endpoints are rejected when the configuration
  is parsed.
- Do not put a reverse proxy in front of it. The login limiter counts from the
  peer address, so a proxy collapses every administrator into one bucket. The
  same applies to `unix://`: all local clients share one peer address, so the
  login limit is per-server, not per-client, there.
- `admins` is mandatory and fail-closed: an empty list is a configuration error,
  because it is the only thing separating a mailbox owner from an administrator.
- `GET /api/v1/health` is unauthenticated. Its `version` is taken from Go's
  build info (`debug.ReadBuildInfo`), i.e. the module/VCS-derived string (a
  VCS-stamped source build reports e.g.
  `0.9.7-0.20261026191710-7931ac3fd885+dirty`, a `go install`-style build
  reports `go-build`), not from the version string `build.sh` injects into the
  binary. Use it as a liveness probe, not as a version probe.
- The `api` sublogger logs every login, successful and failed, and every
  mutating action that succeeded; of the failing ones it logs the storage-layer
  failures (mailbox, message, password, APPENDLIMIT, incomplete user delete) and
  a user create that left state behind. A user create that fails and rolls back
  cleanly, and the create whose existence lookup fails, are not logged; the
  response is the only record of them.

## Reload

The listening socket is shared with the reloaded instance, so the port keeps
accepting through a `SIGUSR2` reload. The old instance closes idle keep-alive
connections and drains in-flight requests for up to 5 seconds; past that
deadline the reload proceeds while a request is still writing, and the old
instance then cuts it. A client that pools connections must therefore tolerate
one closed connection per reload (`net/http` retries a replayable request on a
reused connection, which is why a Go client does not notice). Tokens are
stateless, so they keep working across a reload and across a restart.

## Testing

`cd tests && ./run.sh -run TestAPIAccountManagement` skips the endpoint's
integration test: the binary that `run.sh` builds uses the cgo SQLite driver,
and the test guards itself to the transpiled driver. This invocation runs it:

```
cd tests
go build -o maddy.cover -race -tags 'modernc debugflags' github.com/foxcpp/maddy/cmd/maddy
go test -count=1 -race -tags 'integration modernc' -integration.executable ./maddy.cover -run TestAPIAccountManagement
```
