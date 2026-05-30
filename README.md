# craken-cli

Browserless Craken product client.

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

Dedicated message and wiki-history commands accept `--compact` for tab-separated summaries. They also accept `--fields LIST`, where `LIST` is a comma-separated set of dotted JSON paths such as `messages.id,messages.sender.name,messages.body`.
