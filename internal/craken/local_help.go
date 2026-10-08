package craken

import (
	"fmt"
	"io"
	"strings"
)

const localProfileHelp = `
Connection options:
  --profile NAME       Selected credential profile and login destination (CRAKEN_PROFILE or default).
  --base-url URL       Server override; otherwise saved profile, CRAKEN_BASE_URL, or https://craken.corca.ai.
  --log-file PATH      Optional HTTP diagnostic log.
`

const loginHelp = `Usage:
  craken auth login [options]
  craken auth login --as-agent --workspace WORKSPACE --agent-name NAME --client-kind KIND --profile AGENT_PROFILE

User login opens the browser/device approval flow. Agent login first uses an
available user credential to mint a delegated session, falling back to browser
approval when supported. Login stores the credential in --profile, not
--save-token-profile. A user/agent identity replacement requires --force;
use a separate profile to keep both identities. Confirm with auth whoami.

Login options:
  --as-agent                Log in as a delegated workspace agent.
  --workspace WORKSPACE     Agent workspace id or slug (--workspace-id is an alias).
  --agent-name NAME         External agent participant name.
  --client-kind KIND        Agent client kind (default custom; for example codex or claude_code).
  --client-label LABEL      Optional description of this agent client.
  --scopes LIST             Optional comma-separated delegation scopes.
  --use-user-profile NAME   User credential for direct agent authorization.
  --device-code             Force agent browser/device approval.
  --no-device-fallback      Do not fall back after an unsupported direct authorization endpoint.
  --no-open                 Print the verification URL without opening a browser.
  --timeout-ms MS           Bound login polling (default 300000).
  --force                  Deliberately replace a user/agent identity in the destination profile.
`

func localHelpText(cmd command) string {
	if cmd.Resource == "auth" {
		switch cmd.Action {
		case "login":
			return loginHelp + localProfileHelp
		case "whoami", "status":
			return "Usage:\n  craken auth whoami [connection options]\n\nReports the selected profile, effective server, token identity and delegated workspace/scopes. Local token claims are informational; server confirmation or its failure is reported separately.\n" + localProfileHelp
		case "import-token":
			return "Usage:\n  craken auth import-token --token TOKEN [connection options]\n\nUse --token - to read the bearer from stdin. Stores it in the selected profile.\n" + localProfileHelp
		case "", "help":
			return "Usage:\n  craken auth login|whoami|status|import-token [options]\n\nRun craken auth login --help for browser/device and agent authorization options.\n" + localProfileHelp
		}
	}
	switch cmd.Resource {
	case "commands", "catalog":
		return "Usage:\n  craken commands [--format json|text]\n\nFetch the live server-owned command catalog for the selected bearer. Product commands and permission guidance require a reachable server.\n" + localProfileHelp
	case "get", "post", "put", "patch", "delete", "api":
		return "Usage:\n  craken get|post|put|patch|delete PATH [options]\n  craken api METHOD PATH [options]\n\nRaw HTTP requests use the selected bearer and server.\n" + genericHelpOptions + localProfileHelp
	case "do":
		if cmd.Action == "" || cmd.Action == "help" {
			return "Usage:\n  craken do OPERATION_ID [options]\n\nResolve the operation from the live server catalog. Use craken do OPERATION_ID --help for its path/query/body schema.\n" + genericHelpOptions + localProfileHelp
		}
	}
	return ""
}

const genericHelpOptions = `
Request options:
  --token TOKEN            Bearer override (CRAKEN_TOKEN or selected profile otherwise).
  --bearer-token TOKEN     Explicit bearer override when a command takes its own token argument.
  --json JSON              JSON body; use - for stdin.
  --json-file PATH         Read a JSON body file.
  --format FORMAT          json|ndjson|text|raw|none.
  --accept MIME            HTTP response negotiation, independent of local --format.
  --header "Name: value"   Repeatable HTTP header; use profile/token for credentials.
  --query "name=value"     Repeatable raw query parameter.
  --http-timeout DURATION  Transport deadline (default 120s).
  --response-meta FILE     Response status/headers as JSON; stdout remains the body.
  --error-format json      Structured diagnostics on stderr.
  --save-token-profile NAME Store a token returned by a generic JSON request.
`

func printHelpFallback(stdout, stderr io.Writer, cmd command, client *client, cause error) error {
	if _, err := fmt.Fprintf(stderr, "warning: live catalog unavailable: %v\n", cause); err != nil {
		return err
	}
	connection := map[string]any{"profile": profileName(cmd)}
	server := "unavailable (profile configuration could not be read)"
	if client != nil {
		connection["baseUrl"] = client.baseURL
		server = client.baseURL
	}
	steps := []catalogNextStep{{Command: "craken auth whoami", Description: "Inspect the selected identity; server confirmation may be unavailable."}, {Command: "craken auth login --help", Description: "Show local login options without network access."}}
	if cmd.string("format", "") == "json" || boolOption(cmd, "json") {
		return printJSON(stdout, map[string]any{"source": "local-fallback", "connection": connection, "nextSteps": steps, "requestedCommand": strings.TrimSpace(cmd.Resource + " " + cmd.Action), "summary": "Live command schemas and current authentication/permissions could not be confirmed. No cached session guidance is used."})
	}
	_, err := fmt.Fprintf(stdout, "Local fallback help\n\nLive command schemas and authentication/permissions could not be confirmed.\nNo cached session guidance is used.\nProfile: %s\nServer: %s\n\nRequested: craken %s %s\n\nLocal commands:\n  craken auth login --help\n  craken auth whoami\n  craken auth import-token --help\n  craken do --help\n  craken get --help\n\nRetry the requested help when the server is reachable.\n%s", profileName(cmd), server, cmd.Resource, cmd.Action, localProfileHelp)
	return err
}

func printHelpConnection(stdout io.Writer, cmd command, client *client, auth *catalogAuth) error {
	if boolOption(cmd, "verbose") || boolOption(cmd, "all") {
		if _, err := fmt.Fprintf(stdout, "Profile: %s\nServer: %s\n", profileName(cmd), client.baseURL); err != nil {
			return err
		}
	}
	if line := authStatusLine(auth); line != "" {
		_, err := fmt.Fprintln(stdout, line)
		return err
	}
	return nil
}

func printResourceHelp(stdout io.Writer, cmd command, catalog clientCatalog) (bool, error) {
	if cmd.Resource == "" || cmd.Resource == "help" || (cmd.Action != "" && cmd.Action != "help") {
		return false, nil
	}
	commands := []cliCommand{}
	for _, command := range catalog.Commands {
		if strings.HasPrefix(command.ID, cmd.Resource+".") {
			commands = append(commands, command)
		}
	}
	if len(commands) == 0 {
		return false, nil
	}
	if _, err := fmt.Fprintf(stdout, "Usage:\n  craken %s ACTION [options]\n\n", cmd.Resource); err != nil {
		return true, err
	}
	for _, command := range commands {
		if _, err := fmt.Fprintf(stdout, "  %s\n    %s\n", command.Command, command.Description); err != nil {
			return true, err
		}
	}
	_, err := fmt.Fprintf(stdout, "\nUse craken %s ACTION --help for effective options.\n%s", cmd.Resource, localProfileHelp)
	return true, err
}
