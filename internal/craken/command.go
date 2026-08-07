package craken

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	defaultBaseURL = "https://craken.corca.ai"
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
	logger, err := newLogger(cmd.string("log-file", ""))
	if err != nil {
		return err
	}
	defer logger.close()

	// `--version` is parsed as a flag (no following value), while `version` and
	// `-version` arrive as the resource; handle all three before the help path so
	// the version never falls through to the catalog fetch.
	if cmd.Flags["version"] || cmd.Resource == "version" || cmd.Resource == "-version" {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}
	if cmd.Help || cmd.Resource == "" || cmd.Resource == "help" {
		return runHelp(ctx, cmd, logger, stdout)
	}

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
		// For raw verbs the action slot is the path. A bare verb leaves Action ==
		// "", and `craken get --help` leaves the "help" sentinel; drop the sentinel
		// so the "expected API path" guard fires instead of requesting /help.
		path := cmd.Action
		if path == "help" {
			path = ""
		}
		return runRawHTTP(ctx, client, cmd.Resource, path, cmd, stdout, stdin)
	case "api":
		// Likewise, the action slot is the HTTP method here; reject a missing or
		// help-sentinel method rather than sending a bogus "HELP" request.
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
	// A bare resource leaves Action == "" so the catalog can resolve the
	// resource's declared default action; only the explicit help flag forces the
	// "help" sentinel here. The catalog dispatch (runCatalogCommand) and the raw
	// verb / api / do guards all already treat Action == "" correctly.
	if cmd.Action == "" && cmd.Help {
		cmd.Action = "help"
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

func runHelp(ctx context.Context, cmd command, logger *logger, stdout io.Writer) error {
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
	// The default overview stays compact and state-aware; the full reference and
	// the machine-readable guidance are opt-in so neither buries the other.
	if cmd.string("format", "") == "json" || boolOption(cmd, "json") {
		return printCatalogHelpJSON(stdout, catalog)
	}
	if boolOption(cmd, "verbose") || boolOption(cmd, "all") {
		return printVerboseCatalogHelp(stdout, catalog)
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
	return findInSlice(commands, func(c cliCommand) bool { return c.OperationID == operationID })
}

func routeByID(routes []route, id string) *route {
	if id == "" {
		return nil
	}
	return findInSlice(routes, func(r route) bool { return r.ID == id })
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

// printCatalogHelp renders the default, compact, state-aware overview: what
// Craken is, the current auth status, the server-recommended next steps, a
// grouped command summary, and pointers to the fuller views. The server owns all
// the state-specific content (auth/nextSteps/help); the CLI only lays it out.
func printCatalogHelp(stdout io.Writer, catalog clientCatalog) error {
	title := "Craken"
	summary := ""
	if catalog.Help != nil {
		if trim(catalog.Help.Title) != "" {
			title = catalog.Help.Title
		}
		summary = catalog.Help.Summary
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", title); err != nil {
		return err
	}
	if trim(summary) != "" {
		if _, err := fmt.Fprintf(stdout, "%s\n", summary); err != nil {
			return err
		}
	}

	if line := authStatusLine(catalog.Auth); line != "" {
		if _, err := fmt.Fprintf(stdout, "\n%s\n", line); err != nil {
			return err
		}
	}

	if err := printNextSteps(stdout, catalog); err != nil {
		return err
	}

	if summaries := groupedCommandSummaries(catalog.Commands); len(summaries) > 0 {
		if _, err := fmt.Fprint(stdout, "\nServer commands:\n"); err != nil {
			return err
		}
		for _, line := range summaries {
			if _, err := fmt.Fprintf(stdout, "  %s\n", line); err != nil {
				return err
			}
		}
	}

	_, err := fmt.Fprint(stdout, `
More:
  craken commands            List every command available to this profile.
  craken <command> --help    Show one command's options and parameters.
  craken help --verbose      Full reference: every command, route, and local flag.
  craken help --format json  Structured guidance (auth, nextSteps) for coding agents.
`)
	return err
}

// authStatusLine turns the server-provided auth block into one human line. An
// absent block (older server) yields no line so the overview degrades cleanly.
func authStatusLine(auth *catalogAuth) string {
	if auth == nil {
		return ""
	}
	switch auth.Status {
	case "user":
		if auth.Identity != nil && trim(auth.Identity.Email) != "" {
			return fmt.Sprintf("Logged in as %s.", auth.Identity.Email)
		}
		return "Logged in."
	case "agent":
		workspace := ""
		if auth.Agent != nil {
			workspace = auth.Agent.WorkspaceID
		}
		owner := ""
		if auth.Identity != nil {
			owner = auth.Identity.Email
		}
		switch {
		case workspace != "" && owner != "":
			return fmt.Sprintf("Acting as a delegated agent in workspace %s (owner %s).", workspace, owner)
		case workspace != "":
			return fmt.Sprintf("Acting as a delegated agent in workspace %s.", workspace)
		default:
			return "Acting as a delegated agent."
		}
	default:
		return "Not logged in. Run 'craken auth login' to get started."
	}
}

// printNextSteps renders the server-recommended actions. When the catalog omits
// them (older server), it falls back to a single sensible hint derived from the
// auth status so the user is never left without a next action.
func printNextSteps(stdout io.Writer, catalog clientCatalog) error {
	steps := catalog.NextSteps
	if len(steps) == 0 {
		steps = fallbackNextSteps(catalog.Auth)
	}
	if len(steps) == 0 {
		return nil
	}
	if _, err := fmt.Fprint(stdout, "\nNext steps:\n"); err != nil {
		return err
	}
	for index, step := range steps {
		if _, err := fmt.Fprintf(stdout, "  %d. %s\n", index+1, step.Command); err != nil {
			return err
		}
		if trim(step.Description) != "" {
			if _, err := fmt.Fprintf(stdout, "       %s\n", step.Description); err != nil {
				return err
			}
		}
	}
	return nil
}

func fallbackNextSteps(auth *catalogAuth) []catalogNextStep {
	if auth != nil && (auth.Status == "user" || auth.Status == "agent") {
		return []catalogNextStep{{Command: "craken workspace list", Description: "List the workspaces you can act in."}}
	}
	return []catalogNextStep{{Command: "craken auth login", Description: "Log in through your browser to get started."}}
}

// groupedCommandSummaries collapses each command group to a single
// "Group: action1, action2" line for the compact overview.
func groupedCommandSummaries(commands []cliCommand) []string {
	lines := []string{}
	for _, group := range groupedCommands(commands) {
		actions := []string{}
		for _, command := range group.Commands {
			if _, action, found := strings.Cut(command.ID, "."); found && action != "" {
				actions = append(actions, action)
			}
		}
		if len(actions) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", group.Name, strings.Join(actions, ", ")))
	}
	return lines
}

// printCatalogHelpJSON emits the state-aware guidance as JSON for coding agents:
// the identity (auth) and the recommended next steps, plus the one-line summary.
func printCatalogHelpJSON(stdout io.Writer, catalog clientCatalog) error {
	out := map[string]any{"nextSteps": nextStepsForJSON(catalog)}
	if catalog.Auth != nil {
		out["auth"] = catalog.Auth
	}
	if catalog.Help != nil {
		if trim(catalog.Help.Title) != "" {
			out["title"] = catalog.Help.Title
		}
		if trim(catalog.Help.Summary) != "" {
			out["summary"] = catalog.Help.Summary
		}
	}
	return printJSON(stdout, out)
}

func nextStepsForJSON(catalog clientCatalog) []catalogNextStep {
	if len(catalog.NextSteps) > 0 {
		return catalog.NextSteps
	}
	return fallbackNextSteps(catalog.Auth)
}

// printVerboseCatalogHelp is the full reference: the complete server help, the
// local bootstrap commands and flags, every server command with its description
// and examples, and every advertised operation route.
func printVerboseCatalogHelp(stdout io.Writer, catalog clientCatalog) error {
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
  --base-url URL           Defaults to CRAKEN_BASE_URL or https://craken.corca.ai.
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
