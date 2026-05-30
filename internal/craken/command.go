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
	// `--version` is parsed as a flag (no following value), while `version` and
	// `-version` arrive as the resource; handle all three before the help path so
	// the version never falls through to the catalog fetch.
	if cmd.Flags["version"] || cmd.Resource == "version" || cmd.Resource == "-version" {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}
	if cmd.Help || cmd.Resource == "" || cmd.Resource == "help" {
		return runHelp(ctx, cmd, stdout)
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
		// parseCommand defaults a missing action to the "help" sentinel; for raw
		// verbs the action slot is the path, so drop the sentinel and let the
		// "expected API path" guard fire instead of requesting /help.
		path := cmd.Action
		if path == "help" {
			path = ""
		}
		return runRawHTTP(ctx, client, cmd.Resource, path, cmd, stdout, stdin)
	case "api":
		// Likewise, the action slot is the HTTP method here; reject the missing /
		// sentinel method rather than sending a bogus "HELP" request.
		method := cmd.Action
		if method == "" || method == "help" {
			return fmt.Errorf("expected HTTP method, e.g. craken api GET /path")
		}
		return runRawHTTP(ctx, client, method, first(cmd.Positionals, cmd.string("path", "")), cmd.withPositionals(rest(cmd.Positionals)), stdout, stdin)
	default:
		return runCatalogCommand(ctx, client, cmd, stdout, stdin)
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
	value, err := client.json(ctx, "/api/client")
	if err != nil {
		return err
	}
	catalog, err := catalogFromValue(value)
	if err != nil {
		return err
	}
	if command, route := focusedHelpTarget(cmd, catalog); command != nil || route != nil {
		return printFocusedCommandHelp(stdout, cmd, command, route)
	}
	return printCatalogHelp(stdout, catalog)
}

func focusedHelpTarget(cmd command, catalog clientCatalog) (*cliCommand, *route) {
	if cmd.Action == "" || cmd.Action == "help" {
		return nil, nil
	}
	// `craken do <operationId> --help` addresses a route by its operation id; the
	// shortcut command (if any) enriches the help with examples and options.
	if cmd.Resource == "do" {
		route := routeByID(catalog.Routes, cmd.Action)
		if route == nil {
			return nil, nil
		}
		return commandByOperationID(catalog.Commands, cmd.Action), route
	}
	if cmd.Resource == "" {
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

func commandByOperationID(commands []cliCommand, operationID string) *cliCommand {
	for i := range commands {
		if commands[i].OperationID == operationID {
			return &commands[i]
		}
	}
	return nil
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

func printFocusedCommandHelp(stdout io.Writer, cmd command, command *cliCommand, route *route) error {
	usage := focusedHelpUsage(cmd, command, route)
	description := focusedHelpDescription(command, route)
	if _, err := fmt.Fprintf(stdout, "Usage:\n  %s\n\n%s\n", usage, description); err != nil {
		return err
	}
	if command != nil && len(command.Examples) > 0 {
		if _, err := fmt.Fprint(stdout, "\nExamples:\n"); err != nil {
			return err
		}
		for _, example := range command.Examples {
			if _, err := fmt.Fprintf(stdout, "  e.g. %s\n", example); err != nil {
				return err
			}
		}
	}
	if command != nil {
		if options := localCommandHelpOptions(*command); len(options) > 0 {
			if _, err := fmt.Fprint(stdout, "\nOptions:\n"); err != nil {
				return err
			}
			for _, option := range options {
				if _, err := fmt.Fprintf(stdout, "  %s\n", option); err != nil {
					return err
				}
			}
		}
	}
	if route == nil {
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "\nOperation:\n  %s\t%s\t%s\n", route.ID, route.Method, route.Path); err != nil {
		return err
	}
	if route.Description != "" && (command == nil || route.Description != command.Description) {
		if _, err := fmt.Fprintf(stdout, "  %s\n", route.Description); err != nil {
			return err
		}
	}
	for _, group := range []struct {
		title  string
		fields []catalogField
	}{
		{title: "Path parameters", fields: firstCatalogFields(route.PathParams, route.PathParameters)},
		{title: "Query parameters", fields: firstCatalogFields(route.QueryParams, route.QueryParameters)},
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

func focusedHelpUsage(cmd command, command *cliCommand, route *route) string {
	if command != nil && command.Command != "" {
		return command.Command
	}
	if route != nil {
		return fmt.Sprintf("craken do %s [options]", route.ID)
	}
	return fmt.Sprintf("craken %s %s", cmd.Resource, cmd.Action)
}

func focusedHelpDescription(command *cliCommand, route *route) string {
	if command != nil && command.Description != "" {
		return command.Description
	}
	if route != nil {
		return route.Description
	}
	return ""
}

func firstCatalogFields(primary []catalogField, fallback []catalogField) []catalogField {
	if len(primary) > 0 {
		return primary
	}
	return fallback
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
	if field.Resolver != nil {
		label := field.Resolver.Label
		if label == "" {
			label = "name"
		}
		parts = append(parts, fmt.Sprintf("accepts a %s name or id", label))
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

func localCommandHelpOptions(command cliCommand) []string {
	options := []string{}
	options = append(options, bindingHelpOptions(command.Execution.QueryParams)...)
	if command.Execution.Output != nil {
		options = append(options, "--fields LIST             Print JSON projected to comma-separated dotted fields.")
		if command.Execution.Output.Mode == commandOutputModeTable {
			options = append(options, "--compact                 Print the catalog table columns as tab-separated text.")
		}
	}
	if command.Execution.Poll != nil {
		options = append(
			options,
			fmt.Sprintf("--%s N              Poll interval in seconds.", command.Execution.Poll.IntervalOption),
			fmt.Sprintf("--%s N             Maximum poll attempts.", command.Execution.Poll.MaxPollsOption),
		)
	}
	if command.Execution.Transport == commandTransportWebSocket {
		options = append(
			options,
			"--limit N                 Stop after receiving N WebSocket messages.",
			"--timeout-ms MS          Stop when no WebSocket message arrives before the timeout.",
			"--pretty                 Pretty-print JSON WebSocket messages.",
		)
	}
	return options
}

func bindingHelpOptions(bindings map[string]commandBinding) []string {
	options := []string{}
	for _, binding := range bindings {
		if binding.Source != commandBindingSourceOption || binding.Option == "" {
			continue
		}
		value := "VALUE"
		if binding.Type == commandBindingValueTypeInteger {
			value = "N"
		}
		if binding.Type == commandBindingValueTypeJSON {
			value = "JSON"
		}
		options = append(options, fmt.Sprintf("--%s %s", binding.Option, value))
	}
	return options
}

func printCatalogHelp(stdout io.Writer, catalog clientCatalog) error {
	if catalog.Help != nil {
		if err := printServerHelp(stdout, *catalog.Help); err != nil {
			return err
		}
	} else if _, err := fmt.Fprint(stdout, "Client catalog\n"); err != nil {
		return err
	}

	if _, err := fmt.Fprint(stdout, `
Local client commands:
  craken auth login
  craken auth import-token --token -
  craken commands --format text
  craken do OPERATION_ID [options]
  craken get|post|put|patch|delete PATH [options]

Local options:
  --profile NAME           Credential profile. Defaults to CRAKEN_PROFILE or default.
  --token TOKEN            Bearer token override. Defaults to CRAKEN_TOKEN or the selected profile.
  --bearer-token TOKEN     Explicit bearer override when a command has its own --token.
  --base-url URL           Defaults to CRAKEN_BASE_URL or https://craken.borca.ai.
  --log-file PATH          Optional HTTP step log. No log file is created by default.

Auth login options:
  --as-agent               Mint a delegated-agent session instead of a user session.
  --force                  Replace a profile that already holds a different identity.

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

func printServerHelp(stdout io.Writer, help catalogHelp) error {
	if _, err := fmt.Fprintf(stdout, "%s\n", help.Title); err != nil {
		return err
	}
	if trim(help.Summary) != "" {
		if _, err := fmt.Fprintf(stdout, "\n%s\n", help.Summary); err != nil {
			return err
		}
	}
	for _, section := range help.Sections {
		if _, err := fmt.Fprintf(stdout, "\n%s:\n", section.Title); err != nil {
			return err
		}
		if trim(section.Body) != "" {
			if _, err := fmt.Fprintf(stdout, "  %s\n", section.Body); err != nil {
				return err
			}
		}
		for _, item := range section.Items {
			if err := printServerHelpItem(stdout, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func printServerHelpItem(stdout io.Writer, item catalogHelpItem) error {
	lead := firstNonEmpty(item.Command, item.Label)
	if lead == "" {
		_, err := fmt.Fprintf(stdout, "  %s\n", item.Description)
		return err
	}
	_, err := fmt.Fprintf(stdout, "  %s\n      %s\n", lead, item.Description)
	return err
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
