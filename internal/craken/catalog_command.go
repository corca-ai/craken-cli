package craken

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
		plan.Transport = commandTransportHTTP
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
	case commandTransportHTTP:
		return runCatalogHTTPCommand(ctx, client, *selectedRoute, plan, cmd, requestPath, resolved, consumed, stdout, stdin)
	case commandTransportDownload:
		return runCatalogDownloadCommand(ctx, client, *selectedRoute, cmd, requestPath, stdout)
	case commandTransportMultipart:
		return runCatalogMultipartCommand(ctx, client, cmd, requestPath, stdout)
	case commandTransportWebSocket:
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
		return commandBinding{Option: "workspace", Required: true, Resolver: commandBindingResolverWorkspace, Source: commandBindingSourceOption}
	case "channelId":
		return commandBinding{Option: "channel", Required: true, Resolver: commandBindingResolverChannel, Scope: "workspaceId", Source: commandBindingSourceOption}
	case "participantId":
		return commandBinding{Aliases: []string{"participant"}, Option: "target", Required: true, Resolver: commandBindingResolverParticipant, Scope: "workspaceId", Source: commandBindingSourceOption}
	case "pageTitle":
		return commandBinding{Aliases: []string{"page"}, Option: "title", Required: true, Source: commandBindingSourceOption}
	case "fileId":
		return commandBinding{Option: "file", Required: true, Source: commandBindingSourceOption}
	case "folderId":
		return commandBinding{Option: "folder", Required: true, Source: commandBindingSourceOption}
	case "jobId":
		return commandBinding{Aliases: []string{"wake"}, Option: "job", Required: true, Source: commandBindingSourceOption}
	case "wakeId":
		return commandBinding{Option: "wake", Required: true, Source: commandBindingSourceOption}
	case "token":
		return commandBinding{Aliases: []string{"token"}, Option: "invitation-token", Required: true, Source: commandBindingSourceOption}
	case "versionNumber":
		return commandBinding{Aliases: []string{"version"}, Option: "version-number", Required: true, Source: commandBindingSourceOption}
	default:
		return commandBinding{Option: kebabCase(name), Required: true, Source: commandBindingSourceOption}
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
	if plan.Output == commandExecutionOutputAgentJobWatch {
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

func printCatalogCommandPayload(stdout io.Writer, payload responsePayload, cmd command, output commandExecutionOutput) error {
	switch output {
	case "":
		return printPayload(stdout, payload, cmd)
	case commandExecutionOutputMessages:
		return printCommandOutput(stdout, payload.Parsed, cmd, outputMessages)
	case commandExecutionOutputWikiRecent:
		return printCommandOutput(stdout, payload.Parsed, cmd, outputWikiRecent)
	case commandExecutionOutputWikiVersion:
		return printCommandOutput(stdout, payload.Parsed, cmd, outputWikiVersion)
	case commandExecutionOutputWikiVersions:
		return printCommandOutput(stdout, payload.Parsed, cmd, outputWikiVersions)
	case commandExecutionOutputJSON, commandExecutionOutputBytes:
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
