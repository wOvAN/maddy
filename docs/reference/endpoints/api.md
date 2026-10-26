name: api endpoint
permalink: /reference/endpoints/api/
description: "HTTP management API endpoint"
---

## Description

Exposes a JSON HTTP API for administration of IMAP accounts, credentials,
mailboxes and messages, using the module instances referenced in the block.

## Configuration

`api tcp://127.0.0.1:8081 { creds &local_authdb; storage &local_mailboxes; admins postmaster@example.org; secret ... }`

## Directives

`creds <auth>`
: Credentials module, e.g. `creds &local_authdb` or an inline
  `creds password_hash { ... }` block. The module must implement the
  `PlainUserDB` role. Required.

`storage <storage>`
: Storage module, e.g. `storage &local_mailboxes`. The module must
  implement the `Storage` role. Required.

`admins <address>...`
: List of administrator addresses. Login is only possible for the users
  listed here. Required.

`secret <string>`
: Shared secret used to sign API sessions. Must be at least 16 bytes.
  Required.

`login_rate_limit <number>`
: Maximum number of failed login attempts per client within
  `login_rate_window`. Default is `10`.

`login_rate_window <duration>`
: Time window for `login_rate_limit`. Default is `5m`.

`max_msg_size <size>`
: Maximum size of a message that can be fetched through the API.
  Default is `32m`.

`debug`
: Enables debug logging for the module.
