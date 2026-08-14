# Roadmap

## v0.2 — accounts people can actually onboard

- OAuth 2.0 authorization-code flow, refresh-token storage, IMAP XOAUTH2, and SMTP OAuth for Google Workspace and Microsoft 365.
- OS keychain and encrypted-file secret backends; keep environment references for containers.
- Provider presets and discovery for Gmail, Microsoft 365, Fastmail, iCloud, generic IMAP/SMTP, and private ICS feed URLs.
- Connection doctor command with non-mutating authentication and capability checks.
- Full-screen TUI for onboarding, selection, previews, and explicit write confirmation.

## v0.3 — complete daily operations

- Fetch complete MIME messages and safe text/HTML alternatives; stream attachments with size limits.
- Reply, forward, draft, move, archive, flag, and mark read/unread.
- Optional CalDAV/provider-API connectors for event update, recurrence, cancellations, attendee status, and free/busy.
- Per-connection partial failures so cross-connection reads return useful results plus structured errors.
- Provider-aware rate limiting and retry guidance.

## Before a public production release

- Threat model, security policy, audit logging with content redaction, and external review.
- HTTP authorization scopes separating reads, mail writes, artifact generation, and future provider mutations.
- Integration suites against disposable IMAP/SMTP servers and representative ICS feeds.
- Stable config migrations, release automation, signed artifacts, SBOM, and vulnerability scanning.
- Resolve the final project name and GitHub organization.
