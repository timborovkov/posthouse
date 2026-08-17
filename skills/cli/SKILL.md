---
name: posthouse-cli
description: Operate Posthouse mail and calendar from the local CLI. Use when Posthouse is installed on this machine. Do not try to log the user into Gmail or Microsoft yourself.
---

# Posthouse CLI

Posthouse is the user's mail and calendar switchboard on this machine. It is not a hosted SaaS.

If there are no connections, tell the user to follow GETTING-STARTED.md (connect Gmail/Microsoft with `posthouse connection auth`, or IMAP with an app password). Never ask them to paste a refresh token or create a Google Cloud project.

## Vocabulary

- **Connection**: one connected mailbox or calendar. Never say account or inbox.
- **Selector**: names, category, labels, capabilities. Reads may span connections. Writes never do.
- **Prepared operation**: ten-minute token. Nothing is sent until `posthouse operation execute TOKEN`.

## Safety

- Never log passwords, `POSTHOUSE_CACHE_KEY`, access keys, or refresh tokens.
- Every write needs `--connection`. Do not guess.
- Show the preview and wait for confirmation before `operation execute`.

## Connect (only if the user asked to add a mailbox)

```sh
posthouse connection add --kind gmail --email you@gmail.com
posthouse connection auth gmail-work
posthouse connection add --kind microsoft --email you@outlook.com
posthouse connection auth microsoft-work
```

On a server, add `--device` to `auth`. A link and code print; the user Allow's on their phone.

## Daily commands

```sh
posthouse connection list
posthouse connection doctor CONNECTION
posthouse mail list --category work --unread
posthouse mail search --query TEXT --page-size 25
posthouse mail get --connection CONNECTION --id ID
posthouse mail send --connection CONNECTION --to addr --subject S --body-file FILE
posthouse operation show TOKEN
posthouse operation execute TOKEN
posthouse calendar list --connection CONNECTION --start RFC3339 --end RFC3339
posthouse tui
```

If Posthouse runs on a remote server, use the `posthouse-mcp` or `posthouse-rest` skill instead.
