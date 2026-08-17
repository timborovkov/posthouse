# Roadmap

## v0.2.0 release gate

- Run `make validate-all` from a clean checkout with Docker available.
- Keep GreenMail `2.1.11`, Radicale `3.7.3`, generated Go-TUI sources, migration notes, and MCP descriptions aligned with the tag.
- Publish only when both GitHub Actions jobs pass; there is no partial v0.2 release.

## Deliberately outside v0.2.0

- Live-provider certification gates for Fastmail, iCloud, Google, or Microsoft.
- A contacts registry or recipient picker (recipients stay raw addresses).
- A WYSIWYG HTML editor (HTML is a sendable body type, not a rich composer).
- Permanent IMAP expunge and background-daemon sync.
- Server-side CalDAV scheduling/free-busy and attendee-response processing.
- Provider notifications or other external push integrations.
- Publisher OAuth app verification and shipping verified client IDs.

Native Gmail API and Microsoft Graph backends are implemented after v0.2: public
client + PKCE, `connection auth`, and the same MCP/CLI selector. Operators set
`POSTHOUSE_GOOGLE_CLIENT_ID` / `POSTHOUSE_MICROSOFT_CLIENT_ID` until verified IDs
ship. Do not commit client secrets or refresh tokens.

## After v0.2: native Gmail and Microsoft Graph

Implementing brief: [handoffs/native-gmail-microsoft.md](./handoffs/native-gmail-microsoft.md). Publisher registration: [handoffs/publisher-gmail-microsoft.md](./handoffs/publisher-gmail-microsoft.md). Delete `handoffs/` when this ships.

Publisher-owned desktop/public OAuth clients; Gmail API + Graph as extra backends; IMAP/CalDAV remain for generic connections. Users consent in a browser; they do not create Cloud projects. Refresh tokens use existing keychain/env refs. Public message identity becomes opaque `id` so search can compose IMAP + Gmail + Graph into one page. No Posthouse token proxy, no IMAP XOAUTH2 for these two vendors, no branded protocol presets.

## Later hardening

- Broaden generic-server interoperability fixtures and fuzz MIME/iCalendar parsing.
- Add scoped HTTP authorization and independent read/write credentials.
- External security review, SBOM/signing, vulnerability gates, and recovery documentation.
- Optional provider presets and OAuth without weakening generic protocol support.
