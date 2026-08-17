---
name: posthouse-calendar
description: List, inspect, create, update, delete, or export Posthouse calendar events via the local CLI. Use for agenda search, CalDAV writes, and portable ICS files — not for mail or connection setup.
---

# Posthouse calendar

Local CLI workflows for **events**. For mail use `posthouse-email-inboxes` / `posthouse-email-send`. For connection setup use `posthouse-connections`.

## Vocabulary

- **Event**: scheduled item with a stable iCalendar UID.
- **Calendar collection**: selectable calendar on a CalDAV connection (not a “calendar account”).
- **Calendar feed**: read-only ICS subscription connection.
- **Prepared operation**: create/update/delete only take effect after `posthouse operation execute TOKEN`.

Never log event descriptions or tokens unless the user asked. Writes need exact `--connection`.

## List and get

```sh
posthouse calendar list --connection acme --start 2026-08-01T00:00:00Z --end 2026-09-01T00:00:00Z
posthouse calendar list --category work --query planning --page-size 100
posthouse calendar get --id EVENT_OR_OCCURRENCE_ID
```

- Default list window is roughly now → +30 days when `--start`/`--end` omitted.
- Pass `--cursor` from `next_cursor` with identical filters (and the same explicit range if you set one).
- `--offline` = cache only (miss is an error, not an empty calendar). `--refresh` refuses stale fallback.
- Max page size 500.

## Create / update (prepare → confirm → execute)

Event JSON file (times are RFC3339):

```json
{
  "title": "Planning",
  "description": "Sprint plan",
  "location": "Room A",
  "start": "2026-08-17T09:00:00+03:00",
  "end": "2026-08-17T10:00:00+03:00",
  "collection": "COLLECTION_ID"
}
```

Include fields returned from list/get when updating (id/href/etag as required by the prepared preview). Collection IDs come from `connection discover`.

```sh
posthouse calendar create --connection acme --file event.json
posthouse calendar update --connection acme --file event.json
posthouse operation show 'TOKEN'
posthouse operation execute 'TOKEN'
```

## Delete

ETag-guarded. Expanded recurrence instances cannot be deleted as whole objects without `--recurrence-id` when applicable.

```sh
posthouse calendar delete --connection acme \
  --collection COLLECTION_ID --href HREF --etag ETAG
```

Then show preview → `operation execute`.

## Portable ICS (no provider write)

Emits `text/calendar` to stdout unless `--output` is set.

```sh
posthouse calendar ics --title Planning \
  --start 2026-08-17T09:00:00+03:00 \
  --end 2026-08-17T10:00:00+03:00 \
  --output planning.ics

posthouse calendar ics --title Planning --start … --end … \
  --method request --attendee a@example.test --organizer you@example.test
posthouse calendar ics --id EXISTING --title Planning --start … --end … \
  --method cancel
```

`--method cancel` requires `--id`. This does not mutate CalDAV; it only writes a file.

## Sync

```sh
posthouse sync --capability calendar.read
posthouse cache status
```
