# Architecture

`craken` is a small Go CLI that mirrors the Craken product API surface. It keeps machine-local behavior in the binary: profile storage, device-code browser login polling, request formatting, file upload/download, and realtime WebSocket subscriptions.

The Worker remains the source of truth for product behavior. Dedicated commands call stable API routes directly, while `commands`, `do`, and raw HTTP commands discover and invoke the server-owned `/api/client` catalog so new routes are usable before a dedicated command is added.

The default help is intentionally catalog-first. It explains login, profiles, generic HTTP commands, and how to inspect `/api/client`; it must not try to duplicate the complete product operation list because that list is owned by the Worker and filtered by the current bearer session.

The profile file is intentionally compatible with the prior Node CLI:

```text
${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json
```

Each profile stores `baseUrl` and `token`. `CRAKEN_PROFILE`, `CRAKEN_TOKEN`, and `CRAKEN_BASE_URL` provide the same environment overrides as the Node CLI.

`auth login` starts a server-side device login through `/api/client/device-authorizations`, prints the short code and verification URL, and polls `/api/client/device-token` until the browser approves the code. The CLI never opens a loopback callback listener, so the same command works from local shells, SSH sessions, and containers.

Realtime subscriptions use the Craken bearer WebSocket subprotocol pair: `craken-bearer` plus a base64url JSON payload protocol carrying the selected token.
