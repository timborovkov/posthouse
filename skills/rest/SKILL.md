---
name: posthouse-rest
description: Call a self-hosted Posthouse REST API for mail and calendar operations. Use when Posthouse is running as `posthouse serve` on localhost or a private server and the user wants HTTP JSON instead of the CLI.
---

# Posthouse REST API

Posthouse is a personal deployment, not a multi-tenant SaaS. The HTTP process serves REST at `/v1` and Streamable MCP at `/mcp`.

## Auth

Every `/v1` and `/mcp` request needs the access key. `/healthz` and `/readyz` are unauthenticated probes.

```
Authorization: Bearer $POSTHOUSE_ACCESS_KEY
```

`X-Posthouse-Key: $POSTHOUSE_ACCESS_KEY` is accepted as an equivalent header. After several failed attempts the server returns 429 with `Retry-After`. Do not brute-force a key.

## Base URL

- Local process: `http://127.0.0.1:8791`
- Private server: the HTTPS origin of the user's reverse proxy or Railway URL. Never disable TLS on a public address.

Discover routes with `GET /v1`.

## Contract

JSON bodies match the MCP tool inputs. Reads may include a selector (`connections`, `category`, `labels`, `capability`, `collections`). Writes require an exact `connection` and return a prepared operation. Only `POST /v1/operations/execute` with `{"token":"..."}` performs the provider side effect. Show the preview and wait for confirmation.

Do not send filesystem attachment `path` fields. Use base64 `data` on attachment objects. Total attachment bytes per mail/draft operation are limited to 25 MiB.

## Examples

```sh
curl -sS -H "Authorization: Bearer $POSTHOUSE_ACCESS_KEY" "$POSTHOUSE_URL/v1/connections"
curl -sS -H "Authorization: Bearer $POSTHOUSE_ACCESS_KEY" -H "Content-Type: application/json" \
  -d '{"connection":"work","to":["teammate@example.test"],"subject":"Status","text":"Update","html":"<p>Update</p>"}' \
  "$POSTHOUSE_URL/v1/mail/send"
```

Useful routes: `POST /v1/mail/search`, `POST /v1/mail/get`, `POST /v1/mail/send`, `POST /v1/calendar/events`, `POST /v1/calendar/create`, `POST /v1/operations/show`, `POST /v1/operations/execute`, `POST /v1/sync`, `GET /v1/cache`.

Never log response bodies that contain message text, event descriptions, or tokens in shared transcripts unless the user asked to see them.
