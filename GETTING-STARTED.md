# Getting started

Posthouse is a **personal** mail and calendar switchboard for you and your agents. It is not a hosted product and it does not create an account for you. You run it on your laptop, or you deploy it on a machine you control.

You can use it three ways, in any combination:

1. **Yourself**, with the full-screen terminal app or the CLI.
2. **A local agent** (Claude, Cursor, Codex, Hermes, …) through MCP stdio or a skill file.
3. **A private server** you deploy, exposing MCP and a REST API behind an access key.

## 1. Install the CLI

If you have Go 1.26.6:

```sh
curl -fsSL https://raw.githubusercontent.com/timborovkov/posthouse/main/scripts/install.sh | sh
```

Or:

```sh
go install github.com/timborovkov/posthouse/cmd/posthouse@latest
posthouse version
```

From a clone:

```sh
make build
./bin/posthouse version
```

If you would rather not install a binary yet, skip to [Docker](#4-optional-run-it-on-your-own-server).

## 2. Create local secrets

```sh
posthouse setup
```

That prints two values:

- `POSTHOUSE_CACHE_KEY` encrypts local Posthouse state. Desktop use can skip this and let Posthouse store a key in the OS keychain; headless and Docker **must** set it.
- `POSTHOUSE_ACCESS_KEY` protects HTTP (MCP + REST). Stdio MCP on your machine does not need it.

Write them to a file only you can read:

```sh
posthouse setup --write-env "$HOME/.config/posthouse/env"
chmod 600 "$HOME/.config/posthouse/env"
```

Then `set -a; . "$HOME/.config/posthouse/env"; set +a` in the shell that will run Posthouse. Do not commit that file.

## 3. Add a mailbox and calendar

Copy [examples/connection.json](./examples/connection.json). Put a **provider app password** (not your login password) in the environment, then:

```sh
export ACME_MAIL_PASSWORD='the app password'
export ACME_CALENDAR_PASSWORD='the app password'
posthouse connection add --file examples/connection.json
posthouse connection discover acme
posthouse connection doctor acme
```

Native Gmail or Microsoft (no IMAP hostnames). Until publisher-verified client
IDs ship, set yours in the environment, then authorize in a browser:

```sh
export POSTHOUSE_GOOGLE_CLIENT_ID='...'
export POSTHOUSE_GOOGLE_CLIENT_SECRET='...'   # Google Desktop token exchange only
posthouse connection add --file examples/gmail-connection.json
posthouse connection auth gmail-work
posthouse connection doctor gmail-work
```

Microsoft is the same with `POSTHOUSE_MICROSOFT_CLIENT_ID` and
[examples/microsoft-connection.json](./examples/microsoft-connection.json).
Do not register your own Cloud/Entra app unless you are dogfooding; that
registration is the publisher checklist in `handoffs/`.

Use the TUI if you prefer not to memorize commands:

```sh
posthouse tui
```

`Tab` moves around, `?` is help, `o` authorizes Gmail/Microsoft, `q` quits. Sending mail or changing a calendar still shows an exact preview before anything is sent.

## 4. Optional: run it on your own server

This is still **your** process. There is no Posthouse cloud.

### Docker Compose (VPS, homelab, private cloud)

```sh
git clone https://github.com/timborovkov/posthouse
cd posthouse
posthouse setup --write-env .env
# Add provider app-password variables to .env as well.
docker compose up --build
```

The default Compose file publishes `127.0.0.1:8791` on the host. Put Caddy, nginx, or another TLS terminator in front, or use [docker-compose.private.yml](./docker-compose.private.yml) only when the published port is already on a private network or behind TLS:

```sh
docker compose -f docker-compose.yml -f docker-compose.private.yml up --build
```

Set `POSTHOUSE_TRUST_PROXY=1` when a reverse proxy on a private or loopback hop supplies `X-Real-IP` or `X-Forwarded-For`, so brute-force lockout keys off the real client rather than the proxy.

### Railway

1. Create a service from this repository. `railway.json` builds the Dockerfile and health-checks `/healthz`.
2. Attach a volume at `/data`.
3. Set `POSTHOUSE_CACHE_KEY`, `POSTHOUSE_ACCESS_KEY`, `POSTHOUSE_TRUST_PROXY=1`, and your provider secret variables. Railway injects `PORT`; `railway.json` passes `--allow-container-listener` so Posthouse listens on `0.0.0.0:$PORT`.
4. Put the generated HTTPS URL plus the access key into your MCP client or REST calls.

The container still requires the access key. Railway's edge TLS is the reverse proxy; do not turn off bearer auth.

## 5. Connect an agent

Pick the surface that matches how you run Posthouse:

| You have | Teach the agent |
| --- | --- |
| CLI on this machine | `posthouse skill install --agent claude cli` (or `cursor` / `codex` / `hermes`) |
| Local MCP | `posthouse mcp stdio` and the `mcp` skill |
| Server URL | `rest` and/or `mcp` skills, plus `POSTHOUSE_ACCESS_KEY` |

Install every shipped skill:

```sh
posthouse skill install --agent claude --all
posthouse skill list
```

`--dir PATH` copies into any other agent skill folder. Claude marketplace plugins are a later option; skills work today.

Local MCP client snippet:

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

Remote MCP: URL `https://your-host/mcp` with `Authorization: Bearer <POSTHOUSE_ACCESS_KEY>`.

REST: `GET https://your-host/v1` with the same header. Writes still prepare-then-execute.

## Safety in one paragraph

Reads may search several connections. Sends, folder actions, and calendar mutations never do: they target one connection, return a ten-minute preview token, and change nothing until `operation execute` (CLI), `operation_execute` (MCP), or `POST /v1/operations/execute` (REST). Failed HTTP logins are locked out. Provider mail is not stored in the clear.

More detail: [INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md).
