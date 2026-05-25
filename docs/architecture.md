# Architecture

`craken` is a small Go CLI that mirrors the Craken product API surface. It keeps machine-local behavior in the binary: profile storage, device-code browser login polling, request formatting, file upload/download, and realtime WebSocket subscriptions.

The Worker remains the source of truth for product behavior. Product shortcuts such as `workspace list`, `channel send`, and `wiki save` are discovered from the server-owned `/api/client` catalog and executed from the catalog's command plan. The binary keeps only local bootstrap and transport primitives: profile storage, device login polling, request construction, file upload/download, realtime WebSocket handling, and output shaping. `commands`, `do`, and raw HTTP commands use the same catalog route contract as the ergonomic shortcuts.

Wiki saves follow the service's optimistic concurrency contract. Creating a missing page can omit `--base-version`; updating an existing page should read the page first, edit against the returned `latestVersionNumber`, and pass that number as `--base-version`. A stale base version returns the server's 409 conflict payload so scripts can re-read, merge, and retry.

The default help fetches `/api/client` once and renders the server-owned command syntax, descriptions, examples, and operation routes available to the selected bearer session. Operation help such as `craken channel messages --help` focuses on the matching server command when it is present, then augments it with local option knowledge and any route metadata the catalog exposes. Authentication and generic transport flags stay local bootstrap behavior; the product command surface belongs to the Worker so API changes do not require a CLI release just to discover or execute new operations.

Message page commands pass cursor options and `--limit` through to the Worker so service-side validation, clamping, and cursor semantics remain the source of truth.

Dedicated message and wiki-history reads can format service JSON locally. `--compact` emits stable tab-separated rows for shell loops, while `--fields` keeps JSON output but projects only the requested dotted paths so scripts can omit large fields such as profile pictures without changing the service response contract.

The profile file is intentionally compatible with the prior Node CLI:

```text
${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json
```

Each profile stores `baseUrl` and `token`. `CRAKEN_PROFILE`, `CRAKEN_TOKEN`, and `CRAKEN_BASE_URL` provide the same environment overrides as the Node CLI.

`auth login` starts a server-side device login through `/api/client/device-authorizations`, prints the short code and verification URL, and polls `/api/client/device-token` until the browser approves the code. The CLI never opens a loopback callback listener, so the same command works from local shells, SSH sessions, and containers.

Realtime subscriptions use the Craken bearer WebSocket subprotocol pair: `craken-bearer` plus a base64url JSON payload protocol carrying the selected token. `channel wait` is the channel-scoped alternative for agent loops that need one bounded long-poll response instead of the full workspace stream.
