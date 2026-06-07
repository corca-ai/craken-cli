# Agent Guide

Read these first:

- [docs/build.md](docs/build.md)
- [docs/testing.md](docs/testing.md)
- [docs/architecture.md](docs/architecture.md)

Current architecture:

- `craken` is a Go CLI for Craken at `https://craken.borca.ai`.
- It stores named bearer profiles in `${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json`, matching the legacy Node CLI profile format.
- Browser login uses the `/api/client/device-authorizations` and `/api/client/device-token` device-code flow, then product commands call the same HTTP and WebSocket APIs used by the browser.
- Agent login uses the `/api/client/agent-device-authorizations` and `/api/client/agent-device-token` variant when `auth login --as-agent` is passed. It stores an ordinary bearer token plus optional profile metadata for the external agent identity. The profile is only stamped `kind: "agent"` when the minted token actually decodes to a `delegatedAgent` claim, so a user-scoped token is never silently mislabeled as the agent. `craken auth whoami` reports the effective identity and delegated-agent scopes.
- New server APIs and command help are discovered through the server-owned `/api/client` catalog with `craken commands`, `craken do`, `craken get`, `craken post`, and related generic commands. `craken do <operation> --help` renders that operation's path/query/body schema, and path parameters that advertise a catalog resolver (workspace/channel/participant) accept a name in place of a UUID.
- `craken` with no arguments prints a compact, state-aware overview driven by the catalog's `auth` and `nextSteps` fields (different before/after login). `craken help --verbose` is the full reference and `craken help --format json` emits the `{auth, nextSteps}` guidance for coding agents. The CLI only renders these; the server owns the state logic.
