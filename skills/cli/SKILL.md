---
name: posthouse-cli
description: Operate Posthouse mail and calendar connections from the local CLI. Use when the user has Posthouse installed on this machine and wants list, search, send, or calendar writes through `posthouse` commands.
---

# Posthouse CLI

Posthouse is a local-first switchboard for multiple mail and calendar connections. It is not a hosted SaaS. Prefer this skill when you can run `posthouse` on the same machine.

## Vocabulary

- **Connection**: one authenticated provider endpoint (mail and/or calendar). Never say account or inbox.
- **Selector**: intersection of connection names, category, labels, and capabilities. Reads may fan out. Writes never do.
- **Prepared operation**: opaque ten-minute token. Preparation does not send mail or mutate CalDAV. Only `posthouse operation execute TOKEN` performs the side effect.

## Safety

- Never log, print, or commit passwords, `POSTHOUSE_CACHE_KEY`, or access keys.
- Every write needs `--connection` (exact ID or unique name). Do not guess.
- Show the prepared preview to the user and wait for confirmation before `operation execute`.
- Repeated execute returns the original result. Uncertain SMTP after DATA is never retried automatically.

## Commands

Data commands emit JSON except `calendar ics`, which emits `text/calendar` unless `--output` is set.

```sh
posthouse connection list
posthouse connection doctor CONNECTION
posthouse mail list --category work --label primary --unread
posthouse mail search --query TEXT --page-size 25
posthouse mail get --connection CONNECTION --id ID
posthouse mail send --connection CONNECTION --to addr --subject S --body-file FILE
posthouse mail send --connection CONNECTION --to addr --subject S --html-file FILE.html
posthouse operation show TOKEN
posthouse operation execute TOKEN
posthouse calendar list --connection CONNECTION --start RFC3339 --end RFC3339
posthouse calendar create --connection CONNECTION --file event.json
posthouse sync
posthouse cache status
posthouse tui
```

Use `--offline` for cache-only reads and `--refresh` to refuse stale fallback. Pass `--cursor` from `next_cursor` with identical filters.

If Posthouse is deployed on a remote server instead, use the `posthouse-rest` or `posthouse-mcp` skill.
