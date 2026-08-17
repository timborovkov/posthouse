# Security

Report vulnerabilities privately via
[GitHub security advisories](https://github.com/timborovkov/posthouse/security/advisories/new)
or email tim.borovkov@icloud.com.

Do not open a public issue. Redact secrets and provider content.

## Operator controls for agents

Prepare → execute alone does not stop an MCP client from chaining both calls.
Use these shipped controls:

- **Deny classes** — `posthouse policy deny …`, config `policy.deny`, or
  `POSTHOUSE_POLICY_DENY` block prepare and execute for send, move, trash, and
  other write classes on every surface.
- **MCP readonly profile** — `posthouse mcp … --profile readonly`,
  `policy.mcp_profile`, or `POSTHOUSE_MCP_PROFILE=readonly` omits prepare and
  execute tools from the MCP tool list (`operation_show` remains).
- Autoconfig probe refuses private/loopback destinations unless
  `--allow-private` / `POSTHOUSE_AUTOCONFIG_ALLOW_PRIVATE=1`.

Details: [INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md#policy).
