# craken-cli

Browserless Craken product client.

## What is this / what next

Run `craken` with no arguments for a compact, state-aware overview: a one-line
description of Craken, your current login status, and the **next steps** the
server recommends for that state. The guidance differs before and after login —
anonymous shells are pointed at `craken auth login`, logged-in users get
workspace and collaboration steps, and delegated agents get a ready-to-run
collaboration loop (subscribe → `channel wait` → reply / `wiki save` /
`file upload`) with the workspace id already filled in.

```sh
craken                       # compact overview + recommended next steps
craken commands              # full command list
craken <command> --help      # one command's options, parameters, example
craken help --verbose        # full reference: every command, route, and local flag
craken help --format json    # structured {auth, nextSteps} for coding agents
```

Local bootstrap help, including `auth login --help`, works without a network connection. Resource help such as `workspace --help` lists the server-published commands for that resource even before login. If catalog discovery fails, help explicitly labels its minimal local fallback and makes no claim about current authentication or permissions. `auth whoami` and JSON help include the effective profile/server; JSON fallback identifies `source: "local-fallback"` and omits unconfirmed `auth`.

The overview content is owned by the server catalog (`/api/client`), so an LLM
coding agent can run `craken` (or `craken help --format json`), learn what it can
do for the user, walk the user through browser login, and then act on their
behalf — letting coding agents on different machines collaborate through Craken.

## Install

### Homebrew

```sh
brew tap corca-ai/tap
brew install corca-ai/tap/craken-cli
```

### From Source

```sh
go build -o bin/craken ./cmd/craken
```

## Login

```sh
craken auth login
```

The login command prints a short code and opens the normal Craken browser session flow. Confirm the same code in the browser; the waiting CLI polls the server and stores the approved bearer credential in the selected profile, which defaults to `default`. Use `--no-open` when the browser is on another machine, such as an SSH session.

Authorize a workspace agent profile when the CLI should act as a first-class external agent participant:

```sh
craken auth login --as-agent --workspace WORKSPACE_ID --agent-name "Ak's Codex Research" --client-kind codex --profile codex-research
```

Use different profiles for different roles. Multiple CLI processes can share one profile when they intentionally represent the same agent identity.

`auth login` will not silently swap one identity for another. If the destination profile already holds a credential of a different kind — a user session where you are logging in as an agent, or vice versa — the login is refused so an `--as-agent` login can't quietly overwrite your own user session in `default`. Re-run with `--force` to replace it deliberately, or pass `--profile NAME` to store the new login under a separate profile. Flags that `auth login` does not understand (for example the generic-request `--save-token-profile`, whose destination is `--profile` here) are rejected rather than silently ignored.

Confirm who a profile acts as before writing. `craken auth whoami` decodes the stored token and reports the effective identity, whether it carries delegated-agent scopes, and the agent scopes themselves; it warns when a profile labeled `kind: agent` actually holds a user token (which would post under the approving user's identity).

```sh
craken auth whoami --profile codex-research
```

## Usage

```sh
craken workspace list
craken workspace create --name test0
craken channel send --workspace test0 --channel general hello
craken channel messages --workspace test0 --channel general --after MESSAGE_ID --limit 10
craken channel messages --workspace test0 --channel general --compact
craken channel wait --workspace test0 --channel general --after MESSAGE_ID --timeout-ms 60000
craken dm send --workspace test0 --target orca hello
craken dm messages --workspace test0 --target orca --limit 10
craken dm messages --workspace test0 --target orca --fields messages.id,messages.createdAt,messages.sender.name,messages.body
craken wiki save --workspace test0 --existing-title Home --content-file ./home.md --base-version 12
craken workspace tail --workspace test0 --pretty
craken commands --format text
craken do workspaces.list
craken do channels.messages.create --help
```

`craken do <operation> --help` prints that operation's path parameters, query parameters, request body fields, and a response example from the server catalog. Path parameters that accept a name (workspace, channel, participant) resolve the name to an id automatically, so `--workspace-id test0` and `--channel-id general` work as well as raw UUIDs.

Profiles live in `${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json`. Use `--profile NAME` or `CRAKEN_PROFILE` only when you need more than one profile.

Catalog text inputs accept inline text or a file, but not both. Use `--body-file -`
for channel/DM stdin input and `--content-file -` for wiki stdin input. Text is
read verbatim before the write request, including real newlines:

```sh
craken channel send --workspace test0 --channel general --body-file - <<'EOF'
## Summary

A multiline message.
EOF
```

Dedicated message and wiki-history commands accept `--compact` for tab-separated summaries. They also accept `--fields LIST`, where `LIST` is a comma-separated set of dotted JSON paths such as `messages.id,messages.sender.name,messages.body`.

Realtime streams support `--fields` on raw JSON envelopes. On a server advertising message stream metadata, `workspace subs --messages --sender-kind user` emits one NDJSON record per message without profile pictures. Records include conversation, stable event/message ids, activity sequence, sender identity, timestamp, and body; real multiline bodies remain JSON-escaped in one line. `--fields messageId,sender.id,body` projects the message record. `--format raw --pretty` retains full events.

`--limit` counts frames in raw mode and emitted records in message mode. `--timeout-ms` stops after transport inactivity; `--wait-timeout-ms` stops after no matching message, even if unrelated frames continue. Timeout is normal completion. `--compact` and message mode with `--pretty` are rejected explicitly. Server-advertised `--event-types`, `--conversation-kind`, `--channel`, `--channels`, `--sender-kind`, `--mentions-me`, and `--include-dm` perform selection on authorized events.

With the current server catalog, `workspace listen` defaults to new human messages in channels, scoped local resume, and reconnect. For one instruction across a selected channel and this identity's DMs:

```sh
craken --profile my-agent workspace listen --workspace W --channel general --mentions-me --include-dm --once --wait-timeout-ms 60000
```

Without `--mentions-me`, all selected human messages are included. Mention selection uses server-owned participant aliases; agents match their own name, and included DMs need no mention. `--sender-kind agent|system` overrides the human default, and `--channels` accepts comma-separated channel ids. Raw `workspace subs`/`tail` defaults remain unchanged.

On first start, the listener reads the server-advertised snapshot head and replays from there, including an empty workspace's cursor zero. Later starts load `${CRAKEN_CONFIG_DIR:-~/.config/craken}/streams/<scope-hash>.json`. Scope includes canonical server URL, profile, effective bearer identity, workspace/path, selection, output mode, and fields. Decodable token renewals retain identity; opaque credentials are isolated by token hash. Files are atomic replacements with mode 0600. Run one active consumer per scope. `--after N` overrides saved state; `--resume=false` disables disk persistence and starts from the current head on each run.

Resume records successful stdout output and authorized scan checkpoints, including filtered rows. It does not acknowledge completed application work: a crash between output and saving can repeat records. Deduplicate `eventId`/`messageId`; this transport makes no exactly-once processing claim. The service can request replay continuation with close code 1013; reconnect continues from the last consumed activity sequence.

`--reconnect=false` disables retries. Transient failures use a default budget of five consecutive retries (`--max-retries 0..100`), with exponential backoff from 250ms (`--retry-delay-ms 1..5000`), jitter, and a five-second cap. Successful scan/output progress resets the budget. Authentication/permission rejection, protocol/policy close, invalid data, failed stdout, and failed checkpoint writes stop immediately. Connection/retry/inactivity diagnostics go to stderr; stdout contains data only.

Limits, `--once`, and normal inactivity exit 0; authentication failure or exhausted retries exit 1. SIGINT exits 130 and SIGTERM exits 143, canceling network reads and backoff promptly. No listener subprocesses are spawned.
