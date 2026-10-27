# Security policy

bbbudget holds some of the most personal data people have, so security reports are very welcome.

## Reporting a vulnerability

**Please don't open a public issue.** Report it privately through GitHub instead: go to the repository's **Security** tab and choose **Report a vulnerability**. Only the maintainer sees the report.

It helps to include:

- what an attacker can do, and from where (a web page you visited, another local user, the network, a malicious budget or template file);
- steps to reproduce, ideally against the fictional `example` template;
- the version (`bbbudget version`) and your OS.

**Never attach your real budget.** If a specific file triggers the bug, cut it down or swap in made-up numbers first.

This is a spare-time project, so responses are best effort. You should hear back within a week or two. Fixes land on `main`, and you'll be credited in the advisory unless you'd rather not be.

## Supported versions

Only the latest commit on `main` gets security fixes.

## Threat model

bbbudget is a **single-user tool on your own machine**. Remote mode (`serve -remote`) lets that one user's other devices in. It is designed to resist:

- **Websites you visit.** The server binds to `127.0.0.1` and rejects requests whose `Host` header isn't a loopback name, which blocks DNS rebinding. Mutations need a JSON content type and `DELETE` needs an `X-Requested-With` header, so a malicious page can't forge them with a plain form or a simple cross-site request. The Host check also covers the MCP endpoint at `/mcp`.
- **Hostile data.** Budgets and templates are strictly validated: unknown fields, exponents and malformed amounts are rejected, and bodies are capped at 1 MB. Budget ids must match `^[a-z0-9][a-z0-9-]{0,63}$` before they reach the filesystem, which rules out path traversal. Arithmetic overflow is an error, never a wrapped value.
- **The UI calling home.** All UI assets are embedded and the UI is served with a same-origin `Content-Security-Policy`, so the page cannot load or send anything to another origin.
- **Other local users.** The data directory is created with mode `0700` and budget files with `0600`. So is `devices.json`.
- **Anyone on the network, in remote mode.** Every API and MCP request needs the token of a device you paired. Tokens carry 128 random bits and pairing codes 50, a code works once and for 10 minutes, and only SHA-256 hashes of either are stored, so reading `devices.json` does not let anyone in. `bbbudget devices -revoke` locks a device out at once, even from a running server.

Out of scope:

- **Anyone who can run code as your user.** They can read `~/.local/share/bbbudget` directly. Budgets are not encrypted at rest; use disk encryption for that.
- **Exposing the server to a network without remote mode.** Without `-remote` there is **no authentication**. `-addr 0.0.0.0` together with `-allow-host` hands every budget, and the ability to change or delete it, to anyone who can reach the port.
- **Remote mode without TLS.** The server speaks plain HTTP. A token sent over a network without TLS can be read and reused by anyone on the path. Put HTTPS in front (Tailscale, Caddy, nginx), or use an SSH tunnel.
- **A paired device that is lost or compromised.** Its token reads and changes every budget until you revoke it.
- **What an AI assistant does with your data.** Through MCP, an assistant can read and edit any budget, and a hosted assistant sends what it reads to its provider. Connect only clients you trust.

Bugs in any of the "designed to resist" items above are vulnerabilities. Please report them as described above.
