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
posthouse setup --write-env "$HOME/.config/posthouse/env"
```

Then load it in the shell that will run Posthouse: `set -a; . "$HOME/.config/posthouse/env"; set +a`

## 2. Connect a mailbox

### Gmail

```sh
posthouse connection add --kind gmail --email you@gmail.com
posthouse connection auth gmail-work
```

A browser opens. Click **Allow**. Done.

On a server (no browser on that machine), add `--device`:

```sh
posthouse connection add --kind gmail --email you@gmail.com
posthouse connection auth gmail-work --device
```

Posthouse prints a link and a short code. Open the link **on your phone**, type
the code, click Allow. You never copy tokens.

### Microsoft (Outlook, Hotmail, Microsoft 365)

```sh
posthouse connection add --kind microsoft --email you@outlook.com
posthouse connection auth microsoft-work
```

Same Allow screen. On a server, add `--device` as above.

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

`?` is help. `o` connects Gmail or Microsoft the same way as `connection auth`.

If `connection auth` says Google or Microsoft login is not configured, this
build does not include Posthouse's login yet. That is on the maintainer. You
should not create a Google Cloud or Microsoft app.

## 3. Let an agent use it

The agent never logs into Gmail for you. You connected the mailbox above. Now
you only point the agent at Posthouse.

Install the how-to files Hermes, Codex, Claude, and Cursor read:

```sh
posthouse skill install --agent hermes --all
posthouse skill install --agent codex --all
```

`--agent` can also be `claude` or `cursor`.

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

- URL: `https://YOUR-HOST/mcp`
- Header: `Authorization: Bearer` plus the access key from `posthouse setup`

Same URL with `/v1` is the REST API if the agent prefers HTTP JSON.

Connect Gmail **on that server** with `--device` (section 2) before the agent
tries to read mail. The agent cannot click Allow for you.

Docker Compose, from the project directory:

```sh
posthouse setup --write-env .env
docker compose up --build
docker compose exec posthouse posthouse --config /data/config.json connection add --kind gmail --email you@gmail.com
docker compose exec posthouse posthouse --config /data/config.json connection auth gmail-work --device
```

Railway: create a service from this repo, attach a volume at `/data`, set
`POSTHOUSE_CACHE_KEY`, `POSTHOUSE_ACCESS_KEY`, and `POSTHOUSE_TRUST_PROXY=1`.
Open a shell on the service and run the same two `connection` commands with
`--config /data/config.json`. Then put `https://YOUR-RAILWAY-HOST/mcp` and the
access key into Hermes or Codex.

## What sending looks like

Nothing is sent until you confirm. The agent (or you) prepares a message, you
see exactly who it is from and to, then execute. That is true for CLI, TUI,
MCP, and REST.

More detail (flags, IMAP, Docker networking): [INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md).
