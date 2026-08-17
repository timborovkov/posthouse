# Publisher checklist — Gmail API and Microsoft Graph

Temporary. Delete this file and `handoffs/` when native Gmail and Microsoft connections ship.

This is maintainer work for adding native Gmail and Microsoft connections after v0.2. Operators never create Google Cloud or Entra projects. They click Allow in a browser. Posthouse is not a SaaS: there is no backend and no hosted OAuth callback.

## You already have

- A local-first CLI/MCP/TUI that must stay local-first.
- A plan to inject client IDs via env (dogfood) then release ldflags (after verification).

## A. Domain and policy (for verification, not for dogfood)

A `PRIVACY.md` in the git repo is not a privacy policy for Google. They want a **public HTTPS homepage and privacy page on a domain you own**, verified in [Google Search Console](https://search.google.com/search-console) with the same account that owns the Cloud project. Both URLs go on the OAuth consent screen and must match. [Verification requirements](https://support.google.com/cloud/answer/13464321): the homepage describes the app; the privacy policy lives on that same domain and is linked from the homepage.

`github.com/.../PRIVACY.md` and `*.github.io` are the cases Google often rejects as a third-party platform. GitHub Pages or Cloudflare Pages is fine **if** the hostname is your domain.

You do **not** need this to create the Desktop/public clients or to dogfood as a test user. Do it before submitting Google verification so other people can click Allow.

When you do publish it:

1. Two static pages on a domain you control, e.g. `https://your.domain/` and `https://your.domain/privacy`.
2. Homepage: one paragraph that Posthouse is a local mail and calendar operator; Google/Microsoft sign-in is optional and on-device; link the privacy page.
3. Privacy page, in plain language:
   - Posthouse runs on the operator’s machine.
   - Mail and calendar data are stored only in the local encrypted cache the operator controls.
   - Posthouse has no backend and does not send mailbox content to Posthouse.
   - OAuth tokens are stored in the OS keychain or an env var the operator sets.
   - If the operator connects an agent (MCP), message content leaves the machine only because they directed that agent to read it.
   - Gmail data is not used to train models. Include Google’s Limited Use sentence: Posthouse’s use of information received from Google APIs adheres to the Google API Services User Data Policy, including the Limited Use requirements.
4. Paste those two URLs into the Google consent screen and, later, Entra branding / publisher domain.

## B. Google Cloud — one Desktop client

1. Open [Google Cloud Console](https://console.cloud.google.com/) with the Google account that will own Posthouse.
2. Create a project, e.g. `posthouse`.
3. Enable **Gmail API** and **Google Calendar API**.
4. APIs & Services → OAuth consent screen:
   - User type: **External** (unless you only ever dogfood inside a Workspace org).
   - App name: Posthouse.
   - Support email: yours.
   - Developer contact: yours.
   - Homepage and privacy policy URLs from A are required for **verification**, not for creating the client or Testing-mode dogfood. Leave them blank until those pages exist.
5. Add test users: your Gmail addresses (unverified apps are capped at 100 users total, lifetime).
6. Scopes, start narrow:
   - `gmail.readonly`
   - `gmail.send`
   - `gmail.compose` if drafts need it
   - Calendar: `calendar.readonly` then `calendar.events` / `calendar` when writes land
   - Add `gmail.modify` only if archive/trash cannot work without it
   - Do **not** add `https://mail.google.com/`
7. Create credentials → OAuth client ID → application type **Desktop app**. Name it `posthouse-desktop`.
8. **Download the JSON immediately.** Google may not show the client secret again. Store in your password manager:
   - `POSTHOUSE_GOOGLE_CLIENT_ID`
   - `POSTHOUSE_GOOGLE_CLIENT_SECRET`
9. Put those two values in your shell env for dogfood. Later, GitHub Actions secrets with the same names for release ldflags. Never commit the JSON or the secret.
10. Stay in Testing until the code can demo sign-in + list + send. Then submit brand + sensitive-scope verification (demo video of TUI/CLI OAuth and mail, not “we dump Gmail into an LLM”). Restricted-scope / CASA only if you must keep `gmail.modify` and Google requires assessment.

Docs:

- [OAuth for desktop apps](https://developers.google.com/identity/protocols/oauth2/native-app)
- [Restricted scope verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/restricted-scope-verification)
- [Verification FAQ](https://support.google.com/cloud/answer/13463817)
- [Gmail API](https://developers.google.com/gmail/api/guides)

## C. Microsoft Entra — one public client

1. Sign in to [Azure Portal](https://portal.azure.com/) / [Entra admin center](https://entra.microsoft.com/). If you only have a personal Microsoft account, create a directory (Azure free / [M365 Developer Program](https://developer.microsoft.com/microsoft-365/dev-program)). New app registrations must live in a directory.
2. App registrations → New registration:
   - Name: Posthouse
   - Supported accounts: **Accounts in any organizational directory and personal Microsoft accounts**
   - Redirect URI: platform **Mobile and desktop**, `http://localhost` (loopback). Device-code does not need a redirect.
3. Authentication → Advanced → **Allow public client flows**: Yes.
4. Certificates & secrets: create **none**. This is a public client.
5. API permissions → Microsoft Graph → Delegated:
   - `User.Read`
   - `Mail.Read`
   - `Mail.Send`
   - `Mail.ReadWrite` when modify/drafts need it
   - `Calendars.Read` / `Calendars.ReadWrite` when calendar lands
   - `offline_access`
   - Do **not** add `IMAP.AccessAsUser.All` / `SMTP.Send` (that is the IMAP OAuth path we rejected).
6. Copy the **Application (client) ID**. Store as `POSTHOUSE_MICROSOFT_CLIENT_ID`. No secret exists.
7. Branding: set publisher domain to the domain from A when you can verify DNS.
8. Dogfood personal Outlook.com/Hotmail first. Work M365 tenants often block unmarked apps until publisher verification.
9. Later: [Microsoft AI Cloud Partner Program](https://partner.microsoft.com/) + [publisher verification](https://learn.microsoft.com/en-us/entra/identity-platform/publisher-verification-overview) so the consent screen shows a verified publisher. Required for many work tenants; not required to start personal-account dogfood.

Docs:

- [Public vs confidential clients](https://learn.microsoft.com/en-us/entra/identity-platform/msal-client-applications)
- [Device code flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-device-code)
- [Graph mail](https://learn.microsoft.com/en-us/graph/api/resources/message)

## D. Client IDs for implementation

Once B and C exist, pass these in the implementing agent chat (not in git):

```
POSTHOUSE_GOOGLE_CLIENT_ID=...
POSTHOUSE_GOOGLE_CLIENT_SECRET=...
POSTHOUSE_MICROSOFT_CLIENT_ID=...
```

Wire env/ldflags only. If the IDs do not exist yet, build against httptest fixtures; `connection auth` should fail closed with “set POSTHOUSE_*_CLIENT_ID”.

## E. Dogfood sequence (live)

After the branch can run `connection auth` and search:

1. Gmail 1: auth, list, get, send to yourself, trash.
2. Gmail 2: second connection, same binary, selector across both.
3. Microsoft personal: same.
4. One existing IMAP/CalDAV connection (iCloud or Fastmail) in the same selector.
5. MCP: `messages_search` with no provider-specific fields; confirm partial errors if one source fails.
6. Keep a private notes file of API surprises (labels vs folders, Graph well-known folders, consent errors). Do not commit mailbox content.

## F. After it works — verification, not code

1. Record a demo video: consent screen, Posthouse name, list/send in CLI or TUI.
2. Submit Google OAuth verification.
3. Entra publisher verification when you want work Microsoft 365.
4. Then embed client IDs in release builds via CI secrets.

## Do not

- Create a web app OAuth client or a redirect on a Posthouse server.
- Ask users to make their own GCP/Entra apps as the default path.
- Put the Google client secret in the git repo.
- Enable IMAP/SMTP Gmail scope `https://mail.google.com/`.
- Promise every corporate M365 tenant will allow an unverified public client.
