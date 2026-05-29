package craken

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionFlagPrintsVersionWithoutNetwork pins that every documented version
// form prints the version and exits without touching the network. Before the
// fix, `--version` was parsed into cmd.Flags and fell through to the catalog
// help path, which dials /api/client.
func TestVersionFlagPrintsVersionWithoutNetwork(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	for _, arg := range []string{"version", "-version", "--version"} {
		var stdout, stderr bytes.Buffer
		// An unroutable base-url makes the buggy help/network path fail fast.
		err := Run(context.Background(), "v1.2.3", []string{arg, "--base-url", "http://127.0.0.1:0"}, strings.NewReader(""), &stdout, &stderr)
		if err != nil {
			t.Fatalf("arg %q: unexpected error %v", arg, err)
		}
		if stdout.String() != "v1.2.3\n" {
			t.Fatalf("arg %q: expected stdout %q, got %q", arg, "v1.2.3\n", stdout.String())
		}
	}
}

// TestRawVerbWithoutPathErrorsAndIssuesNoRequest pins that a raw HTTP verb with
// no path errors instead of sending a request to the synthesized "help"
// sentinel path (e.g. POST /help).
func TestRawVerbWithoutPathErrorsAndIssuesNoRequest(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		writeJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	for _, verb := range []string{"get", "post", "put", "patch", "delete"} {
		requests = nil
		err := Run(context.Background(), "dev", []string{verb, "--token", "t", "--base-url", server.URL}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "expected API path") {
			t.Fatalf("%s: expected \"expected API path\" error, got %v", verb, err)
		}
		if len(requests) != 0 {
			t.Fatalf("%s: expected no request, got %v", verb, requests)
		}
	}
}

// TestAPIWithoutMethodDoesNotSendHelpMethod pins that `craken api` with no HTTP
// method errors instead of fabricating the bogus "HELP" method from the
// default-action sentinel.
func TestAPIWithoutMethodDoesNotSendHelpMethod(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var recorded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded = r.Method + " " + r.URL.Path
		writeJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	err := Run(context.Background(), "dev", []string{"api", "--path", "/api/foo", "--token", "t", "--base-url", server.URL}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error when the api method is omitted")
	}
	if recorded == "HELP /api/foo" {
		t.Fatalf("client sent a bogus HELP method: %q", recorded)
	}
}

// TestBoolOptionHonorsExplicitValue pins that an inline boolean value is parsed
// rather than treated as "present == true". Before the fix `--flag=false`
// (stored in Options, not Flags) read as true.
func TestBoolOptionHonorsExplicitValue(t *testing.T) {
	cases := []struct {
		args []string
		name string
		want bool
	}{
		{[]string{"x", "y", "--no-open"}, "no-open", true},
		{[]string{"x", "y", "--no-open=true"}, "no-open", true},
		{[]string{"x", "y", "--no-open=1"}, "no-open", true},
		{[]string{"x", "y", "--no-open=false"}, "no-open", false},
		{[]string{"x", "y", "--no-open=0"}, "no-open", false},
		{[]string{"x", "y", "--no-open=off"}, "no-open", false},
		{[]string{"x", "y"}, "no-open", false},
	}
	for _, tc := range cases {
		cmd, err := parseCommand(tc.args)
		if err != nil {
			t.Fatalf("parseCommand(%v): %v", tc.args, err)
		}
		if got := boolOption(cmd, tc.name); got != tc.want {
			t.Fatalf("boolOption(%v, %q) = %t, want %t", tc.args, tc.name, got, tc.want)
		}
	}
}

// TestChannelMessagesCompactFalseProducesFullJSON is the end-to-end counterpart:
// `--compact=false` must emit full JSON, not the compact table.
func TestChannelMessagesCompactFalseProducesFullJSON(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	picture := strings.Repeat("picture-data", 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "GET /api/workspaces/workspace-id":
			writeJSON(t, w, map[string]any{"channels": []map[string]any{{"id": "channel-id", "name": "general"}}, "members": []map[string]any{}})
		case "GET /api/workspaces/workspace-id/channels/channel-id/messages":
			writeJSON(t, w, map[string]any{"messages": []map[string]any{{
				"body": "hi", "createdAt": "2026-05-21T12:00:00.000Z", "id": "message-1",
				"sender": map[string]any{"id": "user:ada", "name": "Ada", "picture": picture},
			}}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{
		"channel", "messages", "--token", "test-token", "--base-url", server.URL,
		"--workspace", "test0", "--channel", "general", "--compact=false",
	}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "picture-data") {
		t.Fatalf("expected full JSON output, got:\n%s", stdout.String())
	}
}

// TestGenericDoSendsExplicitEmptyStringBodyField pins that a body field the user
// explicitly set to "" round-trips into the request body. Before the fix
// compact() dropped empty strings, so `--name ""` sent {} instead of {"name":""}.
func TestGenericDoSendsExplicitEmptyStringBodyField(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var body map[string]any
	gotPost := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/client":
			writeJSON(t, w, map[string]any{"schemaVersion": 1, "routes": []map[string]any{{
				"auth": "required", "description": "Create a thing", "id": "things.create",
				"method": "POST", "path": "/api/things", "requestBody": "json",
			}}})
		case "POST /api/things":
			gotPost = true
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{
		"do", "things.create", "--token", "t", "--base-url", server.URL, "--name", "",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !gotPost {
		t.Fatal("POST /api/things was never reached")
	}
	value, ok := body["name"]
	if !ok {
		t.Fatalf("expected name key present in body, got %#v", body)
	}
	if value != "" {
		t.Fatalf("expected empty string body value, got %#v", value)
	}
}

// TestUserLoginClearsStaleAgentMetadata pins that re-logging a profile as a user
// clears any prior agent labeling, so the stored kind always matches the stored
// token. Before the fix the new user token kept the old kind=agent/agentId,
// which whoami flags as "writes would post under the user identity, not agent".
func TestUserLoginClearsStaleAgentMetadata(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()
	openBrowser = func(target string) { t.Fatalf("did not expect browser open %s", target) }

	if err := writeConfig(config{Profiles: map[string]profile{"codex": {
		Kind: "agent", AgentID: "agent-id", AgentName: "Codex", ClientKind: "codex", WorkspaceID: "ws", Token: "old",
	}}}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/device-authorizations":
			writeJSON(t, w, map[string]any{
				"deviceCode": "device-code", "expiresIn": 30, "interval": 1, "userCode": "WXYZ-2345",
				"verificationUri": serverURL(r) + "/api/client/device",
			})
		case "/api/client/device-token":
			writeJSON(t, w, map[string]any{
				"session": map[string]any{"email": "manual@example.com"}, "token": "fresh-user-token", "tokenType": "Bearer",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{
		"auth", "login", "--no-open", "--profile", "codex", "--base-url", server.URL, "--timeout-ms", "5000",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["codex"]
	if p.Token != "fresh-user-token" {
		t.Fatalf("expected fresh user token, got %q", p.Token)
	}
	if p.Kind == "agent" || p.AgentID != "" || p.AgentName != "" || p.ClientKind != "" || p.WorkspaceID != "" {
		t.Fatalf("expected agent metadata cleared, got %#v", p)
	}
}

// TestChannelSendBodyFileReadsPlaintext pins that a catalog text binding's
// fileOption (channel send --body-file) is read as plaintext. Before the fix the
// generic JSON body handler claimed --body-file, tried to JSON-parse the message
// file, and failed before any message was sent.
func TestChannelSendBodyFileReadsPlaintext(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	bodyPath := filepath.Join(t.TempDir(), "msg.txt")
	if err := os.WriteFile(bodyPath, []byte("hello from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posted map[string]any
	gotPost := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "GET /api/workspaces/workspace-id":
			writeJSON(t, w, map[string]any{"channels": []map[string]any{{"id": "channel-id", "name": "general"}}, "members": []map[string]any{}})
		case "POST /api/workspaces/workspace-id/channels/channel-id/messages":
			gotPost = true
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{
		"channel", "send", "--token", "test-token", "--base-url", server.URL,
		"--workspace", "test0", "--channel", "general", "--body-file", bodyPath,
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !gotPost {
		t.Fatal("message POST never fired")
	}
	if posted["body"] != "hello from file" {
		t.Fatalf("unexpected posted body %#v", posted)
	}
}

// TestWikiSaveContentValueAndFileConflictReportsMutualExclusion pins that passing
// both the value and file form of a required text binding surfaces the
// descriptive "not both" error instead of masking it with "expected --content".
func TestWikiSaveContentValueAndFileConflictReportsMutualExclusion(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), "dev", []string{
		"wiki", "save", "--token", "test-token", "--base-url", server.URL,
		"--workspace", "test0", "--title", "Home", "--content", "inline", "--content-file", "home.md",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected mutual-exclusion error")
	}
	if !strings.Contains(err.Error(), "use either --content or --content-file, not both") {
		t.Fatalf("expected mutual-exclusion message, got %q", err.Error())
	}
}

// TestCatalogPostCarriesQueryParamsAlongsideBody pins that a POST command that
// declares both query params and body fields sends both. Before the fix the
// query values were computed, marked consumed, then dropped on non-GET routes.
func TestCatalogPostCarriesQueryParamsAlongsideBody(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var seenQuery string
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/client":
			writeJSON(t, w, map[string]any{
				"schemaVersion": 1,
				"commands": []map[string]any{{
					"command": "craken thing create", "description": "Create a thing", "group": "Test",
					"id": "thing.create", "operationId": "things.create",
					"execution": map[string]any{
						"operationId": "things.create",
						"queryParams": map[string]any{"dryRun": map[string]any{"source": "option", "option": "dry-run"}},
						"bodyFields":  map[string]any{"name": map[string]any{"source": "option", "option": "name", "required": true}},
					},
				}},
				"routes": []map[string]any{{
					"auth": "required", "description": "Create a thing", "id": "things.create",
					"method": "POST", "path": "/api/things", "requestBody": "json",
				}},
			})
		case "POST /api/things":
			seenQuery = r.URL.RawQuery
			if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{
		"thing", "create", "--token", "test-token", "--base-url", server.URL, "--name", "widget", "--dry-run", "true",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenQuery, "dryRun=true") {
		t.Fatalf("expected dryRun query param, got %q", seenQuery)
	}
	if seenBody["name"] != "widget" {
		t.Fatalf("expected name body field, got %#v", seenBody)
	}
}

// TestCatalogJSONQueryParamSerializedAsJSON pins that a type:json query param is
// sent as JSON. Before the fix appendQuery rendered the parsed value with
// fmt.Sprint, producing Go map syntax like "map[sequence:5 surface:channel]".
func TestCatalogJSONQueryParamSerializedAsJSON(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var receivedAnchor string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/client":
			writeJSON(t, w, map[string]any{
				"schemaVersion": 1,
				"commands": []map[string]any{{
					"command": "craken workspace activity --workspace WORKSPACE", "description": "Read workspace activity.",
					"group": "Workspace", "id": "workspace.activity", "operationId": "workspaces.activity",
					"execution": map[string]any{
						"operationId": "workspaces.activity",
						"pathParams":  map[string]any{"workspaceId": map[string]any{"source": "option", "option": "workspace", "required": true}},
						"queryParams": map[string]any{"anchor": map[string]any{"source": "option", "option": "anchor-json", "aliases": []string{"anchor"}, "type": "json"}},
					},
				}},
				"routes": []map[string]any{{
					"auth": "required", "description": "Read workspace activity.", "id": "workspaces.activity",
					"method": "GET", "path": "/api/workspaces/{workspaceId}/activity", "requestBody": "none",
				}},
			})
		case "GET /api/workspaces/ws-1/activity":
			receivedAnchor = r.URL.Query().Get("anchor")
			writeJSON(t, w, map[string]any{"activities": []any{}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{
		"workspace", "activity", "--token", "test-token", "--base-url", server.URL,
		"--workspace", "ws-1", "--anchor-json", `{"sequence":5,"surface":"channel"}`,
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(receivedAnchor, "map[") {
		t.Fatalf("anchor sent as Go map syntax, not JSON: %q", receivedAnchor)
	}
	var decoded any
	if err := json.Unmarshal([]byte(receivedAnchor), &decoded); err != nil {
		t.Fatalf("anchor query is not valid JSON (%q): %v", receivedAnchor, err)
	}
}

// TestCatalogPollDoesNotLeakControlFlags pins that client-only poll controls
// (--verbose, --interval, --max-polls) are not forwarded to the server when a
// poll command declares no query params.
func TestCatalogPollDoesNotLeakControlFlags(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client":
			writeJSON(t, w, map[string]any{
				"schemaVersion": 1,
				"commands": []map[string]any{{
					"command": "craken job watch", "description": "Watch a job.", "group": "Job",
					"id": "job.watch", "operationId": "jobs.get",
					"execution": map[string]any{
						"operationId": "jobs.get",
						"poll": map[string]any{
							"defaultMaxPolls": 1, "intervalOption": "interval", "maxPollsOption": "max-polls",
							"statusPath": "status", "terminalValues": []string{"done"},
						},
					},
				}},
				"routes": []map[string]any{{
					"auth": "required", "description": "Get a job.", "id": "jobs.get",
					"method": "GET", "path": "/api/jobs", "requestBody": "none",
				}},
			})
		case "/api/jobs":
			seenQuery = r.URL.RawQuery
			writeJSON(t, w, map[string]any{"status": "done"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{
		"job", "watch", "--base-url", server.URL, "--token", "test-token", "--verbose", "--interval", "5", "--max-polls", "3",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"verbose", "interval", "maxPolls"} {
		if strings.Contains(seenQuery, leaked) {
			t.Fatalf("control flag %q leaked into query %q", leaked, seenQuery)
		}
	}
}

// TestCatalogDownloadAppendsQueryParams pins that a download command forwards its
// declared query params, the same way the HTTP and websocket transports do.
func TestCatalogDownloadAppendsQueryParams(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/client":
			writeJSON(t, w, map[string]any{
				"schemaVersion": 1,
				"commands": []map[string]any{{
					"command": "craken file download --workspace WORKSPACE --variant VARIANT", "description": "Download a file.",
					"group": "File", "id": "file.download", "operationId": "files.download",
					"execution": map[string]any{
						"operationId": "files.download", "transport": "download",
						"pathParams":  map[string]any{"workspaceId": workspaceOptionBinding()},
						"queryParams": map[string]any{"variant": map[string]any{"source": "option", "option": "variant"}},
					},
				}},
				"routes": []map[string]any{
					testRoute("workspaces.list", http.MethodGet, "/api/workspaces", "none"),
					testRoute("files.download", http.MethodGet, "/api/workspaces/{workspaceId}/files", "none"),
				},
			})
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "GET /api/workspaces/workspace-id/files":
			seenQuery = r.URL.RawQuery
			if _, err := w.Write([]byte("BINARY")); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{
		"file", "download", "--token", "test-token", "--base-url", server.URL, "--workspace", "test0", "--variant", "thumbnail",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenQuery, "variant=thumbnail") {
		t.Fatalf("expected download to send variant query, got %q", seenQuery)
	}
}
