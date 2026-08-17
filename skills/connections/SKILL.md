---
name: posthouse-connections
description: Add, label, update, discover, doctor, or remove Posthouse connections via the local CLI. Use when setting up IMAP/SMTP/CalDAV/ICS, changing labels or category, rotating secrets, or troubleshooting a connection — not for reading or sending mail.
---

# Posthouse connections

Guide connection **management** on a machine that can run `posthouse`. Do not use this skill for inbox search or calendar agenda; use `posthouse-email-inboxes`, `posthouse-email-send`, or `posthouse-calendar`.

## Vocabulary

- **Connection**: one authenticated provider endpoint (mail and/or calendar). Never say account or inbox.
- **Category**: single grouping such as `work` or `personal`.
- **Label**: free marker for selectors (`acme`, `primary`, finance). Changing labels is how you “tag” a connection.
- **Capability**: `mail.read`, `mail.send`, `calendar.read`, `calendar.write`.

Never log, print, or commit passwords, keychain values, `POSTHOUSE_CACHE_KEY`, or access keys.

## List and select

```sh
posthouse connection list
posthouse connection list --category work --label primary --capability mail.read
```

Pass `--cursor` from `next_cursor` with the same filters. Output is redacted JSON (no secret values or env names for secrets on doctor/list paths that omit them — still never paste live passwords).

## Add a connection

1. Ask the user for hosts, address, category, labels, and where the app password lives (env var name or keychain entry). Prefer an **app password**, not their normal login.
2. Write a JSON file from their answers (do not embed the password in the file):

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

Read-only ICS feed alternative: `"calendar": {"kind": "feed", "url_secret": {"env": "HOLIDAYS_ICS_URL"}}` (no mail block required).

3. Put the secret in the environment or keychain **before** add:

```sh
export ACME_MAIL_PASSWORD='…'          # user supplies; do not echo
# or:
printf '%s' "$ACME_MAIL_PASSWORD" | posthouse connection secret set acme-mail --file -
# then use "secret": {"keychain": "acme-mail"} in JSON
```

4. Add, discover folders/collections, then doctor:

```sh
posthouse connection add --file connection.json
posthouse connection discover acme
posthouse connection doctor acme
```

`discover` **saves** folder and calendar collection metadata. Printed discover JSON is redacted — **do not** feed it back into `connection update`.

Remote servers need real TLS or STARTTLS. Cleartext auth is loopback-only (`"insecure": true` only for local test servers).

## Update, retag, or replace

`connection update` replaces the whole connection document for that `id` (`--replace` is implied). To change labels/category/name/folders:

1. `posthouse connection list` (and the user's known config) to reconstruct the full JSON.
2. Edit labels/category/fields. Keep the same `id`. Keep secrets as `{"env":"…"}` or `{"keychain":"…"}` — never paste the secret value into JSON.
3. `posthouse connection update --file connection.json`
4. Re-run `discover` / `doctor` if hosts or auth changed.

There is no separate “tag” command: labels in the connection JSON are the tags.

## Remove

```sh
posthouse connection remove acme
```

Confirm with the user first. This removes local config for that connection, not provider mailbox data.

## Secrets and config paths

```sh
posthouse config path
posthouse connection secret set KEYCHAIN_NAME --file -
```

State DB (`posthouse.db`) sits next to the config. Headless environments need `POSTHOUSE_CACHE_KEY`.
