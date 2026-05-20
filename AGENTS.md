# Agent Guide

Read these first:

- [docs/build.md](docs/build.md)
- [docs/testing.md](docs/testing.md)
- [docs/architecture.md](docs/architecture.md)

Current architecture:

- `craken` is a Go CLI for Craken at `https://craken.borca.ai`.
- It stores named bearer profiles in `${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json`, matching the legacy Node CLI profile format.
- Browser login uses `/api/client/login` with a loopback callback, then product commands call the same HTTP and WebSocket APIs used by the browser.
- New server APIs can be reached through the server-owned `/api/client` catalog with `craken do`, `craken get`, `craken post`, and related generic commands.
