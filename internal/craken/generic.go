package craken

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var pathParamPattern = regexp.MustCompile(`\{([^}/]+)\}`)

type route struct {
	ID              string         `json:"id"`
	Method          string         `json:"method"`
	Path            string         `json:"path"`
	Description     string         `json:"description"`
	Auth            string         `json:"auth"`
	Capability      string         `json:"capability,omitempty"`
	PathParams      []catalogField `json:"pathParams,omitempty"`
	PathParameters  []catalogField `json:"pathParameters,omitempty"`
	QueryParams     []catalogField `json:"queryParams,omitempty"`
	QueryParameters []catalogField `json:"queryParameters,omitempty"`
	BodyFields      []catalogField `json:"bodyFields,omitempty"`
	RequestBody     string         `json:"requestBody"`
	ResponseExample any            `json:"responseExample,omitempty"`
	Stream          string         `json:"stream,omitempty"`
}

type clientCatalog struct {
	BuildID       string           `json:"buildId"`
	Commands      []cliCommand     `json:"commands,omitempty"`
	Examples      []commandExample `json:"examples,omitempty"`
	Help          *catalogHelp     `json:"help,omitempty"`
	Routes        []route          `json:"routes"`
	SchemaVersion int              `json:"schemaVersion"`
	Shortcuts     []shortcut       `json:"shortcuts,omitempty"`
}

type catalogHelp struct {
	Sections []catalogHelpSection `json:"sections,omitempty"`
	Summary  string               `json:"summary,omitempty"`
	Title    string               `json:"title"`
}

type catalogHelpSection struct {
	Body  string            `json:"body,omitempty"`
	Items []catalogHelpItem `json:"items,omitempty"`
	Title string            `json:"title"`
}

type catalogHelpItem struct {
	Command     string `json:"command,omitempty"`
	Description string `json:"description"`
	Label       string `json:"label,omitempty"`
}

type cliCommand struct {
	Command     string           `json:"command"`
	Description string           `json:"description"`
	Execution   commandExecution `json:"execution,omitempty"`
	Examples    []string         `json:"examples,omitempty"`
	Group       string           `json:"group"`
	ID          string           `json:"id"`
	OperationID string           `json:"operationId,omitempty"`
}

type commandExecution struct {
	BodyFields  map[string]commandBinding `json:"bodyFields,omitempty"`
	Multipart   commandMultipartPlan      `json:"multipart,omitempty"`
	OperationID string                    `json:"operationId,omitempty"`
	Output      *commandOutputPlan        `json:"output,omitempty"`
	PathParams  map[string]commandBinding `json:"pathParams,omitempty"`
	Poll        *commandPollPlan          `json:"poll,omitempty"`
	QueryParams map[string]commandBinding `json:"queryParams,omitempty"`
	Transport   commandTransport          `json:"transport,omitempty"`
	Variants    []commandExecutionVariant `json:"variants,omitempty"`
	WebSocket   commandWebSocketPlan      `json:"websocket,omitempty"`
}

type commandExecutionVariant struct {
	BodyFields  map[string]commandBinding `json:"bodyFields,omitempty"`
	Multipart   commandMultipartPlan      `json:"multipart,omitempty"`
	OperationID string                    `json:"operationId,omitempty"`
	Output      *commandOutputPlan        `json:"output,omitempty"`
	PathParams  map[string]commandBinding `json:"pathParams,omitempty"`
	Poll        *commandPollPlan          `json:"poll,omitempty"`
	QueryParams map[string]commandBinding `json:"queryParams,omitempty"`
	Transport   commandTransport          `json:"transport,omitempty"`
	When        commandCondition          `json:"when,omitempty"`
	WebSocket   commandWebSocketPlan      `json:"websocket,omitempty"`
}

type commandCondition struct {
	Option string `json:"option,omitempty"`
}

type commandBinding struct {
	Aliases     []string                  `json:"aliases,omitempty"`
	Default     any                       `json:"default,omitempty"`
	FileOption  string                    `json:"fileOption,omitempty"`
	Name        string                    `json:"name,omitempty"`
	Option      string                    `json:"option,omitempty"`
	Positionals commandBindingPositionals `json:"positionals,omitempty"`
	Required    bool                      `json:"required,omitempty"`
	Resolver    *commandResolverPlan      `json:"resolver,omitempty"`
	Source      commandBindingSource      `json:"source,omitempty"`
	Type        commandBindingValueType   `json:"type,omitempty"`
	Value       any                       `json:"value,omitempty"`
}

type commandResolverPlan struct {
	CollectionPath       string                    `json:"collectionPath"`
	Label                string                    `json:"label"`
	MatchFields          []string                  `json:"matchFields"`
	OperationID          string                    `json:"operationId"`
	PathParams           map[string]commandBinding `json:"pathParams,omitempty"`
	RequiredResultPrefix string                    `json:"requiredResultPrefix,omitempty"`
	ResultPath           string                    `json:"resultPath"`
	TrimResultPrefix     string                    `json:"trimResultPrefix,omitempty"`
}

type commandOutputPlan struct {
	Columns  []commandOutputColumn `json:"columns,omitempty"`
	Mode     commandOutputMode     `json:"mode"`
	RowsPath string                `json:"rowsPath,omitempty"`
}

type commandOutputColumn struct {
	Paths []string `json:"paths"`
}

type commandPollPlan struct {
	DefaultIntervalSeconds int      `json:"defaultIntervalSeconds"`
	DefaultMaxPolls        int      `json:"defaultMaxPolls"`
	IntervalOption         string   `json:"intervalOption"`
	MaxPollsOption         string   `json:"maxPollsOption"`
	StatusPath             string   `json:"statusPath"`
	TerminalValues         []string `json:"terminalValues"`
}

type commandMultipartPlan struct {
	ContentTypeDefault string                    `json:"contentTypeDefault,omitempty"`
	ContentTypeOption  string                    `json:"contentTypeOption,omitempty"`
	Fields             map[string]commandBinding `json:"fields,omitempty"`
	FileField          string                    `json:"fileField,omitempty"`
	FileNameDefault    string                    `json:"fileNameDefault,omitempty"`
	FileNameOption     string                    `json:"fileNameOption,omitempty"`
	FileOption         string                    `json:"fileOption,omitempty"`
}

type commandWebSocketPlan struct {
	Protocols []commandWebSocketProtocol `json:"protocols,omitempty"`
}

type commandWebSocketProtocol struct {
	Payload map[string]commandBinding `json:"payload,omitempty"`
	Prefix  string                    `json:"prefix,omitempty"`
	Source  string                    `json:"source"`
	Value   string                    `json:"value,omitempty"`
}

type commandExample struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type shortcut struct {
	Actions     []string `json:"actions"`
	Description string   `json:"description"`
	Resource    string   `json:"resource"`
}

type catalogField struct {
	Name        string                `json:"name"`
	Type        string                `json:"type,omitempty"`
	Description string                `json:"description,omitempty"`
	Required    bool                  `json:"required,omitempty"`
	Values      []string              `json:"values,omitempty"`
	Resolver    *catalogFieldResolver `json:"resolver,omitempty"`
}

// catalogFieldResolver describes how a UUID parameter can be addressed by a human
// name. The server advertises it on route path params so generic callers (do/get)
// can resolve names, and so help can show that a parameter accepts a name.
type catalogFieldResolver struct {
	CollectionPath       string   `json:"collectionPath"`
	Label                string   `json:"label"`
	MatchFields          []string `json:"matchFields"`
	OperationID          string   `json:"operationId"`
	RequiredResultPrefix string   `json:"requiredResultPrefix,omitempty"`
	ResultPath           string   `json:"resultPath"`
	Scope                string   `json:"scope,omitempty"`
	TrimResultPrefix     string   `json:"trimResultPrefix,omitempty"`
}

func runCommands(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	value, err := client.json(ctx, "/api/client")
	if err != nil {
		return err
	}
	catalog, err := catalogFromValue(value)
	if err != nil {
		return err
	}
	if cmd.string("format", "") == "text" {
		if len(catalog.Commands) > 0 {
			return printCommandsText(stdout, catalog.Commands)
		}
		for _, route := range catalog.Routes {
			if _, err := fmt.Fprintf(stdout, "Operation\t%s\tcraken do %s\t%s\n", route.ID, route.ID, route.Description); err != nil {
				return err
			}
		}
		return nil
	}
	return printJSON(stdout, value)
}

func runDo(ctx context.Context, client *client, cmd command, stdout io.Writer, stdin io.Reader) error {
	operationID := cmd.Action
	if operationID == "" || operationID == "help" {
		return fmt.Errorf("expected operation id")
	}
	catalog, err := client.json(ctx, "/api/client")
	if err != nil {
		return err
	}
	routes, err := routesFromCatalog(catalog)
	if err != nil {
		return err
	}
	var selected *route
	for i := range routes {
		if routes[i].ID == operationID {
			selected = &routes[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("unknown operation: %s", operationID)
	}
	if selected.Stream == "websocket" {
		return fmt.Errorf("operation %s is a stream. Use the resource-specific tail command for realtime subscriptions", operationID)
	}
	requestPath, spec, err := requestFromDiscoveredRoute(ctx, client, routes, *selected, cmd, stdin)
	if err != nil {
		return err
	}
	response, err := client.raw(ctx, selected.Method, requestPath, spec)
	if err != nil {
		return err
	}
	payload, err := readPayload(response, selected.Method, requestPath)
	if err != nil {
		return err
	}
	if err := saveTokenProfile(client, cmd.string("save-token-profile", ""), payload.Parsed); err != nil {
		return err
	}
	return printPayload(stdout, payload, cmd)
}

func runRawHTTP(ctx context.Context, client *client, method string, path string, cmd command, stdout io.Writer, stdin io.Reader) error {
	if path == "" {
		return fmt.Errorf("expected API path")
	}
	spec, err := rawRequestFromOptions(methodUpper(method), cmd, stdin)
	if err != nil {
		return err
	}
	response, err := client.raw(ctx, methodUpper(method), path, spec)
	if err != nil {
		return err
	}
	payload, err := readPayload(response, methodUpper(method), path)
	if err != nil {
		return err
	}
	if err := saveTokenProfile(client, cmd.string("save-token-profile", ""), payload.Parsed); err != nil {
		return err
	}
	return printPayload(stdout, payload, cmd)
}

func routesFromCatalog(value any) ([]route, error) {
	catalog, err := catalogFromValue(value)
	if err != nil {
		return nil, err
	}
	return catalog.Routes, nil
}

func catalogFromValue(value any) (clientCatalog, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return clientCatalog{}, fmt.Errorf("client command catalog must be an object")
	}
	if number, ok := root["schemaVersion"].(float64); !ok || number != 1 {
		return clientCatalog{}, fmt.Errorf("client command catalog schemaVersion must be 1")
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return clientCatalog{}, err
	}
	var catalog clientCatalog
	if err := json.Unmarshal(bytes, &catalog); err != nil {
		return clientCatalog{}, err
	}
	if catalog.SchemaVersion != 1 {
		return clientCatalog{}, fmt.Errorf("client command catalog schemaVersion must be 1")
	}
	if catalog.Routes == nil {
		return clientCatalog{}, fmt.Errorf("client command catalog routes must be an array")
	}
	for _, route := range catalog.Routes {
		if route.ID == "" || route.Method == "" || route.Path == "" || route.Description == "" || route.Auth == "" || route.RequestBody == "" {
			return clientCatalog{}, fmt.Errorf("invalid route in client command catalog")
		}
	}
	for _, command := range catalog.Commands {
		if trim(command.ID) == "" || trim(command.Group) == "" || trim(command.Command) == "" || trim(command.Description) == "" {
			return clientCatalog{}, fmt.Errorf("invalid command in client command catalog")
		}
		for _, example := range command.Examples {
			if trim(example) == "" {
				return clientCatalog{}, fmt.Errorf("invalid command example in client command catalog")
			}
		}
	}
	for _, example := range catalog.Examples {
		if trim(example.Command) == "" || trim(example.Description) == "" {
			return clientCatalog{}, fmt.Errorf("invalid example in client command catalog")
		}
	}
	for _, shortcut := range catalog.Shortcuts {
		if trim(shortcut.Resource) == "" || len(shortcut.Actions) == 0 || trim(shortcut.Description) == "" {
			return clientCatalog{}, fmt.Errorf("invalid shortcut in client command catalog")
		}
	}
	if catalog.Help != nil {
		if trim(catalog.Help.Title) == "" {
			return clientCatalog{}, fmt.Errorf("invalid help in client command catalog")
		}
		for _, section := range catalog.Help.Sections {
			if trim(section.Title) == "" {
				return clientCatalog{}, fmt.Errorf("invalid help section in client command catalog")
			}
			for _, item := range section.Items {
				if trim(item.Description) == "" {
					return clientCatalog{}, fmt.Errorf("invalid help item in client command catalog")
				}
			}
		}
	}
	return catalog, nil
}

func catalogFieldIndex(fields []catalogField) map[string]catalogField {
	index := map[string]catalogField{}
	for _, field := range fields {
		index[field.Name] = field
	}
	return index
}

// fieldResolverPlan adapts a route-level field resolver to the command resolver
// plan executed by resolveCatalogValue. A workspace-scoped resolver lists through
// the workspace detail operation, so it binds workspaceId from the already
// resolved path values.
func fieldResolverPlan(resolver catalogFieldResolver) commandResolverPlan {
	plan := commandResolverPlan{
		CollectionPath:       resolver.CollectionPath,
		Label:                resolver.Label,
		MatchFields:          resolver.MatchFields,
		OperationID:          resolver.OperationID,
		RequiredResultPrefix: resolver.RequiredResultPrefix,
		ResultPath:           resolver.ResultPath,
		TrimResultPrefix:     resolver.TrimResultPrefix,
	}
	if resolver.Scope == "workspace" {
		plan.PathParams = map[string]commandBinding{
			"workspaceId": {Source: commandBindingSourceResolved, Name: "workspaceId", Required: true},
		}
	}
	return plan
}

func requestFromDiscoveredRoute(ctx context.Context, client *client, routes []route, route route, cmd command, stdin io.Reader) (string, requestSpec, error) {
	consumed := map[string]bool{}
	positionals := append([]string{}, cmd.Positionals...)
	pathFields := catalogFieldIndex(firstCatalogFields(route.PathParams, route.PathParameters))
	resolved := map[string]string{}
	var resolveErr error
	path := pathParamPattern.ReplaceAllStringFunc(route.Path, func(match string) string {
		name := match[1 : len(match)-1]
		for _, alias := range optionAliases(name) {
			consumed[alias] = true
		}
		raw, ok := optionValue(cmd, name)
		if !ok {
			if len(positionals) == 0 {
				return "\x00missing:" + name
			}
			raw = positionals[0]
			positionals = positionals[1:]
		}
		value := raw
		// When the catalog advertises a name resolver for this id, accept a human
		// name and resolve it to a UUID. A value that is already an id matches the
		// resolver's id field and passes through unchanged.
		if field, ok := pathFields[name]; ok && field.Resolver != nil && raw != "" && resolveErr == nil {
			id, err := resolveCatalogValue(ctx, client, routes, fieldResolverPlan(*field.Resolver), raw, resolved)
			if err != nil {
				resolveErr = err
				return ""
			}
			value = id
		}
		resolved[name] = value
		return url.PathEscape(value)
	})
	if resolveErr != nil {
		return "", requestSpec{}, resolveErr
	}
	if strings.Contains(path, "\x00missing:") {
		name := strings.TrimPrefix(path[strings.Index(path, "\x00missing:"):], "\x00missing:")
		return "", requestSpec{}, fmt.Errorf("expected --%s for %s", kebabCase(name), route.Path)
	}
	if len(positionals) > 0 {
		return "", requestSpec{}, fmt.Errorf("unexpected positional argument: %s", positionals[0])
	}
	values := requestValuesFromOptions(cmd, consumed)
	explicitBody, hasExplicit, err := jsonBodyFromOptions(cmd, stdin)
	if err != nil {
		return "", requestSpec{}, err
	}
	requestBody := route.RequestBody
	if requestBody == "" {
		requestBody = defaultRequestBody(route.Method)
	}
	if requestBody == "multipart" {
		return "", requestSpec{}, fmt.Errorf("operation %s expects multipart request bodies, which generic do does not support yet", route.ID)
	}
	if requestBody == "none" && hasExplicit {
		return "", requestSpec{}, fmt.Errorf("operation %s does not accept a JSON body", route.ID)
	}
	spec := requestSpec{Headers: requestHeadersFromOptions(cmd)}
	if route.Method == http.MethodGet || (route.Method == http.MethodDelete && requestBody == "none") || requestBody == "none" {
		return appendQuery(path, values), spec, nil
	}
	if hasExplicit {
		spec.JSONBody = explicitBody
	} else {
		spec.JSONBody = compact(values)
	}
	return path, spec, nil
}

func rawRequestFromOptions(method string, cmd command, stdin io.Reader) (requestSpec, error) {
	body, hasBody, err := jsonBodyFromOptions(cmd, stdin)
	if err != nil {
		return requestSpec{}, err
	}
	if !hasBody && len(cmd.Positionals) > 0 {
		if err := json.Unmarshal([]byte(strings.Join(cmd.Positionals, " ")), &body); err != nil {
			return requestSpec{}, err
		}
		hasBody = true
	}
	spec := requestSpec{Headers: requestHeadersFromOptions(cmd)}
	if hasBody && method != http.MethodGet {
		spec.JSONBody = body
	}
	return spec, nil
}

func requestHeadersFromOptions(cmd command) http.Header {
	headers := http.Header{}
	accept := cmd.string("accept", "")
	if accept == "" {
		accept = acceptFromFormat(cmd.string("format", ""))
	}
	if accept != "" {
		headers.Set("Accept", accept)
	}
	return headers
}

func jsonBodyFromOptions(cmd command, stdin io.Reader) (any, bool, error) {
	jsonValue := firstNonEmpty(cmd.string("json", ""), cmd.string("body-json", ""))
	file := firstNonEmpty(cmd.string("json-file", ""), cmd.string("body-file", ""))
	if jsonValue != "" && file != "" {
		return nil, false, fmt.Errorf("use either --json/--body-json or --json-file/--body-file, not both")
	}
	var bytes []byte
	var err error
	if jsonValue == "-" {
		bytes, err = io.ReadAll(stdin)
	} else if jsonValue != "" {
		bytes = []byte(jsonValue)
	} else if file != "" {
		bytes, err = os.ReadFile(filepath.Clean(file))
	} else {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var parsed any
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		return nil, false, err
	}
	return parsed, true, nil
}

type responsePayload struct {
	Parsed any
	Text   string
}

func readPayload(response *http.Response, method string, path string) (responsePayload, error) {
	defer func() { _ = response.Body.Close() }()
	bytes, err := io.ReadAll(response.Body)
	if err != nil {
		return responsePayload{}, err
	}
	text := string(bytes)
	var parsed any
	if text != "" && strings.Contains(response.Header.Get("Content-Type"), "json") {
		if err := json.Unmarshal(bytes, &parsed); err != nil {
			return responsePayload{}, err
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responsePayload{}, fmt.Errorf("%s %s failed with %d: %s", method, path, response.StatusCode, text)
	}
	return responsePayload{Parsed: parsed, Text: text}, nil
}

func printPayload(stdout io.Writer, payload responsePayload, cmd command) error {
	format := cmd.string("format", "")
	if format == "" {
		if payload.Parsed == nil {
			format = "text"
		} else {
			format = "json"
		}
	}
	switch format {
	case "none":
		return nil
	case "text", "raw":
		if _, err := fmt.Fprint(stdout, payload.Text); err != nil {
			return err
		}
		if payload.Text != "" && !strings.HasSuffix(payload.Text, "\n") {
			_, err := fmt.Fprintln(stdout)
			return err
		}
		return nil
	case "ndjson":
		bytes, err := json.Marshal(payload.Parsed)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(bytes))
		return err
	case "json":
		return printJSON(stdout, payload.Parsed)
	default:
		return fmt.Errorf("unknown output format: %s", format)
	}
}

func requestValuesFromOptions(cmd command, consumed map[string]bool) map[string]any {
	generic := map[string]bool{
		"accept": true, "base-url": true, "body-file": true, "body-json": true, "format": true,
		"json": true, "json-file": true, "log-file": true, "profile": true, "save-token-profile": true,
		"token": true, "bearer-token": true,
		"compact": true, "fields": true, "output": true, "pretty": true,
	}
	values := map[string]any{}
	for key, value := range cmd.Options {
		if generic[key] || consumed[key] {
			continue
		}
		values[camelCase(key)] = value
	}
	for key, value := range cmd.Flags {
		if !value || generic[key] || consumed[key] {
			continue
		}
		values[camelCase(key)] = true
	}
	return values
}

func appendQuery(path string, values map[string]any) string {
	params := url.Values{}
	for key, value := range values {
		if value == nil {
			continue
		}
		text := fmt.Sprint(value)
		if text != "" {
			params.Add(key, text)
		}
	}
	if len(params) == 0 {
		return path
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + params.Encode()
}

func optionValue(cmd command, name string) (string, bool) {
	for _, alias := range optionAliases(name) {
		if value, ok := cmd.Options[alias]; ok {
			return value, true
		}
		if value, ok := cmd.Flags[alias]; ok && value {
			return "true", true
		}
	}
	return "", false
}

func optionAliases(name string) []string {
	kebab := kebabCase(name)
	if kebab == name {
		return []string{name}
	}
	return []string{name, kebab}
}

func acceptFromFormat(format string) string {
	switch format {
	case "text", "raw":
		return "text/plain, */*"
	case "json", "ndjson", "":
		return "application/json"
	default:
		return ""
	}
}

func camelCase(name string) string {
	parts := strings.Split(name, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func kebabCase(name string) string {
	var out strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteByte('-')
		}
		out.WriteRune(r)
	}
	return strings.ToLower(out.String())
}

func defaultRequestBody(method string) string {
	if method == http.MethodGet || method == http.MethodDelete {
		return "none"
	}
	return "json"
}
