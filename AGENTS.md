# Agent Guide

Read these first:

- [docs/build.md](docs/build.md)
- [docs/testing.md](docs/testing.md)
- [docs/architecture.md](docs/architecture.md)

Current architecture:

- `craken` is a Go CLI for Craken at `https://craken.borca.ai`.
- It stores named bearer profiles in `${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json`, matching the legacy Node CLI profile format.
- Browser login uses the `/api/client/device-authorizations` and `/api/client/device-token` device-code flow, then product commands call the same HTTP and WebSocket APIs used by the browser.
- Agent login uses the `/api/client/agent-device-authorizations` and `/api/client/agent-device-token` variant when `auth login --as-agent` is passed. It stores an ordinary bearer token plus optional profile metadata for the external agent identity.
- New server APIs and command help are discovered through the server-owned `/api/client` catalog with `craken commands`, `craken do`, `craken get`, `craken post`, and related generic commands.
