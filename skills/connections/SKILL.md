---
name: posthouse-connections
description: Manage Posthouse connections via CLI (Gmail/Microsoft Allow, IMAP/SMTP/CalDAV). Use for setup — not mail or calendar work.
---

# Posthouse connections

Guide connection **management** on a machine that can run `posthouse`. Mail → `posthouse-mail`. Calendar → `posthouse-calendar`.

Do **not** run OAuth yourself, collect refresh tokens, or ask the user to create a Google Cloud or Microsoft app.

## Vocabulary

- **Connection**: one authenticated provider endpoint. Never say account or inbox.
- **Category**: `work` / `personal` (one per connection).
- **Label**: free markers for selectors (`acme`, `primary`). Labels are how you “tag” a connection.
- **Capability**: `mail.read`, `mail.send`, `calendar.read`, `calendar.write`.

Never log passwords, keychain values, `POSTHOUSE_CACHE_KEY`, access keys, or refresh tokens.

## List

```sh
posthouse connection list
posthouse connection list --category work --label primary --capability mail.read
```

Pass `--cursor` from `next_cursor` with the same filters.

## Gmail and Microsoft

The user clicks Allow. You only run these commands if they asked to add a mailbox **and** `posthouse` is on this machine:

```sh
posthouse connection add --kind gmail --email you@gmail.com
posthouse connection auth gmail-work
posthouse connection add --kind microsoft --email you@outlook.com
posthouse connection auth microsoft-work
```

Gmail needs a browser on this computer. `--device` is the supported **Microsoft** path on a server (prints a link + code; phone clicks Allow). Google Desktop apps often reject device-code — do not promise Gmail `--device`.

There is no MCP or REST login. On a remote server, tell the user to run `connection auth` in a shell on that host.

Refresh tokens go in the OS keychain, or a mode-`0600` file next to config when no keychain exists. Those files are local secrets, not a vault, and are not encrypted with `POSTHOUSE_CACHE_KEY`.

`discover` is only for generic IMAP folders and CalDAV collections.

## IMAP / CalDAV

1. Collect hosts, address, category, labels, and secret location (env name or keychain). Prefer an **app password**.
2. Write JSON with secrets as refs only — never embed the password:

```json
{
  "id": "acme",
  "name": "Acme work",
  "category": "work",
  "labels": ["acme", "primary"],
  "identity": {"name": "Your Name", "email": "you@acme.example"},
  "mail": {
    "username": "you@acme.example",
    "secret": {"env": "ACME_MAIL_PASSWORD"},
    "imap": {"address": "imap.acme.example:993", "tls": true},
    "smtp": {"address": "smtp.acme.example:465", "tls": true},
    "folders": {
      "inbox": "INBOX", "sent": "Sent", "drafts": "Drafts",
      "archive": "Archive", "trash": "Trash"
    },
    "sent_copy": "provider-managed"
  },
  "calendar": {
    "kind": "caldav",
    "url": "https://calendar.acme.example/",
    "username": "you@acme.example",
    "secret": {"env": "ACME_CALENDAR_PASSWORD"},
    "collections": []
  }
}
```

ICS feed: `"calendar": {"kind": "feed", "url_secret": {"env": "HOLIDAYS_ICS_URL"}}`.

3. Export the secret (or `posthouse connection secret set NAME --file -` with `"secret": {"keychain":"NAME"}`), then:

```sh
posthouse connection add --file connection.json
posthouse connection discover acme
posthouse connection doctor acme
```

`discover` saves folders/collections. Redacted discover JSON must **not** be fed into `connection update`. Remote servers need real TLS/STARTTLS; cleartext auth is loopback-only.

## Update / retag / remove

`connection update` replaces the whole document for that `id`. Edit labels/category/hosts in full JSON (keep secret refs), then:

```sh
posthouse connection update --file connection.json
posthouse connection remove acme
```

Confirm remove with the user. Config path: `posthouse config path`.
