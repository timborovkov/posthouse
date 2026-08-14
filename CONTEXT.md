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
A class of operations a connection supports, currently mail or calendar.
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
_Avoid_: Calendar account, CalDAV connection
