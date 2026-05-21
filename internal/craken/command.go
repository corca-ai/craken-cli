package craken

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
		return runHelp(ctx, cmd, stdout)
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
		if cmd.Help {
			cmd.Action = "help"
			return cmd, nil
		}
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

func runHelp(ctx context.Context, cmd command, stdout io.Writer) error {
	logger, err := newLogger(cmd.string("log-file", ""))
	if err != nil {
		return err
	}
	defer logger.close()

	client, err := newCatalogClient(cmd, logger)
	if err != nil {
		return err
	}
	value, err := client.json(ctx, "GET", "/api/client", nil)
	if err != nil {
		return err
	}
	catalog, err := catalogFromValue(value)
	if err != nil {
		return err
	}
	if command, route := focusedHelpTarget(cmd, catalog); command != nil {
		return printFocusedCommandHelp(stdout, *command, route)
	}
	return printCatalogHelp(stdout, catalog)
}

func focusedHelpTarget(cmd command, catalog clientCatalog) (*cliCommand, *route) {
	if cmd.Resource == "" || cmd.Action == "" || cmd.Action == "help" {
		return nil, nil
	}
	id := cmd.Resource + "." + cmd.Action
	for i := range catalog.Commands {
		if catalog.Commands[i].ID == id {
			return &catalog.Commands[i], routeByID(catalog.Routes, catalog.Commands[i].OperationID)
		}
	}
	return nil, nil
}

func routeByID(routes []route, id string) *route {
	if id == "" {
		return nil
	}
	for i := range routes {
		if routes[i].ID == id {
			return &routes[i]
		}
	}
	return nil
}

func printFocusedCommandHelp(stdout io.Writer, command cliCommand, route *route) error {
	if _, err := fmt.Fprintf(stdout, "Usage:\n  %s\n\n%s\n", command.Command, command.Description); err != nil {
		return err
	}
	if len(command.Examples) > 0 {
		if _, err := fmt.Fprint(stdout, "\nExamples:\n"); err != nil {
			return err
		}
		for _, example := range command.Examples {
			if _, err := fmt.Fprintf(stdout, "  e.g. %s\n", example); err != nil {
				return err
			}
		}
	}
	if options := localCommandHelpOptions(command.ID); len(options) > 0 {
		if _, err := fmt.Fprint(stdout, "\nOptions:\n"); err != nil {
			return err
		}
		for _, option := range options {
			if _, err := fmt.Fprintf(stdout, "  %s\n", option); err != nil {
				return err
			}
		}
	}
	if route == nil {
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "\nOperation:\n  %s\t%s\t%s\n", route.ID, route.Method, route.Path); err != nil {
		return err
	}
	if route.Description != "" && route.Description != command.Description {
		if _, err := fmt.Fprintf(stdout, "  %s\n", route.Description); err != nil {
			return err
		}
	}
	for _, group := range []struct {
		title  string
		fields []catalogField
	}{
		{title: "Path parameters", fields: route.PathParameters},
		{title: "Query parameters", fields: route.QueryParameters},
		{title: "Body fields", fields: route.BodyFields},
	} {
		if err := printCatalogFieldGroup(stdout, group.title, group.fields); err != nil {
			return err
		}
	}
	if route.ResponseExample != nil {
		if err := printResponseExample(stdout, route.ResponseExample); err != nil {
			return err
		}
	}
	return nil
}

func printCatalogFieldGroup(stdout io.Writer, title string, fields []catalogField) error {
	if len(fields) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "\n%s:\n", title); err != nil {
		return err
	}
	for _, field := range fields {
		detail := fieldDetail(field)
		if detail == "" {
			if _, err := fmt.Fprintf(stdout, "  --%s\n", kebabCase(field.Name)); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(stdout, "  --%s  %s\n", kebabCase(field.Name), detail); err != nil {
			return err
		}
	}
	return nil
}

func fieldDetail(field catalogField) string {
	parts := []string{}
	if field.Type != "" {
		parts = append(parts, field.Type)
	}
	if len(field.Values) > 0 {
		parts = append(parts, strings.Join(field.Values, "|"))
	}
	if field.Required {
		parts = append(parts, "required")
	}
	if field.Description != "" {
		parts = append(parts, field.Description)
	}
	return strings.Join(parts, "; ")
}

func printResponseExample(stdout io.Writer, value any) error {
	bytes, err := json.MarshalIndent(value, "  ", "\t")
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(string(bytes), "\n", "\n  ")
	_, err = fmt.Fprintf(stdout, "\nResponse example:\n  %s\n", text)
	return err
}

func localCommandHelpOptions(commandID string) []string {
	switch commandID {
	case "channel.messages", "dm.messages", "dm.list":
		return []string{
			"--position latest|start   Read the latest page or the beginning of the conversation.",
			"--before MESSAGE_ID       Read older messages before a response oldestCursor.",
			"--after MESSAGE_ID        Read newer messages after a response newestCursor.",
			"--around MESSAGE_ID       Read a page ending at a known message id.",
			"--limit N                 Request a smaller message page from the service.",
			"--compact                 Print createdAt, sender, and body as tab-separated text.",
			"--fields LIST             Print JSON projected to comma-separated dotted fields.",
		}
	case "channel.wait":
		return []string{
			"--after MESSAGE_ID        Wait after a previous message id or newestCursor. Omit to wait after the current newest message.",
			"--timeout-ms MS          Server-side wait timeout. Defaults to 30000 and caps at 60000.",
		}
	case "wiki.recent":
		return []string{
			"--limit N                 Limit recent wiki changes.",
			"--compact                 Print createdAt, author, page, and version as tab-separated text.",
			"--fields LIST             Print JSON projected to comma-separated dotted fields.",
		}
	case "wiki.versions", "wiki.version":
		return []string{
			"--compact                 Print createdAt, author, and version as tab-separated text.",
			"--fields LIST             Print JSON projected to comma-separated dotted fields.",
		}
	case "workspace.activity":
		return []string{
			"--anchor-json JSON        Activity anchor JSON.",
			"--before-sequence N       Read activity before a sequence cursor.",
			"--limit N                 Limit returned activity rows.",
			"--surfaces LIST           Comma-separated activity surfaces.",
		}
	default:
		return nil
	}
}

func printCatalogHelp(stdout io.Writer, catalog clientCatalog) error {
	if _, err := fmt.Fprint(stdout, `Craken CLI

Authentication:
  craken auth login
  craken auth import-token --token -

Catalog commands:
  craken commands --format text
  craken do OPERATION_ID [options]
  craken get|post|put|patch|delete PATH [options]

Global options:
  --profile NAME           Credential profile. Defaults to CRAKEN_PROFILE or default.
  --token TOKEN            Bearer token override. Defaults to CRAKEN_TOKEN or the selected profile.
  --bearer-token TOKEN     Explicit bearer override when a command has its own --token.
  --base-url URL           Defaults to CRAKEN_BASE_URL or https://craken.borca.ai.
  --log-file PATH          Optional HTTP step log. No log file is created by default.

Generic request options:
  --json JSON              JSON request body. Use - to read from stdin.
  --json-file PATH         JSON request body file.
  --format json|ndjson|text|raw|none
  --accept MIME            Override the HTTP Accept header.
  --save-token-profile NAME
`); err != nil {
		return err
	}

	if len(catalog.Commands) > 0 {
		if _, err := fmt.Fprint(stdout, "\nServer commands:\n"); err != nil {
			return err
		}
		for _, group := range groupedCommands(catalog.Commands) {
			if _, err := fmt.Fprintf(stdout, "%s:\n", group.Name); err != nil {
				return err
			}
			for _, command := range group.Commands {
				if _, err := fmt.Fprintf(stdout, "  %s\n      %s\n", command.Command, command.Description); err != nil {
					return err
				}
				for _, example := range command.Examples {
					if _, err := fmt.Fprintf(stdout, "      e.g. %s\n", example); err != nil {
						return err
					}
				}
			}
		}
	} else {
		if len(catalog.Examples) > 0 {
			if _, err := fmt.Fprint(stdout, "\nServer examples:\n"); err != nil {
				return err
			}
			for _, example := range catalog.Examples {
				if _, err := fmt.Fprintf(stdout, "  %s\n      %s\n", example.Command, example.Description); err != nil {
					return err
				}
			}
		}

		if len(catalog.Shortcuts) > 0 {
			if _, err := fmt.Fprint(stdout, "\nServer-advertised shortcuts:\n"); err != nil {
				return err
			}
			for _, shortcut := range catalog.Shortcuts {
				if _, err := fmt.Fprintf(stdout, "  %s %s\n      %s\n", shortcut.Resource, strings.Join(shortcut.Actions, "|"), shortcut.Description); err != nil {
					return err
				}
			}
		}
	}

	if len(catalog.Routes) > 0 {
		if _, err := fmt.Fprint(stdout, "\nServer operations:\n"); err != nil {
			return err
		}
		for _, route := range catalog.Routes {
			if _, err := fmt.Fprintf(stdout, "  %s\t%s\t%s\t%s\n", route.ID, route.Method, route.Path, route.Description); err != nil {
				return err
			}
		}
	}

	return nil
}

type commandGroup struct {
	Name     string
	Commands []cliCommand
}

func groupedCommands(commands []cliCommand) []commandGroup {
	groups := []commandGroup{}
	indexes := map[string]int{}
	for _, command := range commands {
		name := command.Group
		if name == "" {
			name = "Commands"
		}
		index, ok := indexes[name]
		if !ok {
			index = len(groups)
			indexes[name] = index
			groups = append(groups, commandGroup{Name: name})
		}
		groups[index].Commands = append(groups[index].Commands, command)
	}
	return groups
}

func printCommandsText(stdout io.Writer, commands []cliCommand) error {
	for _, command := range commands {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", command.Group, command.ID, command.Command, command.Description); err != nil {
			return err
		}
	}
	return nil
}
