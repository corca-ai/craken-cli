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
```

Profiles live in `${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json`. Use `--profile NAME` or `CRAKEN_PROFILE` only when you need more than one profile.

Dedicated message and wiki-history commands accept `--compact` for tab-separated summaries. They also accept `--fields LIST`, where `LIST` is a comma-separated set of dotted JSON paths such as `messages.id,messages.sender.name,messages.body`.
