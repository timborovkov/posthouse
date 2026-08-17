# Posthouse v0.2 release-candidate handoff

Posthouse is a local-first Go CLI, MCP server, and full-screen terminal application for operating multiple generic mail and calendar connections through open protocols.

## Included

- Multi-account IMAP/SMTP mail, read-only ICS feeds, and mutable CalDAV calendars.
- Complete MIME reads, bounded previews and attachment access, text or HTML send, reply/forward/draft/folder actions, and configurable sent-copy handling.
- CalDAV discovery, collection selection, ETag-guarded CRUD, recurrence exceptions, embedded timezones, and portable invitations.
- Ten-minute encrypted prepared operations with exact previews, persisted cross-process claiming, idempotent replay, and uncertain-result handling.
- Optional write policy (default allow-all) to deny classes such as send, move, trash, draft, or calendar write on prepare and execute.
- MCP `full` / `readonly` profiles so agents can be limited to read/sync tools without listing prepare/execute.
- CGo-free encrypted SQLite cache/state with verified key markers, offline fallback, explicit sync, bounded retention, LRU eviction, and safe rekeying.
- Typed MCP tools over stdio and authenticated Streamable HTTP, plus the generated keyboard-first Go-TUI.
- Deterministic GreenMail `2.1.11` and Radicale `3.7.3` development environments using disposable work and personal principals.

## Verified

- `make validate`: vet, race-enabled unit tests, formatting, and CGo-free build pass.
- `make test-integration`: SMTP→IMAP MIME/attachment round trips and multi-collection CalDAV discovery/CRUD/conflict/isolation pass.
- `make test-e2e`: built-binary multi-account CLI, concurrent operation execution, offline cache, MCP stdio/HTTP writes, mail actions, CalDAV aggregation with an ICS feed, and invitations pass.
- Go-TUI state tests cover keyboard navigation, working modal forms, cancellation, discover, attachment save, paging, and exact prepared-operation previews.

## Release boundary

OAuth, native Gmail/Microsoft APIs, live-provider certification, a contacts registry, a WYSIWYG HTML editor, permanent IMAP expunge, CalDAV scheduling/free-busy, background-daemon sync, and external notifications remain deliberately outside v0.2. HTML is a sendable body type. Contacts are not planned.

The module and installation path are `github.com/timborovkov/posthouse`.
