package craken

import (
	"context"
	"fmt"
	"io"
)

const (
	defaultBaseURL = "https://craken.borca.ai"
	defaultProfile = "default"
)

type command struct {
	Resource    string
	Action      string
	Options     map[string]string
	Flags       map[string]bool
	Positionals []string
	Help        bool
}

func Run(ctx context.Context, version string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	cmd, err := parseCommand(args)
	if err != nil {
		return err
	}
	if cmd.Help || cmd.Resource == "" {
		printHelp(stdout)
		return nil
	}
	if cmd.Resource == "version" || cmd.Resource == "--version" || cmd.Resource == "-version" {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}

	logger, err := newLogger(cmd.string("log-file", ""))
	if err != nil {
		return err
	}
	defer logger.close()

	if cmd.Resource == "auth" {
		return runAuth(ctx, cmd, stdin, stdout, stderr)
	}

	client, err := newClient(cmd, logger)
	if err != nil {
		return err
	}

	switch cmd.Resource {
	case "commands", "catalog":
		return runCommands(ctx, client, cmd, stdout)
	case "do":
		return runDo(ctx, client, cmd, stdout, stdin)
	case "get", "post", "put", "patch", "delete":
		return runRawHTTP(ctx, client, cmd.Resource, cmd.Action, cmd, stdout, stdin)
	case "api":
		return runRawHTTP(ctx, client, cmd.Action, first(cmd.Positionals, cmd.string("path", "")), cmd.withPositionals(rest(cmd.Positionals)), stdout, stdin)
	case "workspace":
		return runWorkspace(ctx, client, cmd, stdout)
	case "channel":
		return runChannel(ctx, client, cmd, stdout)
	case "dm":
		return runDM(ctx, client, cmd, stdout)
	case "file":
		return runFile(ctx, client, cmd, stdout)
	case "folder":
		return runFolder(ctx, client, cmd, stdout)
	case "wiki":
		return runWiki(ctx, client, cmd, stdout)
	case "agent":
		return runAgent(ctx, client, cmd, stdout)
	case "dream":
		return runDream(ctx, client, cmd, stdout)
	default:
		return fmt.Errorf("unknown resource: %s", cmd.Resource)
	}
}

func parseCommand(args []string) (command, error) {
	cmd := command{Options: map[string]string{}, Flags: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		current := args[i]
		if current == "--help" || current == "-h" {
			cmd.Help = true
			continue
		}
		if len(current) > 2 && current[:2] == "--" {
			raw := current[2:]
			name, inline, hasInline := splitOption(raw)
			if name == "" {
				return cmd, fmt.Errorf("invalid option: %s", current)
			}
			if hasInline {
				cmd.Options[name] = inline
				continue
			}
			if i+1 >= len(args) || hasOptionPrefix(args[i+1]) {
				cmd.Flags[name] = true
				continue
			}
			cmd.Options[name] = args[i+1]
			i++
			continue
		}
		if cmd.Resource == "" {
			cmd.Resource = current
		} else if cmd.Action == "" {
			cmd.Action = current
		} else {
			cmd.Positionals = append(cmd.Positionals, current)
		}
	}
	if cmd.Action == "" {
		if cmd.Resource == "workspace" {
			cmd.Action = "list"
		} else {
			cmd.Action = "help"
		}
	}
	return cmd, nil
}

func splitOption(raw string) (string, string, bool) {
	for i, r := range raw {
		if r == '=' {
			return raw[:i], raw[i+1:], true
		}
	}
	return raw, "", false
}

func hasOptionPrefix(value string) bool {
	return len(value) >= 2 && value[:2] == "--"
}

func (cmd command) string(name string, fallback string) string {
	if value, ok := cmd.Options[name]; ok && trim(value) != "" {
		return trim(value)
	}
	return fallback
}

func (cmd command) required(names ...string) (string, error) {
	for _, name := range names {
		if value := cmd.string(name, ""); value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("expected --%s", joinNames(names))
}

func (cmd command) withPositionals(positionals []string) command {
	cmd.Positionals = positionals
	return cmd
}

func printHelp(stdout io.Writer) {
	_, _ = fmt.Fprint(stdout, `Craken CLI

Usage:
  craken auth login --profile ak
  craken auth import-token --profile ak --token -
  craken commands --profile ak --format text
  craken do workspaces.list --profile ak
  craken get /api/workspaces --profile ak

Command discovery:
  craken commands --profile ak --format text
      List the server-owned /api/client catalog available to your bearer session.
  craken do OPERATION_ID [options]
      Invoke a catalog operation by id.
  craken get|post|put|patch|delete PATH [options]
      Call an API path directly.

Dedicated shortcuts:
  workspace, channel, dm, file, folder, wiki, agent, and dream wrap common stable flows.
  Use craken commands for the authoritative operation list and capability-filtered sysop routes.

Global options:
  --profile NAME           Credential profile. Defaults to CRAKEN_PROFILE or default.
  --token TOKEN            Bearer token override. Defaults to CRAKEN_TOKEN or the selected profile.
  --bearer-token TOKEN     Explicit bearer override when a command has its own --token.
  --base-url URL           Defaults to CRAKEN_BASE_URL or https://craken.borca.ai.
  --log-file PATH          Optional HTTP step log. No log file is created by default.

Resources:
  commands|catalog
  do OPERATION_ID
  get|post|put|patch|delete PATH
  auth login|import-token
  workspace|channel|dm|file|folder|wiki|agent|dream
  api METHOD PATH

Generic request options:
  --json JSON              JSON request body. Use - to read from stdin.
  --json-file PATH         JSON request body file.
  --format json|ndjson|text|raw|none
  --accept MIME            Override the HTTP Accept header.
  --save-token-profile NAME
`)
}
