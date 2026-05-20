# Build & Release

## Prerequisites

The project uses Go and can be managed with `mise`.

```sh
mise install
```

## Build

```sh
go build -o bin/craken ./cmd/craken
```

Release builds inject the version with `ldflags`.

```sh
go build -trimpath -ldflags="-s -w -X main.version=$(cat VERSION)" -o bin/craken ./cmd/craken
```

## Release

`VERSION` is the release source of truth.

When `VERSION` changes on `main`, `.github/workflows/tag-release.yml` creates the annotated `v*` tag and dispatches `.github/workflows/release.yml`. The release workflow runs GoReleaser, publishes GitHub release artifacts, and updates `corca-ai/homebrew-tap` under `Formula/`.

Required repository secret:

- `HOMEBREW_TAP_TOKEN`: token with write access to `corca-ai/homebrew-tap`

The GoReleaser configuration is in `.goreleaser.yaml`.
