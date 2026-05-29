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
	requestPath, resolved, consumed, err := catalogCommandPath(ctx, client, catalog.Routes, *selectedRoute, plan, cmd)
	if err != nil {
		return err
	}
	switch plan.Transport {
	case commandTransportHTTP:
		return runCatalogHTTPCommand(ctx, client, catalog.Routes, *selectedRoute, plan, cmd, requestPath, resolved, consumed, stdout, stdin)
	case commandTransportDownload:
		return runCatalogDownloadCommand(ctx, client, *selectedRoute, cmd, requestPath, stdout)
	case commandTransportMultipart:
		return runCatalogMultipartCommand(ctx, client, catalog.Routes, plan, cmd, requestPath, resolved, consumed, stdout)
	case commandTransportWebSocket:
		return runCatalogWebSocketCommand(ctx, client, catalog.Routes, plan, cmd, requestPath, resolved, consumed, stdout)
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
	if !isEmptyMultipartPlan(variant.Multipart) {
		base.Multipart = variant.Multipart
	}
	if variant.Output != nil {
		base.Output = variant.Output
	}
	if variant.Poll != nil {
		base.Poll = variant.Poll
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
	if len(variant.WebSocket.Protocols) > 0 {
		base.WebSocket = variant.WebSocket
	}
	return base
}

func isEmptyMultipartPlan(plan commandMultipartPlan) bool {
	return plan.FileOption == "" && plan.FileField == "" && len(plan.Fields) == 0
}

func catalogCommandPath(ctx context.Context, client *client, routes []route, route route, plan commandExecution, cmd command) (string, map[string]string, map[string]bool, error) {
	consumed := map[string]bool{}
	resolved := map[string]string{}
	path := pathParamPattern.ReplaceAllStringFunc(route.Path, func(match string) string {
		name := match[1 : len(match)-1]
		binding := plan.PathParams[name]
		if binding.Source == "" {
			return "\x00missing:" + name
		}
		value, err := catalogBindingString(ctx, client, routes, cmd, binding, resolved, consumed)
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

func runCatalogHTTPCommand(
	ctx context.Context,
	client *client,
	routes []route,
	route route,
	plan commandExecution,
	cmd command,
	path string,
	resolved map[string]string,
	consumed map[string]bool,
	stdout io.Writer,
	stdin io.Reader,
) error {
	if plan.Poll != nil {
		return runCatalogPollCommand(ctx, client, routes, route, plan, cmd, path, resolved, consumed, stdout, stdin)
	}
	requestPath, spec, err := catalogHTTPRequest(ctx, client, routes, route, plan, cmd, path, resolved, consumed, stdin)
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
	routes []route,
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
	values, err := catalogValues(ctx, client, routes, cmd, plan.QueryParams, resolved, consumed)
	if err != nil {
		return "", requestSpec{}, err
	}
	if plan.QueryParams == nil {
		values = requestValuesFromOptions(cmd, consumed)
	}
	if route.Method == http.MethodGet || (route.Method == http.MethodDelete && requestBody == "none") || requestBody == "none" {
		return appendQuery(path, values), spec, nil
	}
	// A body method (POST/PATCH/PUT) can still declare query params; append the
	// resolved query values to the path so a command that combines query params
	// with a JSON body sends both rather than dropping the query.
	if plan.QueryParams != nil {
		path = appendQuery(path, values)
	}
	if hasExplicitBody {
		spec.JSONBody = explicitBody
		return path, spec, nil
	}
	if plan.BodyFields != nil {
		body, err := catalogValues(ctx, client, routes, cmd, plan.BodyFields, resolved, consumed)
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

func runCatalogMultipartCommand(
	ctx context.Context,
	client *client,
	routes []route,
	plan commandExecution,
	cmd command,
	path string,
	resolved map[string]string,
	consumed map[string]bool,
	stdout io.Writer,
) error {
	multipartPlan := plan.Multipart
	if multipartPlan.FileOption == "" || multipartPlan.FileField == "" {
		return fmt.Errorf("catalog command %s missing multipart plan", plan.OperationID)
	}
	filePath, err := cmd.required(multipartPlan.FileOption)
	if err != nil {
		return err
	}
	fieldValues, err := catalogValues(ctx, client, routes, cmd, multipartPlan.Fields, resolved, consumed)
	if err != nil {
		return err
	}
	fields := map[string]string{}
	for name, value := range fieldValues {
		fields[name] = fmt.Sprint(value)
	}
	fileName := ""
	if multipartPlan.FileNameOption != "" {
		fileName = cmd.string(multipartPlan.FileNameOption, "")
	}
	if fileName == "" && multipartPlan.FileNameDefault == "basename" {
		fileName = filepath.Base(filePath)
	}
	contentType := multipartPlan.ContentTypeDefault
	if multipartPlan.ContentTypeOption != "" {
		contentType = cmd.string(multipartPlan.ContentTypeOption, contentType)
	}
	parsed, err := client.multipart(ctx, http.MethodPost, path, fields, multipartPlan.FileField, filePath, fileName, contentType)
	if err != nil {
		return err
	}
	return printJSON(stdout, parsed)
}

func printCatalogCommandPayload(stdout io.Writer, payload responsePayload, cmd command, output *commandOutputPlan) error {
	if output == nil {
		return printPayload(stdout, payload, cmd)
	}
	switch output.Mode {
	case commandOutputModeJSON, commandOutputModeBytes:
		return printPayload(stdout, payload, cmd)
	case commandOutputModeTable:
		return printCommandOutput(stdout, payload.Parsed, cmd, *output)
	default:
		return fmt.Errorf("unsupported catalog command output mode: %s", output.Mode)
	}
}
