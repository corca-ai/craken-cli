package craken

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
)

func runCatalogCommand(ctx context.Context, client *client, cmd command, stdout io.Writer, stdin io.Reader, stderr io.Writer) error {
	catalogValue, err := client.json(ctx, "/api/client")
	if err != nil {
		return err
	}
	catalog, err := catalogFromValue(catalogValue)
	if err != nil {
		return err
	}
	// A bare resource arrives with Action == ""; resolve the server-advertised
	// default action for the resource, falling back to "help" so an unknown or
	// default-less resource still renders help instead of dispatching nothing.
	if cmd.Action == "" {
		cmd.Action = catalogDefaultAction(catalog.Shortcuts, cmd.Resource)
		if cmd.Action == "" {
			cmd.Action = "help"
		}
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
	cmd, err = reparseCatalogCommand(cmd, plan, *selectedRoute)
	if err != nil {
		return err
	}
	plan = selectedExecution(*serverCommand, cmd)
	if plan.Transport == "" {
		plan.Transport = commandTransportHTTP
	}
	selectedRoute = routeByID(catalog.Routes, plan.OperationID)
	if selectedRoute == nil {
		return fmt.Errorf("unknown selected operation: %s", plan.OperationID)
	}
	if err := validateCatalogInputs(cmd, *selectedRoute, plan, false); err != nil {
		return err
	}
	requestPath, resolved, consumed, err := catalogCommandPath(ctx, client, catalog.Routes, *selectedRoute, plan, cmd)
	if err != nil {
		return err
	}
	return executeCatalogTransport(ctx, client, catalog.Routes, *selectedRoute, plan, cmd, requestPath, resolved, consumed, stdout, stdin, stderr)
}

func executeCatalogTransport(ctx context.Context, client *client, routes []route, selectedRoute route, plan commandExecution, cmd command, requestPath string, resolved map[string]string, consumed map[string]bool, stdout io.Writer, stdin io.Reader, stderr io.Writer) error {
	switch plan.Transport {
	case commandTransportHTTP:
		return runCatalogHTTPCommand(ctx, client, routes, selectedRoute, plan, cmd, requestPath, resolved, consumed, stdout, stdin)
	case commandTransportDownload:
		return runCatalogDownloadCommand(ctx, client, routes, selectedRoute, plan, cmd, requestPath, resolved, consumed, stdout)
	case commandTransportMultipart:
		return runCatalogMultipartCommand(ctx, client, routes, selectedRoute, plan, cmd, requestPath, resolved, consumed, stdout)
	case commandTransportWebSocket:
		return runCatalogWebSocketCommand(ctx, client, routes, plan, cmd, requestPath, resolved, consumed, stdout, stderr)
	default:
		return fmt.Errorf("unsupported catalog command transport: %s", plan.Transport)
	}
}

// catalogDefaultAction returns the server-advertised default action for a
// resource shortcut, or "" when the resource is unknown or declares no default.
func catalogDefaultAction(shortcuts []shortcut, resource string) string {
	for _, s := range shortcuts {
		if s.Resource == resource {
			return s.DefaultAction
		}
	}
	return ""
}

func catalogCommandByID(commands []cliCommand, id string) *cliCommand {
	return findInSlice(commands, func(c cliCommand) bool { return c.ID == id })
}

// findInSlice returns a pointer to the first element matching pred, or nil.
func findInSlice[T any](items []T, pred func(T) bool) *T {
	for i := range items {
		if pred(items[i]) {
			return &items[i]
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
	path, missing, err := expandPathParams(route.Path, func(name string) (string, bool, error) {
		binding := plan.PathParams[name]
		if binding.Source == "" {
			return "", false, nil
		}
		value, err := catalogBindingString(ctx, client, routes, cmd, binding, resolved, consumed)
		if err != nil {
			return "", false, err
		}
		if value == "" {
			return "", false, nil
		}
		resolved[name] = value
		return value, true, nil
	})
	if err != nil {
		return "", nil, nil, err
	}
	if missing != "" {
		return "", nil, nil, fmt.Errorf("expected --%s for %s", kebabCase(missing), route.Path)
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
	if err := saveTokenProfile(client, cmd.string("save-token-profile", ""), payload.Parsed); err != nil {
		return stageError("local-persist", err, payload.Evidence)
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
	spec := requestSpec{Headers: requestHeadersFromOptions(cmd), Query: rawQuery(cmd), MetadataPath: cmd.string("response-meta", "")}
	wireHeaders(spec.Headers, cmd, route)
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
	values, err := catalogValues(ctx, client, routes, cmd, plan.QueryParams, resolved, consumed, nil)
	if err != nil {
		return "", requestSpec{}, err
	}
	if route.Specification != nil {
		if plan.QueryParams == nil {
			values, err = wireOptions(cmd, route.Specification.Wire.Query)
		} else {
			values, err = convertBoundWire(values, route.Specification.Wire.Query, plan.QueryParams)
		}
		if err != nil {
			return "", requestSpec{}, err
		}
	} else if plan.QueryParams == nil {
		values = requestValuesFromOptions(cmd, consumed)
	}
	if route.Method == http.MethodGet || (route.Method == http.MethodDelete && requestBody == "none") || requestBody == "none" {
		path, err = appendWireQuery(path, values, route)
		return path, spec, err
	}
	// A body method (POST/PATCH/PUT) can still declare query params; append the
	// resolved query values to the path so a command that combines query params
	// with a JSON body sends both rather than dropping the query.
	if plan.QueryParams != nil || route.Specification != nil {
		path, err = appendWireQuery(path, values, route)
		if err != nil {
			return "", requestSpec{}, err
		}
	}
	if hasExplicitBody {
		spec.JSONBody = explicitBody
		spec.HasJSONBody = true
		return path, spec, nil
	}
	if plan.BodyFields != nil {
		body, err := catalogValues(ctx, client, routes, cmd, plan.BodyFields, resolved, consumed, stdin)
		if err != nil {
			return "", requestSpec{}, err
		}
		if route.Specification != nil {
			body, err = convertBoundWire(body, route.Specification.Wire.Body, plan.BodyFields)
			if err != nil {
				return "", requestSpec{}, err
			}
		}
		spec.JSONBody = body
		return path, spec, nil
	}
	if route.Specification != nil {
		body, err := wireOptions(cmd, route.Specification.Wire.Body)
		if err != nil {
			return "", requestSpec{}, err
		}
		spec.JSONBody = body
	} else {
		spec.JSONBody = compact(requestValuesFromOptions(cmd, consumed))
	}
	return path, spec, nil
}

func runCatalogDownloadCommand(ctx context.Context, client *client, routes []route, route route, plan commandExecution, cmd command, path string, resolved map[string]string, consumed map[string]bool, stdout io.Writer) error {
	if plan.QueryParams != nil {
		values, err := catalogValues(ctx, client, routes, cmd, plan.QueryParams, resolved, consumed, nil)
		if err != nil {
			return err
		}
		path, err = appendWireQuery(path, values, route)
		if err != nil {
			return err
		}
	}
	headers := downloadHeaders(cmd)
	wireHeaders(headers, cmd, route)
	response, err := client.raw(ctx, route.Method, path, requestSpec{Headers: headers, Query: rawQuery(cmd), MetadataPath: cmd.string("response-meta", "")})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	evidence := responseInfo(response)
	if response.StatusCode == http.StatusNotModified {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		bytes, err := io.ReadAll(response.Body)
		if err != nil {
			return stageError("decode", err, evidence)
		}
		return httpFailure(response, route.Method, path, string(bytes))
	}
	if cmd.string("format", "") == "none" {
		_, err = io.Copy(io.Discard, response.Body)
		return stageError("decode", err, evidence)
	}
	if output := cmd.string("output", ""); output != "" {
		return stageError("output", writeDownload(output, response.Body), evidence)
	}
	_, err = io.Copy(stdout, response.Body)
	return stageError("output", err, evidence)
}

func runCatalogMultipartCommand(
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
) error {
	multipartPlan := plan.Multipart
	if multipartPlan.FileOption == "" || multipartPlan.FileField == "" {
		return fmt.Errorf("catalog command %s missing multipart plan", plan.OperationID)
	}
	filePath := cmd.string(multipartPlan.FileOption, "")
	if !multipartPlan.FileOptional {
		var err error
		filePath, err = cmd.required(multipartPlan.FileOption)
		if err != nil {
			return err
		}
	}
	fieldValues, err := catalogValues(ctx, client, routes, cmd, multipartPlan.Fields, resolved, consumed, nil)
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
	if filePath != "" && fileName == "" && multipartPlan.FileNameDefault == "basename" {
		fileName = filepath.Base(filePath)
	}
	contentType := multipartPlan.ContentTypeDefault
	if multipartPlan.ContentTypeOption != "" {
		contentType = cmd.string(multipartPlan.ContentTypeOption, contentType)
	}
	if plan.QueryParams != nil {
		query, err := catalogValues(ctx, client, routes, cmd, plan.QueryParams, resolved, consumed, nil)
		if err != nil {
			return err
		}
		path, err = appendWireQuery(path, query, route)
		if err != nil {
			return err
		}
	}
	headers := requestHeadersFromOptions(cmd)
	wireHeaders(headers, cmd, route)
	parsed, err := client.multipart(ctx, route.Method, path, fields, multipartPlan.FileField, filePath, fileName, contentType, headers, cmd)
	if err != nil {
		return err
	}
	if err := saveTokenProfile(client, cmd.string("save-token-profile", ""), parsed); err != nil {
		return stageError("local-persist", err, client.lastResponse)
	}
	return printCatalogCommandPayload(stdout, responsePayload{Parsed: parsed, Evidence: client.lastResponse, NoBody: client.lastResponse != nil && (client.lastResponse.Status == http.StatusNoContent || client.lastResponse.Status == http.StatusNotModified)}, cmd, plan.Output)
}

func printCatalogCommandPayload(stdout io.Writer, payload responsePayload, cmd command, output *commandOutputPlan) (resultErr error) {
	defer func() { resultErr = stageError("output", resultErr, payload.Evidence) }()
	if payload.NoBody {
		return nil
	}
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
