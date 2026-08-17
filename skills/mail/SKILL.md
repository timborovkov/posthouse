---
name: posthouse-mail
description: Read and send Posthouse mail via the local CLI (list, search, triage, unread, get, archive, send, reply, drafts). Use for message work — not connection setup or calendar.
---

# Posthouse mail

Local CLI for messages. Connection setup → `posthouse-connections`. Calendar → `posthouse-calendar`.

## Rules

- Reads may use selectors (`--connection`, `--category`, `--label`). Writes need **exactly one** `--connection`.
- Mark/move/archive/trash/junk/send/reply/forward/draft return a **prepared operation**. Show the preview; only `posthouse operation execute TOKEN` performs the side effect.
- Policy deny classes (`mail.send`, `mail.move`, `mail.mark`, `mail.trash`, `mail.junk`, `mail.draft`) fail prepare and execute. `posthouse policy show` lists the effective deny list.
- Never log bodies, subjects, tokens, or secrets unless the user asked to see them.
- Attachments ≤ 25 MiB per send/draft. Recipients are raw addresses.

```sh
posthouse operation show 'TOKEN'
posthouse operation execute 'TOKEN'
```

Tokens last ~10 minutes. Repeated execute returns the original result. Uncertain SMTP after DATA is never retried.

## Read

```sh
posthouse mail list --category work --label primary --unread
posthouse mail search --query renewal --page-size 25
posthouse mail triage --category work --unread
posthouse mail unread --category work
posthouse mail get --connection acme --folder INBOX --uid 42
posthouse mail attachment --connection acme --folder INBOX --uid 42 --id ATTACH_ID --output ./file.pdf
posthouse mail attachment --connection acme --folder INBOX --uid 42 --id ATTACH_ID --extract-text
```

Pass `--cursor` from `next_cursor` with identical filters. `--offline` = cache only; `--refresh` refuses stale fallback. `mail get` includes `markdown`. `--extract-text` is PDF-only; empty extraction is an error.

## File

```sh
posthouse mail mark --connection acme --folder INBOX --uid 42 --read
posthouse mail mark --connection acme --folder INBOX --uids 41,42,43 --read
posthouse mail archive --connection acme --folder INBOX --uid 42
posthouse mail trash --connection acme --folder INBOX --uid 42
posthouse mail junk --connection acme --folder INBOX --uid 42
posthouse mail move --connection acme --folder INBOX --uid 42 --destination Archive
```

`--uids` batches up to 100 UIDs on mark/move/archive/trash/junk.

## Send, reply, forward, drafts

```sh
posthouse mail send --connection acme --to a@example.test --subject Status --body-file status.txt
posthouse mail send --connection acme --to a@example.test --subject Status --html-file status.html
posthouse mail reply --connection acme --folder INBOX --uid 42 --body 'Thanks'
posthouse mail forward --connection acme --folder INBOX --uid 42 --to b@example.test --body 'FYI'
posthouse mail forward --connection acme --folder INBOX --uid 42 --to b@example.test --verbatim
posthouse mail draft create --connection acme --file draft.json
posthouse mail draft update --connection acme --folder Drafts --uid 9 --file draft.json
posthouse mail draft delete --connection acme --folder Drafts --uid 9
```

`--verbatim` forwards original parts as `message/rfc822` and fails if there are no parts. Preview then lists attachments/comment, not the original body.

`draft.json` shape (same fields as send; `connection_id` optional when `--connection` is set):

```json
{
  "to": ["a@example.test"],
  "cc": ["b@example.test"],
  "subject": "Draft",
  "text": "Plain body",
  "html": "<p>HTML body</p>",
  "attachments": [{"path": "./file.pdf"}]
}
```

Text-only → `text/plain`. HTML-only → `multipart/alternative` with derived plain. Both → as supplied. See `posthouse mail send -h` for cc/bcc/attachments.

## Sync

```sh
posthouse sync --category work
posthouse cache status
```
