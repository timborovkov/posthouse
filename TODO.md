# Roadmap

## v0.2.0 release gate

- Run `make validate-all` from a clean checkout with Docker available.
- Keep GreenMail `2.1.11`, Radicale `3.7.3`, generated Go-TUI sources, migration notes, and MCP descriptions aligned with the tag.
- Publish only when both GitHub Actions jobs pass; there is no partial v0.2 release.

## Shipped: native Gmail API and Microsoft Graph

These backends are implemented: public client + PKCE, `connection auth`, opaque message `id`, and the same MCP/CLI selector. IMAP/CalDAV remain for generic connections. Operators set `POSTHOUSE_GOOGLE_CLIENT_ID` / `POSTHOUSE_MICROSOFT_CLIENT_ID` until verified IDs ship. Do not commit client secrets or refresh tokens.

- Gmail archive/trash uses the restricted `gmail.modify` scope (that one restricted Gmail scope, not stacked `readonly` + `compose` + `modify`). Publisher verification / CASA is maintainer work: [handoffs/publisher-gmail-microsoft.md](./handoffs/publisher-gmail-microsoft.md).
- `--device` is the supported Microsoft headless path. Gmail uses loopback in a browser; Google Desktop apps often reject device-code.
- Fallback OAuth secrets on machines without a keychain are mode-`0600` files next to config (`secrets/`). They are local files, not a vault, and are not encrypted with `POSTHOUSE_CACHE_KEY`.
- There is no REST or MCP OAuth endpoint. Connect from a shell.

Keep `handoffs/` until verified publisher IDs are ready to land.

## Deliberately outside v0.2.0

- Live-provider certification gates for Fastmail, iCloud, Google, or Microsoft.
- A contacts registry or recipient picker (recipients stay raw addresses).
- A WYSIWYG HTML editor (HTML is a sendable body type, not a rich composer).
- Permanent IMAP expunge and background-daemon sync.
- Server-side CalDAV scheduling/free-busy and attendee-response processing.
- Provider notifications or other external push integrations.
- Publisher OAuth app verification and shipping verified client IDs.

Homepage and privacy pages for Google/Microsoft verification live in [`website/`](./website/) (deploy on a domain you own; see the publisher handoff).

## Later hardening

- Broaden generic-server interoperability fixtures and fuzz MIME/iCalendar parsing.
- Add scoped HTTP authorization and independent read/write credentials (a single access key with brute-force lockout is shipped).
- External security review, SBOM/signing, vulnerability gates, and recovery documentation.
- Marketplace plugins for Claude, Hermes, and ChatGPT; today agents install `posthouse skill` files.
