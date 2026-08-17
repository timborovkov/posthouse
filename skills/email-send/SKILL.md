---
name: posthouse-email-send
description: Compose and send Posthouse mail via the local CLI. Use for send, reply, forward, or provider drafts with text or HTML — always prepare, show preview, then execute. Not for inbox search or connection setup.
---

# Posthouse email send

Local CLI workflows for **outbound mail and drafts**. For reading/filing use `posthouse-email-inboxes`. For connection setup use `posthouse-connections`.

## Hard rules

- Writes resolve to **exactly one** `--connection`. Never fan out a send.
- Every command below returns a **prepared operation**. Nothing is sent or saved on the provider until `posthouse operation execute TOKEN`.
- Show the preview (connection, identity, recipients, subject, attachments) and wait for explicit confirmation before execute.
- Never log bodies, passwords, or tokens. Recipients are raw addresses (no contacts registry).
- Attachments total ≤ 25 MiB per send/draft.

## Send new mail

```sh
posthouse mail send --connection acme \
  --to teammate@example.test \
  --subject 'Status' \
  --body-file status.txt

posthouse mail send --connection acme \
  --to a@example.test --cc b@example.test \
  --subject 'Status' \
  --html-file status.html
```

Compose flags (also used conceptually for drafts JSON): `--to` / `--cc` / `--bcc` (repeat or comma-separate), `--subject`, `--body` / `--body-file`, `--html` / `--html-file`, attachment flags from `posthouse mail send -h`.

- Text-only → `text/plain`.
- HTML-only → `multipart/alternative` with a derived plain fallback.
- Both text and HTML → `multipart/alternative` as supplied.

Then:

```sh
posthouse operation show 'TOKEN'
posthouse operation execute 'TOKEN'
```

Repeated execute returns the original result. Uncertain SMTP after DATA is **never** retried automatically.

## Reply and forward

Same connection that owns the source message. Reply honors Reply-To.

```sh
posthouse mail reply --connection acme --folder INBOX --uid 42 --body 'Thanks'
posthouse mail reply --connection acme --folder INBOX --uid 42 --html-file reply.html

posthouse mail forward --connection acme --folder INBOX --uid 42 \
  --to other@example.test --body 'FYI'
```

Confirm preview, then `operation execute`.

## Provider drafts

```sh
posthouse mail draft create --connection acme --file draft.json
posthouse mail draft update --connection acme --folder Drafts --uid 9 --file draft.json
posthouse mail draft delete --connection acme --folder Drafts --uid 9
```

`draft.json` matches a send payload shape (recipients, subject, text/html, attachments). Delete is non-expunging provider draft removal via prepare → execute.

## After failure

- Inspect `operation show` before retrying a **new** prepare.
- Do not re-execute a token that already succeeded.
- If the user needs a different connection or recipients, prepare a new operation; do not edit an old token.
