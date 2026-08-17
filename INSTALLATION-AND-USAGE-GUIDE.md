# Installation and usage

## Install

**Go 1.26.6** (CLI and TUI):

```sh
go install github.com/timborovkov/posthouse/cmd/posthouse@latest
posthouse help
```

Put `$(go env GOPATH)/bin` on your `PATH` if needed.

**From a clone:**

```sh
make build
./bin/posthouse help
```

**Docker** (MCP HTTP, not the TUI): see [Docker](#docker).

You need IMAP/SMTP and/or CalDAV (or an ICS feed URL). Use an app password,
not your normal login. OAuth is not in v0.2.

## Add a connection

**Probe from an email address** (RFC 6186 SRV + Thunderbird autoconfig +
CalDAV `/.well-known/caldav`). No branded hostname catalogs:

```sh
export ACME_MAIL_PASSWORD='app password'
posthouse connection probe --email you@acme.example
posthouse connection add --email you@acme.example --id acme --category work \
  --label acme --label primary --secret-env ACME_MAIL_PASSWORD --caldav
# command secret with spaces: repeat --secret-command once per argv
# posthouse connection add --email you@acme.example --secret-command pass --secret-command show --secret-command "acme mail"
posthouse connection discover acme
posthouse connection doctor acme
```

Probe refuses loopback, link-local, and private discovered hosts (and HTTPS
redirects to those destinations). For an internal mail server:

```sh
posthouse connection probe --email you@internal.example --allow-private
# or: export POSTHOUSE_AUTOCONFIG_ALLOW_PRIVATE=1
```

Or copy [examples/connection.json](./examples/connection.json), fill in hosts
and your address, keep the password in the environment:

```sh
export ACME_MAIL_PASSWORD='app password'
export ACME_CALENDAR_PASSWORD='app password'
posthouse connection add --file examples/connection.json
posthouse connection discover acme
posthouse connection doctor acme
```

`discover` saves folder roles (inbox/sent/drafts/archive/trash/junk via IMAP
SPECIAL-USE when advertised) and calendar collections. The printed JSON is
redacted — do not feed it to `connection update`.

In the TUI, press `c` on Connections, fill ID/name/email/secret, leave IMAP/SMTP
blank to probe, then Enter to discover folders.

Read-only ICS feed: [examples/feed-connection.json](./examples/feed-connection.json).

Config path: `posthouse config path` (or `--config` / `POSTHOUSE_CONFIG`).
State is `posthouse.db` next to it.

Secret in the JSON is exactly one of:

```json
{"secret":{"env":"ACME_MAIL_PASSWORD"}}
```

```json
{"secret":{"keychain":"acme-mail"}}
```

```json
{"secret":{"command":["pass","show","acme-mail"]}}
```

```sh
printf '%s' "$ACME_MAIL_PASSWORD" | posthouse connection secret set acme-mail --file -
```

Optional `mail.imap.proxy` / `mail.smtp.proxy` (`socks5://` or `http://`). When
unset, `ALL_PROXY` / `HTTPS_PROXY` / `HTTP_PROXY` are honored for IMAP and SMTP.
Loopback targets bypass the **environment** proxy. `NO_PROXY` / `no_proxy`
matches `*`, exact host, `.suffix`, CIDR (`10.0.0.0/8`), and `host:port`.
An explicit connection `proxy` URL is always used (no `NO_PROXY` bypass).

Secret commands inherit the process environment except `POSTHOUSE_*` keys. Other
ambient secrets remain visible to `pass` or a custom argv.

Remote servers need real TLS or STARTTLS. Cleartext auth is loopback-only.

### Provider quirks (generic IMAP/SMTP)

- **Gmail app password**: enable 2-Step Verification, create an app password, and
  quote folder names under `[Gmail]/` (or rely on `connection discover` roles).
- **iCloud**: IMAP username is usually the address local-part; SMTP username is
  the full address; use an app-specific password.
- **Proton**: use Proton Bridge local IMAP/SMTP endpoints and the Bridge password,
  not the account password.
- **Fastmail**: app password for IMAP/SMTP, or wait for a native API backend if
  you prefer that path later.

OAuth / native Gmail and Microsoft Graph are tracked separately and are not part
of this generic setup path.

## Terminal app

The TUI is optional. Config files plus env/keychain secrets, CLI, and MCP
already cover every operation; use the TUI for by-hand setup and tweaking.
Recipients are raw addresses. There is no contacts registry and no WYSIWYG
HTML editor.

```sh
posthouse tui
```

`Tab` / `Shift+Tab` cycle areas and form fields, arrows or `j`/`k` to move,
`/` search, `r` refresh the current page, `c` compose or create, `a` actions,
`d` discover the selected connection, `s` save a loaded attachment (0600,
never overwrite), `n` / `PageDown` and `p` / `PageUp` page inbox (25) and
agenda (100) with opaque cursors, `Enter` open/confirm/prepare, `Esc` back,
`?` help, `q` quit.

Compose includes To, optional CC/BCC, subject, body type (`text` default, or
`html`), a body textarea, and attachment paths. Choosing `html` sends the
textarea as HTML; paste markup. Agenda times are RFC3339 with a visible
example and live valid/invalid markers. Writes still go through a preview
before anything hits the provider. Policy denials apply in the TUI the same
way as CLI/MCP.

## Writes

CLI, TUI, and MCP all work the same: prepare → inspect preview → execute.
Nothing is sent or saved until `operation execute TOKEN` (or the TUI
confirm). Tokens last ten minutes.

An agent (or you) can still chain prepare and execute in one session. To block
classes of writes, use [Policy](#policy). To hide write tools from MCP clients,
use an MCP [readonly profile](#mcp).

CLI output is JSON, except `calendar ics`.

```sh
posthouse mail list --category work --label primary --unread
posthouse mail triage --category work --unread --page-size 25
posthouse mail unread --category work
posthouse mail search --query renewal --page-size 25
posthouse calendar list --collection team --start 2026-08-01T00:00:00Z

posthouse mail get --connection work --folder INBOX --uid 42
posthouse mail attachment --connection work --folder INBOX --uid 42 --id ATTACHMENT_ID --extract-text --output -
posthouse mail send --connection work --to teammate@example.test --subject Status --body-file status.txt
posthouse mail send --connection work --to teammate@example.test --subject Status --html-file status.html
posthouse mail forward --connection work --folder INBOX --uid 42 --to teammate@example.test --verbatim
posthouse mail mark --connection work --folder INBOX --uids 42,43 --read
posthouse mail junk --connection work --folder INBOX --uid 42
posthouse mail archive --connection work --folder INBOX --uids 42,43,44
posthouse operation show 'TOKEN'
posthouse operation execute 'TOKEN'

posthouse mail reply --connection work --folder INBOX --uid 42 --body 'Thanks'
posthouse mail archive --connection work --folder INBOX --uid 42

posthouse calendar create --connection work --file event.json
posthouse calendar ics --title Planning --start 2026-08-17T09:00:00+03:00 --end 2026-08-17T10:00:00+03:00 --output planning.ics
posthouse schema write --dir ./schemas
posthouse sync
posthouse cache status
```

`--offline` uses only the local cache. `--refresh` requires a live read.
`posthouse <command> -h` for flags. Attachments on send/draft: 25 MiB max.
HTML send uses `--html` / `--html-file` (and MCP `html`); text-only stays
`text/plain`, HTML-only gets a derived plain fallback, both become
`multipart/alternative`.

## Policy

Default is **allow everything**. You can deny write classes so prepare and
execute fail on CLI, MCP, and TUI.

| Class | Blocks |
| --- | --- |
| `mail.send` | send, reply, forward |
| `mail.move` | move and archive |
| `mail.mark` | seen / flagged |
| `mail.trash` | trash |
| `mail.junk` | junk |
| `mail.draft` | draft create / update / delete |
| `calendar.write` | CalDAV create / update / delete |

```sh
posthouse policy show
posthouse policy deny mail.send mail.move mail.trash
posthouse policy allow mail.move
```

Same settings in config (`posthouse config path`). Sample `policy` object:
[examples/policy.json](./examples/policy.json).

```json
{
  "policy": {
    "deny": ["mail.send", "mail.trash"],
    "mcp_profile": "readonly"
  }
}
```

Env overlay (merged with config; does not rewrite the file):

```sh
export POSTHOUSE_POLICY_DENY='mail.send,mail.trash'
```

Typical agent setups:

- **Read-only agent**: `posthouse policy mcp-profile readonly` (or
  `--profile readonly` / `POSTHOUSE_MCP_PROFILE=readonly`) so prepare/execute
  tools are not listed.
- **Full tools, no sending**: keep MCP `full`, deny `mail.send` (and any other
  classes you do not want).
- **No destructive mail**: deny `mail.move`, `mail.trash`, `mail.junk`.

`policy show` prints effective deny (config + env), the effective MCP profile,
and the known class list.

## MCP

```json
{
  "mcpServers": {
    "posthouse": {
      "command": "/absolute/path/to/posthouse",
      "args": ["mcp", "stdio"],
      "env": {
        "POSTHOUSE_CACHE_KEY": "...",
        "ACME_MAIL_PASSWORD": "..."
      }
    },
    "posthouse-readonly": {
      "command": "/absolute/path/to/posthouse",
      "args": ["mcp", "stdio", "--profile", "readonly"],
      "env": {
        "POSTHOUSE_CACHE_KEY": "..."
      }
    }
  }
}
```

HTTP (token required, loopback only):

```sh
export POSTHOUSE_MCP_TOKEN='a-long-random-token'
export POSTHOUSE_CACHE_KEY='a-base64-or-hex-encoded-32-byte-key'
posthouse mcp http --address 127.0.0.1:8791
# optional: --profile readonly
```

`/mcp` is the API. `/healthz` is liveness; `/readyz` is config and cache key,
not provider connectivity. Headless/Docker must set `POSTHOUSE_CACHE_KEY`.

### MCP profiles

| Profile | Tools |
| --- | --- |
| `full` (default) | Reads plus prepare/execute write tools |
| `readonly` | Reads, doctor, sync, cache, and `operation_show` — no prepare/execute |

Resolution order for the profile:

1. `--profile full|readonly` on `mcp stdio` / `mcp http`
2. else `policy.mcp_profile` in config, including an explicit `full` (`posthouse policy mcp-profile …`)
3. else `POSTHOUSE_MCP_PROFILE`
4. else `full`

Deny classes still apply under `full`: if a tool is listed but the class is
denied, prepare and execute return an error.

Agent-oriented mail tools include `messages_triage`, `messages_unread_counts`,
`messages_get` (includes `markdown`), `messages_attachment_get` with
`extract_text` for PDFs, `messages_forward_prepare` with `verbatim`, and
`messages_action_prepare` with `junk` plus batch `uids`. Prefer
`messages_draft_prepare` when the operator should review before send. Every
write still requires `operation_execute`, and both steps honor policy deny.

## Docker

```sh
cp .env.example .env   # set POSTHOUSE_CACHE_KEY and POSTHOUSE_MCP_TOKEN
docker compose up --build
```

Listens on `127.0.0.1:8791`. Data is the `posthouse-data` volume (`/data`).

Optional in `.env` (passed through by `docker-compose.yml`):

```sh
POSTHOUSE_MCP_PROFILE=readonly
POSTHOUSE_POLICY_DENY=mail.send,mail.trash
```

Or set `policy` in `/data/config.json` inside the volume. For a readonly MCP
HTTP process without changing config:

```sh
# example override
docker compose run --rm posthouse \
  --config /data/config.json mcp http --address 0.0.0.0:8791 \
  --allow-container-listener --profile readonly
```

## Environment variables

| Variable | Role |
| --- | --- |
| `POSTHOUSE_CONFIG` | Config file path (else platform default / `--config`) |
| `POSTHOUSE_CACHE_KEY` | Encrypted cache key (required headless / Docker) |
| `POSTHOUSE_CACHE_KEY_NEW` | New key for `cache rekey --key-env` |
| `POSTHOUSE_MCP_TOKEN` | Bearer token for `mcp http` |
| `POSTHOUSE_MCP_PROFILE` | Default MCP profile when `--profile` and config are unset: `full` or `readonly`. An explicit config `full` beats this env. |
| `POSTHOUSE_POLICY_DENY` | Comma-separated deny classes merged with `policy.deny` |
| `POSTHOUSE_AUTOCONFIG_ALLOW_PRIVATE` | `1`/`true` allows private/loopback hosts from probe (same as `--allow-private`) |
| `ALL_PROXY` / `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY` | IMAP/SMTP env proxy when connection proxy fields are unset. `NO_PROXY` supports `*`, host, `.suffix`, CIDR, `host:port`. Explicit connection proxies ignore `NO_PROXY`. |

Connection secrets use whatever env names you put in `secret.env` (for example
`ACME_MAIL_PASSWORD`).

## Cache

Encrypted local SQLite, not a backup of your mailbox. `cache clear` drops
local cache, not provider data. Headless rekey:

```sh
export POSTHOUSE_CACHE_KEY_NEW='new-base64-or-hex-encoded-32-byte-key'
posthouse cache rekey --key-env POSTHOUSE_CACHE_KEY_NEW
export POSTHOUSE_CACHE_KEY="$POSTHOUSE_CACHE_KEY_NEW"
unset POSTHOUSE_CACHE_KEY_NEW
```

Then restart anything still running with the old key.

## License

[MIT](./LICENSE). Built by [Tim Borovkov](https://timb.dev).
