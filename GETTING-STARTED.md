# Getting started

Posthouse is your mail and calendar, for you and for agents like Hermes or
Codex. It runs on **your** computer or **your** server. There is no Posthouse
cloud and no Posthouse account.

You do three things:

1. Install Posthouse.
2. Connect Gmail, Microsoft, or another mailbox (click Allow, or paste an app password).
3. Point Hermes, Codex, Claude, or Cursor at it.

That is the whole setup.

## 1. Install

```sh
curl -fsSL https://raw.githubusercontent.com/timborovkov/posthouse/main/scripts/install.sh | sh
posthouse version
```

Or: `go install github.com/timborovkov/posthouse/cmd/posthouse@latest`

On this computer you can skip the next command. On a server (Docker, Railway,
VPS), create the two keys Posthouse needs and keep the file private:

```sh
mkdir -p "$HOME/.config/posthouse"
posthouse setup --write-env "$HOME/.config/posthouse/env"
```

Then load it in the shell that will run Posthouse: `set -a; . "$HOME/.config/posthouse/env"; set +a`

`POSTHOUSE_CACHE_KEY` encrypts the local cache. It does **not** encrypt OAuth
refresh tokens. Those live in the OS keychain, or — if this machine has no
keychain — as mode-`0600` files next to the config (`secrets/`). Those files are
local secrets, not a vault. Prefer the keychain on a laptop.

## 2. Connect a mailbox

### Gmail

```sh
posthouse connection add --kind gmail --email you@gmail.com
posthouse connection auth gmail-work
```

A browser opens. Click **Allow**. Done.

Gmail needs a browser **on this machine**. `--device` (phone Allow) is the
Microsoft server path. Google Desktop apps often refuse device-code; do not use
`--device` for Gmail unless you already know this client allows it.

Google may show an “unverified app” warning until the maintainer finishes
publisher verification. Archive and trash need the restricted `gmail.modify`
scope; that is expected, not a prompt to create your own Cloud project.

### Microsoft (Outlook, Hotmail, Microsoft 365)

```sh
posthouse connection add --kind microsoft --email you@outlook.com
posthouse connection auth microsoft-work
```

Same Allow screen on this computer.

On a server (no browser on that machine), add `--device`:

```sh
posthouse connection add --kind microsoft --email you@outlook.com
posthouse connection auth microsoft-work --device
```

Posthouse prints a link and a short code. Open the link **on your phone**, type
the code, click Allow. You never copy tokens. There is no login page on the
REST or MCP server — connect from a shell.

Work Microsoft 365 sometimes blocks apps that are not yet publisher-verified.
Personal Outlook/Hotmail is the reliable first try.

### Fastmail, iCloud, Proton, or other IMAP

Those use an **app password**, not Allow. Copy
[examples/connection.json](./examples/connection.json), put the app password in
the environment, then:

```sh
posthouse connection add --file connection.json
posthouse connection discover YOUR-ID
```

You can connect several mailboxes. Search looks across all of them.

### Optional: terminal app

```sh
posthouse tui
```

`?` is help. `o` connects Gmail or Microsoft the same way as `connection auth`
(browser on this computer).

If `connection auth` says Google or Microsoft login is not configured, this
build does not include Posthouse's login yet. That is on the maintainer. You
should not create a Google Cloud or Microsoft app.

## 3. Let an agent use it

The agent never logs into Gmail for you. You connected the mailbox above. Now
you only point the agent at Posthouse.

Five skills, one job each: `connections`, `mail`, `calendar`, plus `mcp` / `rest`
for remote transports.

```sh
posthouse skill install --agent hermes connections mail calendar
posthouse skill install --agent codex connections mail calendar
```

`--agent` can also be `claude` or `cursor`. Add `mcp` or `rest` when the agent
should use those transports, or `--all` for every skill. Re-running install
refreshes files and removes retired folders (`posthouse-cli`,
`posthouse-email-inboxes`, `posthouse-email-send`). `--agent codex` installs into
`~/.agents/skills` and also refreshes `~/.codex/skills`.

### On this computer (Hermes, Codex, Claude, Cursor)

Local MCP — paste into the agent's MCP config, with the real path to `posthouse`:

```json
{
  "mcpServers": {
    "posthouse": {
      "command": "/absolute/path/to/posthouse",
      "args": ["mcp", "stdio"]
    }
  }
}
```

Find the binary with `command -v posthouse`.

Codex and Claude can also run the CLI directly after the skill install. Tell
the agent: "Use Posthouse to search my mail."

### On a server (Railway, Docker, VPS)

Posthouse is already running as `posthouse serve`. Give the agent:

### On a server (Railway, Docker, VPS)

Posthouse is already running as `posthouse serve`. Give the agent:

- URL: `https://YOUR-HOST/mcp`
- Header: `Authorization: Bearer` plus the access key from `posthouse setup`

Same URL with `/v1` is the REST API if the agent prefers HTTP JSON.

Connect the mailbox **in a shell on that server** before the agent tries to
read mail. The agent cannot click Allow for you, and there is no REST/MCP
OAuth endpoint.

Microsoft on a server:

```sh
docker compose exec posthouse posthouse --config /data/config.json connection add --kind microsoft --email you@outlook.com
docker compose exec posthouse posthouse --config /data/config.json connection auth microsoft-work --device
```

Gmail on a server needs a browser attached to that machine (`connection auth`
without `--device`). Do not paste refresh tokens into env files or REST bodies.

Docker Compose, from the project directory:

```sh
posthouse setup --write-env .env
docker compose up --build
```

Then run the `connection` commands above with `--config /data/config.json`.

Railway: create a service from this repo, attach a volume at `/data`, set
`POSTHOUSE_CACHE_KEY`, `POSTHOUSE_ACCESS_KEY`, and `POSTHOUSE_TRUST_PROXY=1`.
Open a shell on the service and run the same `connection` commands with
`--config /data/config.json`. Then put `https://YOUR-RAILWAY-HOST/mcp` and the
access key into Hermes or Codex.

Set `POSTHOUSE_TRUST_PROXY=1` when a reverse proxy on a private or loopback hop
supplies `X-Real-IP` or `X-Forwarded-For`, so brute-force lockout keys off the
real client rather than the proxy.

## What sending looks like

Nothing is sent until you confirm. The agent (or you) prepares a message, you
see exactly who it is from and to, then execute. That is true for CLI, TUI,
MCP, and REST.

More detail (flags, IMAP, Docker networking): [INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md).
Marketing/privacy pages for a domain you own (OAuth verification): [website/](./website/).
