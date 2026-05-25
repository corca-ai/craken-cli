package craken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func runCatalogCommand(ctx context.Context, client *client, cmd command, stdout io.Writer, stdin io.Reader) error {
	catalogValue, err := client.json(ctx, "/api/client")
	if err != nil {
		return err
	}
	catalog, err := catalogFromValue(catalogValue)
	if err != nil {
		return err
	}
	serverCommand := catalogCommandByID(catalog.Commands, cmd.Resource+"."+cmd.Action)
	if serverCommand == nil {
		return fmt.Errorf("unknown command: %s %s", cmd.Resource, cmd.Action)
	}
	plan := selectedExecution(*serverCommand, cmd)
	if plan.OperationID == "" {
		plan.OperationID = serverCommand.OperationID
	}
	if plan.Transport == "" {
		plan.Transport = "http"
	}
	selectedRoute := routeByID(catalog.Routes, plan.OperationID)
	if selectedRoute == nil {
		return fmt.Errorf("unknown operation for %s: %s", serverCommand.ID, plan.OperationID)
	}
	requestPath, resolved, consumed, err := catalogCommandPath(ctx, client, *selectedRoute, plan, cmd)
	if err != nil {
		return err
	}
	switch plan.Transport {
	case "http":
		return runCatalogHTTPCommand(ctx, client, *selectedRoute, plan, cmd, requestPath, resolved, consumed, stdout, stdin)
	case "download":
		return runCatalogDownloadCommand(ctx, client, *selectedRoute, cmd, requestPath, stdout)
	case "multipart":
		return runCatalogMultipartCommand(ctx, client, cmd, requestPath, stdout)
	case "websocket":
		workspaceID := resolved["workspaceId"]
		if workspaceID == "" {
			return fmt.Errorf("websocket command requires workspaceId")
		}
		return tailWorkspace(ctx, client, workspaceID, cmd, stdout)
	default:
		return fmt.Errorf("unsupported catalog command transport: %s", plan.Transport)
	}
}

func catalogCommandByID(commands []cliCommand, id string) *cliCommand {
	for i := range commands {
		if commands[i].ID == id {
			return &commands[i]
		}
	}
	return nil
}

func selectedExecution(command cliCommand, cmd command) commandExecution {
	base := command.Execution
	if base.OperationID == "" {
		base.OperationID = command.OperationID
	}
	for _, variant := range command.Execution.Variants {
		if variant.When.Option != "" && cmd.string(variant.When.Option, "") == "" && !cmd.Flags[variant.When.Option] {
			continue
		}
		return mergeExecution(base, variant)
	}
	return base
}

func mergeExecution(base commandExecution, variant commandExecutionVariant) commandExecution {
	if variant.OperationID != "" {
		base.OperationID = variant.OperationID
	}
	if variant.Transport != "" {
		base.Transport = variant.Transport
	}
	if variant.Output != "" {
		base.Output = variant.Output
	}
	if variant.PathParams != nil {
		base.PathParams = variant.PathParams
	}
	if variant.QueryParams != nil {
		base.QueryParams = variant.QueryParams
	}
	if variant.BodyFields != nil {
		base.BodyFields = variant.BodyFields
	}
	return base
}

func catalogCommandPath(ctx context.Context, client *client, route route, plan commandExecution, cmd command) (string, map[string]string, map[string]bool, error) {
	consumed := map[string]bool{}
	resolved := map[string]string{}
	path := pathParamPattern.ReplaceAllStringFunc(route.Path, func(match string) string {
		name := match[1 : len(match)-1]
		binding := plan.PathParams[name]
		if binding.Source == "" {
			binding = inferredPathBinding(name)
		}
		value, err := catalogBindingString(ctx, client, cmd, binding, resolved, consumed)
		if err != nil {
			return "\x00error:" + err.Error()
		}
		if value == "" {
			return "\x00missing:" + name
		}
		resolved[name] = value
		return url.PathEscape(value)
	})
	if strings.Contains(path, "\x00error:") {
		return "", nil, nil, errors.New(strings.TrimPrefix(path[strings.Index(path, "\x00error:"):], "\x00error:"))
	}
	if strings.Contains(path, "\x00missing:") {
		name := strings.TrimPrefix(path[strings.Index(path, "\x00missing:"):], "\x00missing:")
		return "", nil, nil, fmt.Errorf("expected --%s for %s", kebabCase(name), route.Path)
	}
	return path, resolved, consumed, nil
}

func inferredPathBinding(name string) commandBinding {
	switch name {
	case "workspaceId":
		return commandBinding{Option: "workspace", Required: true, Resolver: "workspace", Source: "option"}
	case "channelId":
		return commandBinding{Option: "channel", Required: true, Resolver: "channel", Scope: "workspaceId", Source: "option"}
	case "participantId":
		return commandBinding{Aliases: []string{"participant"}, Option: "target", Required: true, Resolver: "participant", Scope: "workspaceId", Source: "option"}
	case "pageTitle":
		return commandBinding{Aliases: []string{"page"}, Option: "title", Required: true, Source: "option"}
	case "fileId":
		return commandBinding{Option: "file", Required: true, Source: "option"}
	case "folderId":
		return commandBinding{Option: "folder", Required: true, Source: "option"}
	case "jobId":
		return commandBinding{Aliases: []string{"wake"}, Option: "job", Required: true, Source: "option"}
	case "wakeId":
		return commandBinding{Option: "wake", Required: true, Source: "option"}
	case "token":
		return commandBinding{Aliases: []string{"token"}, Option: "invitation-token", Required: true, Source: "option"}
	case "versionNumber":
		return commandBinding{Aliases: []string{"version"}, Option: "version-number", Required: true, Source: "option"}
	default:
		return commandBinding{Option: kebabCase(name), Required: true, Source: "option"}
	}
}

func runCatalogHTTPCommand(
	ctx context.Context,
	client *client,
	route route,
	plan commandExecution,
	cmd command,
	path string,
	resolved map[string]string,
	consumed map[string]bool,
	stdout io.Writer,
	stdin io.Reader,
) error {
	if plan.Output == "agent-job-watch" {
		if optionText(cmd, []string{"job", "wake"}) == "" {
			return fmt.Errorf("expected --job")
		}
		return watchAgentJob(ctx, client, resolved["workspaceId"], cmd, stdout)
	}
	requestPath, spec, err := catalogHTTPRequest(ctx, client, route, plan, cmd, path, resolved, consumed, stdin)
	if err != nil {
		return err
	}
	response, err := client.raw(ctx, route.Method, requestPath, spec)
	if err != nil {
		return err
	}
	payload, err := readPayload(response, route.Method, requestPath)
	if err != nil {
		return err
	}
	return printCatalogCommandPayload(stdout, payload, cmd, plan.Output)
}

func catalogHTTPRequest(
	ctx context.Context,
	client *client,
	route route,
	plan commandExecution,
	cmd command,
	path string,
	resolved map[string]string,
	consumed map[string]bool,
	stdin io.Reader,
) (string, requestSpec, error) {
	spec := requestSpec{Headers: requestHeadersFromOptions(cmd)}
	explicitBody, hasExplicitBody, err := jsonBodyFromOptions(cmd, stdin)
	if err != nil {
		return "", requestSpec{}, err
	}
	requestBody := route.RequestBody
	if requestBody == "" {
		requestBody = defaultRequestBody(route.Method)
	}
	if requestBody == "none" && hasExplicitBody {
		return "", requestSpec{}, fmt.Errorf("operation %s does not accept a JSON body", route.ID)
	}
	if requestBody == "multipart" {
		return "", requestSpec{}, fmt.Errorf("operation %s expects multipart request bodies", route.ID)
	}
	values, err := catalogValues(ctx, client, cmd, plan.QueryParams, resolved, consumed)
	if err != nil {
		return "", requestSpec{}, err
	}
	if plan.QueryParams == nil {
		values = requestValuesFromOptions(cmd, consumed)
	}
	if route.Method == http.MethodGet || (route.Method == http.MethodDelete && requestBody == "none") || requestBody == "none" {
		return appendQuery(path, values), spec, nil
	}
	if hasExplicitBody {
		spec.JSONBody = explicitBody
		return path, spec, nil
	}
	if plan.BodyFields != nil {
		body, err := catalogValues(ctx, client, cmd, plan.BodyFields, resolved, consumed)
		if err != nil {
			return "", requestSpec{}, err
		}
		spec.JSONBody = compact(body)
		return path, spec, nil
	}
	spec.JSONBody = compact(requestValuesFromOptions(cmd, consumed))
	return path, spec, nil
}

func catalogValues(
	ctx context.Context,
	client *client,
	cmd command,
	bindings map[string]commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (map[string]any, error) {
	values := map[string]any{}
	for name, binding := range bindings {
		value, ok, err := catalogBindingValue(ctx, client, cmd, binding, resolved, consumed)
		if err != nil {
			return nil, err
		}
		if ok {
			values[name] = value
		}
	}
	return values, nil
}

func catalogBindingString(
	ctx context.Context,
	client *client,
	cmd command,
	binding commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (string, error) {
	value, ok, err := catalogBindingValue(ctx, client, cmd, binding, resolved, consumed)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(value), nil
	}
	return text, nil
}

func catalogBindingValue(
	ctx context.Context,
	client *client,
	cmd command,
	binding commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (any, bool, error) {
	switch binding.Source {
	case "literal":
		return binding.Value, true, nil
	case "flag":
		if binding.Option == "" {
			return nil, false, nil
		}
		consumed[binding.Option] = true
		return boolOption(cmd, binding.Option), true, nil
	case "text":
		value, ok, err := textBindingValue(cmd, binding)
		if err != nil || !ok {
			if binding.Required && !ok {
				return nil, false, fmt.Errorf("expected --%s", binding.Option)
			}
			return value, ok, err
		}
		resolvedValue, err := resolveBoundValue(ctx, client, binding, fmt.Sprint(value), resolved)
		return resolvedValue, true, err
	case "option", "":
		value, source, ok := bindingOptionValue(cmd, binding)
		if !ok {
			if binding.Required {
				return nil, false, fmt.Errorf("expected --%s", binding.Option)
			}
			return nil, false, nil
		}
		consumed[source] = true
		if binding.Type == "json" {
			parsed, err := jsonBindingValue(value, source)
			return parsed, true, err
		}
		if binding.Type == "integer" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return nil, false, fmt.Errorf("expected integer for --%s, got %s", source, value)
			}
			return parsed, true, nil
		}
		resolvedValue, err := resolveBoundValue(ctx, client, binding, value, resolved)
		return resolvedValue, true, err
	default:
		return nil, false, fmt.Errorf("unsupported catalog binding source: %s", binding.Source)
	}
}

func bindingOptionValue(cmd command, binding commandBinding) (string, string, bool) {
	for _, name := range bindingOptionNames(binding) {
		if value, ok := cmd.Options[name]; ok {
			return value, name, true
		}
		if value, ok := cmd.Flags[name]; ok && value {
			return "true", name, true
		}
	}
	return "", "", false
}

func bindingOptionNames(binding commandBinding) []string {
	names := []string{}
	if binding.Option != "" {
		names = append(names, binding.Option)
	}
	names = append(names, binding.Aliases...)
	return names
}

func textBindingValue(cmd command, binding commandBinding) (string, bool, error) {
	value, valueSource, hasValue := bindingOptionValue(cmd, commandBinding{Option: binding.Option})
	file, fileSource, hasFile := bindingOptionValue(cmd, commandBinding{Option: binding.FileOption})
	if hasValue && hasFile {
		return "", false, fmt.Errorf("use either --%s or --%s, not both", valueSource, fileSource)
	}
	if hasFile {
		bytes, err := os.ReadFile(filepath.Clean(file))
		return string(bytes), true, err
	}
	if hasValue {
		return value, true, nil
	}
	if binding.Positionals == "join" && len(cmd.Positionals) > 0 {
		return strings.Join(cmd.Positionals, " "), true, nil
	}
	return "", false, nil
}

func jsonBindingValue(value string, source string) (any, error) {
	if strings.HasSuffix(source, "-file") {
		bytes, err := os.ReadFile(filepath.Clean(value))
		if err != nil {
			return nil, err
		}
		value = string(bytes)
	}
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func resolveBoundValue(
	ctx context.Context,
	client *client,
	binding commandBinding,
	value string,
	resolved map[string]string,
) (string, error) {
	switch binding.Resolver {
	case "":
		return value, nil
	case "workspace":
		return resolveWorkspaceID(ctx, client, value)
	case "channel":
		workspaceID := resolved[binding.Scope]
		if workspaceID == "" {
			return "", fmt.Errorf("resolver channel requires scope %s", binding.Scope)
		}
		return resolveChannelID(ctx, client, workspaceID, value)
	case "participant", "agent":
		workspaceID := resolved[binding.Scope]
		if workspaceID == "" {
			return "", fmt.Errorf("resolver %s requires scope %s", binding.Resolver, binding.Scope)
		}
		participantID, err := resolveParticipantID(ctx, client, workspaceID, value)
		if err != nil {
			return "", err
		}
		if binding.Resolver == "agent" {
			if !strings.HasPrefix(participantID, "agent:") {
				return "", fmt.Errorf("dream agent must resolve to an agent participant, got %s", participantID)
			}
			return strings.TrimPrefix(participantID, "agent:"), nil
		}
		return participantID, nil
	default:
		return "", fmt.Errorf("unsupported catalog resolver: %s", binding.Resolver)
	}
}

func runCatalogDownloadCommand(ctx context.Context, client *client, route route, cmd command, path string, stdout io.Writer) error {
	response, err := client.raw(ctx, route.Method, path, requestSpec{})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	bytes, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed with %d: %s", route.Method, path, response.StatusCode, string(bytes))
	}
	if output := cmd.string("output", ""); output != "" {
		return os.WriteFile(filepath.Clean(output), bytes, 0o600)
	}
	_, err = stdout.Write(bytes)
	return err
}

func runCatalogMultipartCommand(ctx context.Context, client *client, cmd command, path string, stdout io.Writer) error {
	filePath, err := cmd.required("path", "file-path")
	if err != nil {
		return err
	}
	parsed, err := client.multipart(ctx, http.MethodPost, path, map[string]string{
		"folderPath": cmd.string("folder", ""),
		"scope":      cmd.string("scope", "workspace"),
	}, "file", filePath, cmd.string("name", filepath.Base(filePath)), cmd.string("type", "application/octet-stream"))
	if err != nil {
		return err
	}
	return printJSON(stdout, parsed)
}

func printCatalogCommandPayload(stdout io.Writer, payload responsePayload, cmd command, output string) error {
	switch output {
	case "":
		return printPayload(stdout, payload, cmd)
	case "messages":
		return printCommandOutput(stdout, payload.Parsed, cmd, outputMessages)
	case "wiki-recent":
		return printCommandOutput(stdout, payload.Parsed, cmd, outputWikiRecent)
	case "wiki-version":
		return printCommandOutput(stdout, payload.Parsed, cmd, outputWikiVersion)
	case "wiki-versions":
		return printCommandOutput(stdout, payload.Parsed, cmd, outputWikiVersions)
	case "json", "bytes":
		return printPayload(stdout, payload, cmd)
	default:
		return fmt.Errorf("unsupported catalog command output: %s", output)
	}
}

func optionText(cmd command, names []string) string {
	for _, name := range names {
		if value := cmd.string(name, ""); value != "" {
			return value
		}
	}
	return ""
}
