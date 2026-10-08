package craken

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var localBooleanOptions = map[string]bool{"compact": true, "pretty": true, "verbose": true, "all": true, "version": true, "as-agent": true, "no-browser": true}
var localOptions = map[string]bool{
	"help": true, "accept": true, "base-url": true, "body-json": true, "json": true, "json-file": true, "format": true, "log-file": true, "profile": true, "save-token-profile": true, "token": true, "bearer-token": true, "compact": true, "fields": true, "output": true, "pretty": true, "verbose": true, "all": true,
	"header": true, "query": true, "http-timeout": true, "response-meta": true, "error-format": true,
}

func validateLocalOptions(cmd command) error {
	jsonSources := 0
	for _, name := range []string{"json", "body-json", "json-file"} {
		if _, exists := cmd.Options[name]; exists {
			jsonSources++
		}
	}
	if jsonSources > 1 {
		return fmt.Errorf("use only one of --json, --body-json or --json-file")
	}
	for name := range cmd.Flags {
		if localOptions[name] && !localBooleanOptions[name] && (!cmd.Help || name != "json") {
			return fmt.Errorf("expected value for --%s", name)
		}
	}
	switch format := cmd.string("format", ""); format {
	case "", "json", "ndjson", "text", "raw", "none":
	default:
		return fmt.Errorf("unknown output format: %s", format)
	}
	if v := cmd.string("error-format", ""); v != "" && v != "json" && v != "text" {
		return fmt.Errorf("unknown error format: %s", v)
	}
	if boolOption(cmd, "compact") && cmd.string("fields", "") != "" {
		return fmt.Errorf("use either --compact or --fields, not both")
	}
	if fields := cmd.string("fields", ""); fields != "" {
		for _, field := range strings.Split(fields, ",") {
			for _, part := range strings.Split(field, ".") {
				if strings.TrimSpace(part) == "" {
					return fmt.Errorf("invalid field path: %s", field)
				}
			}
		}
	}
	if cmd.string("fields", "") != "" && (cmd.string("format", "") == "text" || cmd.string("format", "") == "raw") {
		return fmt.Errorf("--fields requires JSON or NDJSON output")
	}
	if value, ok := optionValue(cmd, "http-timeout"); ok {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return fmt.Errorf("--http-timeout must be a positive duration, e.g. 30s")
		}
	}
	for name, values := range cmd.Values {
		if len(values) > 1 && localOptions[name] && name != "header" && name != "query" {
			return fmt.Errorf("repeated scalar option --%s", name)
		}
	}
	for _, value := range cmd.Values["header"] {
		name, _, ok := strings.Cut(value, ":")
		if !ok || strings.TrimSpace(name) == "" || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("--header expects Name: value")
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authorization", "cookie", "proxy-authorization", "host":
			return fmt.Errorf("use profile/token credentials; --header %s is not supported", name)
		}
	}
	for _, value := range cmd.Values["query"] {
		name, _, ok := strings.Cut(value, "=")
		if !ok || name == "" {
			return fmt.Errorf("--query expects name=value")
		}
	}
	return nil
}

func printFocusedHelpJSON(stdout io.Writer, value any, cmd command, selectedCommand *cliCommand, selectedRoute *route) error {
	root, _ := value.(map[string]any)
	out := map[string]any{"schemaVersion": root["schemaVersion"]}
	if selectedRoute != nil {
		for _, entry := range rawEntries(root["routes"]) {
			if entry["id"] == selectedRoute.ID {
				out["route"] = entry
				break
			}
		}
	}
	related := []any{}
	for _, entry := range rawEntries(root["commands"]) {
		if (selectedCommand != nil && entry["id"] == selectedCommand.ID) || (selectedRoute != nil && entry["operationId"] == selectedRoute.ID) {
			related = append(related, entry)
		}
	}
	out["commands"] = related
	if selectedCommand != nil {
		out["execution"] = selectedExecution(*selectedCommand, cmd)
	} else if selectedRoute != nil && selectedRoute.Execution != nil {
		out["execution"] = selectedExecution(cliCommand{OperationID: selectedRoute.ID, Execution: *selectedRoute.Execution}, cmd)
	}
	return printJSON(stdout, out)
}
func rawEntries(value any) []map[string]any {
	out := []map[string]any{}
	entries, _ := value.([]any)
	for _, entry := range entries {
		if object, ok := entry.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func reparseCatalogCommand(cmd command, plan commandExecution, selected route) (command, error) {
	if cmd.RawArgs == nil {
		return cmd, nil
	}
	flags := map[string]bool{}
	for name, value := range localBooleanOptions {
		flags[name] = value
	}
	for _, bindings := range []map[string]commandBinding{plan.PathParams, plan.QueryParams, plan.BodyFields, plan.Multipart.Fields} {
		for _, binding := range bindings {
			if binding.Source == commandBindingSourceFlag {
				flags[binding.Option] = true
			}
		}
	}
	if plan.Transport == commandTransportWebSocket {
		for _, name := range []string{"messages", "resume", "reconnect", "once"} {
			flags[name] = true
		}
	}
	if selected.Specification != nil {
		for _, schema := range []wireSchema{selected.Specification.Wire.Query, selected.Specification.Wire.Body} {
			for name, field := range schema.Properties {
				if schemaType(field) == "boolean" {
					flags[kebabCase(name)] = true
					flags[name] = true
				}
			}
		}
	}
	parsed, err := parseCommandWithFlags(cmd.RawArgs, flags)
	if err != nil {
		return parsed, err
	}
	return normalizeArrayOptions(parsed, selected, plan)
}

// Validate execution meaning only for the selected plan; unknown catalog descriptions remain discoverable.
func validatePlan(plan commandExecution, cmd command) error {
	switch plan.Transport {
	case "", commandTransportHTTP, commandTransportDownload, commandTransportMultipart, commandTransportWebSocket:
	default:
		return fmt.Errorf("unsupported catalog command transport: %s", plan.Transport)
	}
	for _, bindings := range []map[string]commandBinding{plan.PathParams, plan.QueryParams, plan.BodyFields, plan.Multipart.Fields} {
		for _, binding := range bindings {
			switch binding.Source {
			case "", commandBindingSourceOption, commandBindingSourceText, commandBindingSourceFlag, commandBindingSourceLiteral, commandBindingSourceResolved, commandBindingSourceBearerToken:
			default:
				return fmt.Errorf("unsupported catalog binding source: %s", binding.Source)
			}
			switch binding.Positionals {
			case "", commandBindingPositionalsJoin:
			default:
				return fmt.Errorf("unsupported catalog positional binding: %s", binding.Positionals)
			}
			switch binding.Type {
			case "", "string", commandBindingValueTypeInteger, commandBindingValueTypeJSON:
			default:
				return fmt.Errorf("unsupported catalog binding type: %s", binding.Type)
			}
		}
	}
	for _, protocol := range plan.WebSocket.Protocols {
		switch protocol.Source {
		case "literal", "json-payload":
		default:
			return fmt.Errorf("unsupported websocket protocol source: %s", protocol.Source)
		}
		for _, binding := range protocol.Payload {
			switch binding.Type {
			case "", "string", commandBindingValueTypeInteger, commandBindingValueTypeJSON:
			default:
				return fmt.Errorf("unsupported websocket binding type: %s", binding.Type)
			}
			switch binding.Source {
			case commandBindingSourceLiteral, commandBindingSourceBearerToken, commandBindingSourceOption, commandBindingSourceFlag, commandBindingSourceResolved:
			default:
				return fmt.Errorf("unsupported websocket payload binding: %s", binding.Source)
			}
		}
	}
	if plan.Output != nil {
		switch plan.Output.Mode {
		case commandOutputModeJSON, commandOutputModeTable, commandOutputModeBytes:
		default:
			return fmt.Errorf("unsupported catalog command output mode: %s", plan.Output.Mode)
		}
	}
	if boolOption(cmd, "compact") && (plan.Output == nil || plan.Output.Mode != commandOutputModeTable) {
		return fmt.Errorf("--compact is not supported for this command")
	}
	if plan.Transport == commandTransportDownload && (cmd.string("fields", "") != "" || boolOption(cmd, "compact") || (cmd.string("format", "") != "" && cmd.string("format", "") != "raw" && cmd.string("format", "") != "none")) {
		return fmt.Errorf("downloads support raw/none output without projection")
	}
	return nil
}

// Explicit JSON bypasses schema-shape validation, never local execution compatibility.
func hasExplicitJSON(cmd command) bool {
	for _, name := range []string{"json", "body-json", "json-file"} {
		if _, ok := cmd.Options[name]; ok {
			return true
		}
	}
	return false
}
func jsonValue(value string) (any, error) {
	var out any
	err := json.Unmarshal([]byte(value), &out)
	return out, err
}

func httpTimeout(cmd command) time.Duration {
	duration, err := time.ParseDuration(cmd.string("http-timeout", "120s"))
	if err != nil {
		return 120 * time.Second
	}
	return duration
}
func downloadHeaders(cmd command) http.Header {
	headers := requestHeadersFromOptions(cmd)
	explicit := cmd.string("accept", "") != ""
	for _, input := range cmd.Values["header"] {
		name, _, _ := strings.Cut(input, ":")
		if strings.EqualFold(strings.TrimSpace(name), "Accept") {
			explicit = true
		}
	}
	if !explicit {
		headers.Set("Accept", "*/*")
	}
	return headers
}
