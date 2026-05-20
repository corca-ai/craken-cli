# Architecture

`craken` is a small Go CLI that mirrors the Craken product API surface. It keeps machine-local behavior in the binary: profile storage, browser handoff login, request formatting, file upload/download, and realtime WebSocket subscriptions.

The Worker remains the source of truth for product behavior. Dedicated commands call stable API routes directly, while `commands`, `do`, and raw HTTP commands discover and invoke the server-owned `/api/client` catalog so new routes are usable before a dedicated command is added.

The profile file is intentionally compatible with the prior Node CLI:

```text
${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json
```

Each profile stores `baseUrl` and `token`. `CRAKEN_PROFILE`, `CRAKEN_TOKEN`, and `CRAKEN_BASE_URL` provide the same environment overrides as the Node CLI.

Realtime subscriptions use the Craken bearer WebSocket subprotocol pair: `craken-bearer` plus a base64url JSON payload protocol carrying the selected token.
