# Posthouse

Posthouse gives people and agents one consistent language for working across otherwise separate communication and scheduling providers.

## Language

**Connection**:
One authenticated provider endpoint that offers one or more capabilities, such as mail or calendar, and carries the user's organizational metadata.
_Avoid_: Account, integration, inbox

**Identity**:
The person or address presented to recipients and attendees when a connection acts externally.
_Avoid_: User, account

**Capability**:
A granular operation a connection supports, such as `mail.read`, `mail.send`, `calendar.read`, or `calendar.write`.
_Avoid_: Service, feature

**Category**:
The connection's single broad organizational grouping, such as work or personal.
_Avoid_: Type, group

**Label**:
A freely chosen marker used to select connections across categories, such as acme, finance, or primary.
_Avoid_: Tag

**Selector**:
An intersection of connection names, category, labels, and capabilities that defines the scope of an operation.
_Avoid_: Filter, query

**Cursor**:
An opaque, query-bound continuation token that resumes a list or search after its last returned item without exposing provider paging state as public API.
_Avoid_: Offset, page number, bookmark

**Message**:
An email item received from or sent through a mail-capable connection.
_Avoid_: Mail, email

**Event**:
A scheduled calendar item with a stable iCalendar UID, whether read from a feed or generated as a portable file.
_Avoid_: Meeting, appointment

**Calendar feed**:
A read-only iCalendar subscription identified by a public URL or a private URL supplied through a secret environment variable.
_Avoid_: Calendar account

**Calendar collection**:
A selectable calendar exposed by one CalDAV-capable connection. A connection may discover multiple collections with independent identifiers and read-only state.
_Avoid_: Calendar account, feed

**Prepared operation**:
An encrypted, opaque, short-lived record that binds one external write to its exact connection, acting identity, payload digest, preview, and provider preconditions. Preparation does not perform the provider side effect.
_Avoid_: Draft, queued action

**Policy**:
Optional deny-list of write classes (`mail.send`, `mail.move`, `mail.mark`, `mail.trash`, `mail.junk`, `mail.draft`, `calendar.write`). Empty means allow everything. Denials apply to prepare and execute on every surface (CLI, MCP, TUI).
_Avoid_: Permission, ACL, role

**MCP profile**:
The MCP tool surface: `full` (default, all tools) or `readonly` (read/sync/cache tools only; prepare and execute tools are omitted). Independent of policy deny classes.
_Avoid_: Mode, sandbox, permission set

**Offline cache**:
The encrypted local SQLite state used for bounded stale reads, sync state, fetched bodies/attachments, and prepared operations. It is not a canonical mailbox or calendar database.
_Avoid_: Archive, backup
