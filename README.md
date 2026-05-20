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
craken auth login --profile ak
```

The login command opens the normal Craken browser session flow, asks for CLI authorization, and stores a bearer credential in the selected profile.

## Usage

```sh
craken workspace list --profile ak
craken workspace create --profile ak --name test0
craken channel send --profile ak --workspace test0 --channel general hello
craken dm send --profile ak --workspace test0 --target orca hello
craken workspace tail --profile ak --workspace test0 --pretty
craken commands --profile ak --format text
craken do workspaces.list --profile ak
```

Profiles live in `${CRAKEN_CONFIG_DIR:-~/.config/craken}/config.json`.
