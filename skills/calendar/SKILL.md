---
name: posthouse-calendar
description: List and mutate Posthouse calendar events via CLI, including ICS export. Use for agenda and CalDAV writes — not mail or connection setup.
---

# Posthouse calendar

Local CLI for events. Mail → `posthouse-mail`. Connections → `posthouse-connections`.

## Rules

- Writes need exact `--connection` and return a **prepared operation**. Show the preview; only `posthouse operation execute TOKEN` mutates CalDAV.
- Never log event text or tokens unless the user asked.

```sh
posthouse operation show 'TOKEN'
posthouse operation execute 'TOKEN'
```

## List / get

```sh
posthouse calendar list --connection acme --start 2026-08-01T00:00:00Z --end 2026-09-01T00:00:00Z
posthouse calendar list --category work --query planning
posthouse calendar get --id EVENT_OR_OCCURRENCE_ID
```

Pass `--cursor` with identical filters. `--offline` = cache only; `--refresh` refuses stale fallback.

## Create / update / delete

```sh
posthouse calendar create --connection acme --file event.json
posthouse calendar update --connection acme --file event.json
posthouse calendar delete --connection acme --collection COLLECTION_ID --href HREF --etag ETAG
```

Event JSON uses RFC3339 times and a `collection` from `connection discover`. Updates/deletes are ETag-guarded.

## ICS (no provider write)

```sh
posthouse calendar ics --title Planning --start 2026-08-17T09:00:00+03:00 --end 2026-08-17T10:00:00+03:00 --output planning.ics
```

Optional `--method request|cancel` (cancel needs `--id`). Emits `text/calendar` unless `--output` is set.

```sh
posthouse sync --capability calendar.read
```
