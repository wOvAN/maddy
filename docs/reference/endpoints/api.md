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

Implemented in the tasks below; keep this page in sync with `Configure`.
