---
name: posthouse-connections
description: Manage Posthouse connections via CLI (add, label, update, remove, probe, discover, doctor). Use for IMAP/SMTP/CalDAV setup — not mail or calendar work.
---

# Posthouse connections

Guide connection **management** on a machine that can run `posthouse`. Mail → `posthouse-mail`. Calendar → `posthouse-calendar`.

## Vocabulary

- **Connection**: one authenticated provider endpoint. Never say account or inbox.
- **Category**: `work` / `personal` (one per connection).
- **Label**: free markers for selectors (`acme`, `primary`). Labels are how you “tag” a connection.
- **Capability**: `mail.read`, `mail.send`, `calendar.read`, `calendar.write`.

Never log passwords, keychain values, `POSTHOUSE_CACHE_KEY`, or access keys.

## List

```sh
posthouse connection list
posthouse connection list --category work --label primary --capability mail.read
```

Pass `--cursor` from `next_cursor` with the same filters.

## Probe, then add

Prefer probing from an identity email (RFC 6186 SRV + Thunderbird XML + CalDAV `/.well-known/caldav`) when hosts are unknown:

```sh
posthouse connection probe --email you@acme.example
posthouse connection add --email you@acme.example --id acme --name "Acme work" \
  --category work --label acme --label primary \
  --secret-env ACME_MAIL_PASSWORD --caldav
```

`--secret-keychain NAME` or `--secret-command` (repeat once per argv — do not put spaces in a single flag) are alternatives to `--secret-env`. Private/loopback discovered hosts need `--allow-private` or `POSTHOUSE_AUTOCONFIG_ALLOW_PRIVATE=1`. Probe redirects are HTTPS-only.

Command secrets (JSON or `--secret-command`) run an argv and take the first line; empty or control-character output is rejected. The subprocess does not inherit `POSTHOUSE_*` env keys.

## Add from JSON

1. Collect hosts, address, category, labels, and secret location (env name, keychain, or command argv). Prefer an **app password**.
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

Command secret: `"secret": {"command": ["pass", "show", "acme-mail"]}`.

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
