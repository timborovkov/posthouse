# Security policy

If you think you found a vulnerability in Posthouse, please report it privately.

Do **not** open a public issue for security reports. Do not include passwords,
tokens, cache keys, message bodies, calendar contents, or other private
provider data in GitHub issues, pull requests, or logs.

## How to report

1. Prefer [GitHub private vulnerability reporting](https://github.com/timborovkov/posthouse/security/advisories/new).
2. Or email **tim.borovkov@icloud.com** with a description of the issue and
   enough detail to reproduce it.

Please include the Posthouse version (`posthouse version`), the interface you
used (CLI, TUI, MCP, or Docker), and whether the report depends on a specific
provider. Redact secrets.

## What to expect

You should get an acknowledgement when the report is seen. Please give a
reasonable amount of time to investigate and fix before any public disclosure.

## Scope

In scope: credential handling, encrypted cache/state, prepared-operation
safety, TLS/STARTTLS behavior, MCP authentication, and anything that could
leak provider content or let a write happen without an explicit execute.

Out of scope for this policy: issues that only exist when a user pastes their
own secrets into a public ticket, or provider-side bugs in IMAP/SMTP/CalDAV
servers that Posthouse cannot mitigate.

Supported versions: the latest published `v0.2.x` tag and `main`.
