---
name: posthouse-email-inboxes
description: Read, search, open, and file Posthouse mail via the local CLI. Use for inbox listing, unread, search, message get, attachments, mark read/flagged, move, archive, or trash — not for composing or sending new mail, and not for connection setup.
---

# Posthouse email inboxes

Local CLI workflows for **reading and filing** messages. For compose/send/reply/forward/drafts use `posthouse-email-send`. For adding connections use `posthouse-connections`.

## Vocabulary

- **Connection** / **selector**: reads may span connections via `--connection`, `--category`, `--label`. Never say account or inbox as a synonym for connection.
- **Prepared operation**: archive/trash/move/mark only take effect after `posthouse operation execute TOKEN`. Show the preview and wait for confirmation.

Never log message bodies, subjects, or tokens into shared transcripts unless the user asked to see them. Never print secrets.

## List and search

Output is JSON. Default page size 25 (max 100).

```sh
posthouse mail list --category work --label primary --unread
posthouse mail list --connection acme --folder INBOX --page-size 25
posthouse mail search --query renewal --since 2026-08-01T00:00:00Z
posthouse mail search --query invoice --connection acme --unread
```

- Pass `--cursor` from `next_cursor` with **identical** filters.
- `--offline` = encrypted cache only. `--refresh` = live read, no stale fallback.
- `--folder` defaults to each connection's inbox when omitted on list/search.

## Open one message

Exact connection + folder + UID required:

```sh
posthouse mail get --connection acme --folder INBOX --uid 42
posthouse mail attachment --connection acme --folder INBOX --uid 42 --id ATTACH_ID --output ./file.pdf
```

Attachment `--id` comes from `mail get`. Use `--force` only when replacing an existing output path is intentional.

## File and flag (prepare → confirm → execute)

These commands return a prepared operation; they do **not** mutate the provider until execute.

```sh
posthouse mail mark --connection acme --folder INBOX --uid 42 --read
posthouse mail mark --connection acme --folder INBOX --uid 42 --unread
posthouse mail mark --connection acme --folder INBOX --uid 42 --flagged
posthouse mail move --connection acme --folder INBOX --uid 42 --destination Archive
posthouse mail archive --connection acme --folder INBOX --uid 42
posthouse mail trash --connection acme --folder INBOX --uid 42

posthouse operation show 'TOKEN'
posthouse operation execute 'TOKEN'
```

Rules:

- Always pass exact `--connection`. Do not guess when multiple connections match.
- Show preview (connection, action, target) to the user before `operation execute`.
- Tokens last ~10 minutes. Repeated execute returns the original result.
- Prefer `archive` / `trash` over inventing folder names; use `move` when the user names a destination folder.

## Sync / cache (optional)

```sh
posthouse sync --category work
posthouse cache status
```

`cache clear` drops local cache only, not provider mail.
