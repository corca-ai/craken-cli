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

func TestImportTokenStoresReusableProfile(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer stored-token" {
			t.Fatalf("unexpected auth header %q", got)
		}
		if r.URL.Path != "/api/workspaces" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{"workspaces": []any{}})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"auth", "import-token", "--profile", "stored", "--token", "stored-token", "--base-url", server.URL}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), "dev", []string{"workspace", "list", "--profile", "stored"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "workspaces") {
		t.Fatalf("expected workspace JSON, got %s", stdout.String())
	}
}

func TestHelpRendersServerCatalogWithOptionalBearer(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	var seenAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuthorization = r.Header.Get("Authorization")
		if r.URL.Path != "/api/client" {
			t.Fatalf("unexpected help path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"commands": []map[string]any{{
				"command":     "craken custom run --name NAME",
				"description": "Run the server-provided command",
				"examples":    []string{"craken custom run --name demo"},
				"group":       "Custom",
				"id":          "custom.run",
				"operationId": "custom.operation",
			}},
			"examples": []map[string]any{{
				"command":     "craken do custom.operation",
				"description": "Run the server-provided example",
			}},
			"routes": []map[string]any{{
				"auth":        "required",
				"description": "Run a custom operation",
				"id":          "custom.operation",
				"method":      "POST",
				"path":        "/api/custom",
				"requestBody": "json",
			}},
			"schemaVersion": 1,
			"shortcuts": []map[string]any{{
				"actions":     []string{"run", "inspect"},
				"description": "Custom shortcuts",
				"resource":    "custom",
			}},
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"--base-url", server.URL}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	for _, expected := range []string{
		"Server commands:",
		"Custom:",
		"craken custom run --name NAME",
		"Run the server-provided command",
		"e.g. craken custom run --name demo",
		"custom.operation\tPOST\t/api/custom\tRun a custom operation",
		"craken do OPERATION_ID",
		"craken auth login",
		"craken commands --format text",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("expected help to contain %q, got:\n%s", expected, help)
		}
	}
	if seenAuthorization != "" {
		t.Fatalf("expected anonymous catalog request, got auth %q", seenAuthorization)
	}
	for _, stale := range []string{
		"Server-advertised shortcuts:",
		"custom run|inspect",
		"craken auth login --profile PROFILE",
		"craken commands --profile PROFILE --format text",
		"workspace list|get|create|delete",
		"workspace|channel|dm|file|folder|wiki|agent|dream",
	} {
		if strings.Contains(help, stale) {
			t.Fatalf("expected help not to hard-code operation list %q, got:\n%s", stale, help)
		}
	}
}

func TestFocusedHelpRendersServerCommandLocalOptionsAndMetadata(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client" {
			t.Fatalf("unexpected help path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"commands": []map[string]any{
				{
					"command":     "craken channel messages --workspace WORKSPACE --channel CHANNEL [--after MESSAGE_ID] [--before MESSAGE_ID] [--around MESSAGE_ID] [--position latest|start]",
					"description": "List channel messages with cursor pagination.",
					"examples":    []string{"craken channel messages --workspace W --channel C --after MESSAGE_ID"},
					"group":       "Channel",
					"id":          "channel.messages",
					"operationId": "channels.messages.list",
				},
				{
					"command":     "craken workspace activity --workspace WORKSPACE [--limit N]",
					"description": "Read workspace activity.",
					"group":       "Workspace",
					"id":          "workspace.activity",
					"operationId": "workspaces.activity",
				},
				{
					"command":     "craken wiki recent --workspace WORKSPACE",
					"description": "List recent wiki changes.",
					"group":       "Wiki",
					"id":          "wiki.recent",
					"operationId": "wiki.recent-changes",
				},
			},
			"routes": []map[string]any{
				{
					"auth":        "required",
					"description": "List channel messages. Response cursors are message ids.",
					"id":          "channels.messages.list",
					"method":      "GET",
					"path":        "/api/workspaces/{workspaceId}/channels/{channelId}/messages",
					"queryParameters": []map[string]any{
						{"description": "Read newer messages after this message id.", "name": "after", "type": "string"},
						{"description": "Read older messages before this message id.", "name": "before", "type": "string"},
					},
					"requestBody": "none",
					"responseExample": map[string]any{
						"messages":     []map[string]any{{"id": "message-1"}},
						"newestCursor": "message-1",
					},
				},
				{
					"auth":        "required",
					"description": "Read workspace activity.",
					"id":          "workspaces.activity",
					"method":      "GET",
					"path":        "/api/workspaces/{workspaceId}/activity",
					"requestBody": "none",
					"responseExample": map[string]any{
						"activities": []map[string]any{{"sequence": 1}},
					},
				},
				{
					"auth":        "required",
					"description": "List wiki recent changes.",
					"id":          "wiki.recent-changes",
					"method":      "GET",
					"path":        "/api/workspaces/{workspaceId}/wiki/recent-changes",
					"requestBody": "none",
				},
			},
			"schemaVersion": 1,
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"channel", "messages", "--base-url", server.URL, "--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	channelHelp := stdout.String()
	for _, expected := range []string{
		"Usage:",
		"craken channel messages --workspace WORKSPACE --channel CHANNEL",
		"--after MESSAGE_ID",
		"--before MESSAGE_ID",
		"--around MESSAGE_ID",
		"--position latest|start",
		"Query parameters:",
		"Response example:",
		"newestCursor",
		"e.g.",
	} {
		if !strings.Contains(channelHelp, expected) {
			t.Fatalf("expected focused channel help to contain %q, got:\n%s", expected, channelHelp)
		}
	}
	if strings.Contains(channelHelp, "Server commands:") {
		t.Fatalf("expected focused help, got global help:\n%s", channelHelp)
	}

	stdout.Reset()
	if err := Run(context.Background(), "dev", []string{"workspace", "activity", "--base-url", server.URL, "--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	workspaceHelp := stdout.String()
	for _, expected := range []string{"--anchor-json JSON", "--before-sequence N", "--surfaces LIST", "activities"} {
		if !strings.Contains(workspaceHelp, expected) {
			t.Fatalf("expected focused workspace help to contain %q, got:\n%s", expected, workspaceHelp)
		}
	}

	stdout.Reset()
	if err := Run(context.Background(), "dev", []string{"wiki", "recent", "--base-url", server.URL, "--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if help := stdout.String(); !strings.Contains(help, "--limit N") {
		t.Fatalf("expected focused wiki help to contain --limit N, got:\n%s", help)
	}
}

func TestFocusedHelpShowsChannelWaitOptions(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client" {
			t.Fatalf("unexpected help path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"commands": []map[string]any{{
				"command":     "craken channel wait --workspace WORKSPACE --channel CHANNEL [--after MESSAGE_ID] [--timeout-ms MS]",
				"description": "Wait for the next channel message after a cursor.",
				"examples":    []string{"craken channel wait --workspace W --channel C --after MESSAGE_ID --timeout-ms 60000"},
				"group":       "Channel",
				"id":          "channel.wait",
				"operationId": "channels.messages.wait",
			}},
			"routes": []map[string]any{{
				"auth":        "required",
				"description": "Wait up to timeoutMs for the next channel message. Returns { message, timedOut }.",
				"id":          "channels.messages.wait",
				"method":      "GET",
				"path":        "/api/workspaces/{workspaceId}/channels/{channelId}/messages/wait",
				"requestBody": "none",
			}},
			"schemaVersion": 1,
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"channel", "wait", "--base-url", server.URL, "--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	for _, expected := range []string{
		"craken channel wait --workspace WORKSPACE --channel CHANNEL",
		"--after MESSAGE_ID",
		"--timeout-ms MS",
		"channels.messages.wait\tGET\t/api/workspaces/{workspaceId}/channels/{channelId}/messages/wait",
		"e.g. craken channel wait --workspace W --channel C --after MESSAGE_ID --timeout-ms 60000",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("expected focused channel wait help to contain %q, got:\n%s", expected, help)
		}
	}
}

func TestCommandsTextRendersServerCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client" {
			t.Fatalf("unexpected commands path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"commands": []map[string]any{{
				"command":     "craken custom run --name NAME",
				"description": "Run the server-provided command",
				"group":       "Custom",
				"id":          "custom.run",
				"operationId": "custom.operation",
			}},
			"routes": []map[string]any{{
				"auth":        "required",
				"description": "Run a custom operation",
				"id":          "custom.operation",
				"method":      "POST",
				"path":        "/api/custom",
				"requestBody": "json",
			}},
			"schemaVersion": 1,
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"commands", "--base-url", server.URL, "--token", "test-token", "--format", "text"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	expected := "Custom\tcustom.run\tcraken custom run --name NAME\tRun the server-provided command\n"
	if stdout.String() != expected {
		t.Fatalf("expected commands text %q, got %q", expected, stdout.String())
	}
}

func TestWorkspaceAcceptUsesProfileBearerWhenTokenIsInvitationAlias(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	var seenAuth string
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		writeJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	cfg := config{Profiles: map[string]profile{"member": {BaseURL: server.URL, Token: "member-bearer-token"}}}
	if err := writeConfig(cfg); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), "dev", []string{"workspace", "accept", "--profile", "member", "--token=opaque-invitation-token"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if seenAuth != "Bearer member-bearer-token" {
		t.Fatalf("unexpected auth %q", seenAuth)
	}
	if seenPath != "/api/workspace-invitations/opaque-invitation-token/accept" {
		t.Fatalf("unexpected path %q", seenPath)
	}
}

func TestGenericDoUsesCatalogPathBodyAndSaveTokenProfile(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/client":
			writeJSON(t, w, map[string]any{
				"schemaVersion": 1,
				"routes": []map[string]any{{
					"auth":        "required",
					"description": "Create delegated session",
					"id":          "client.delegated-sessions.create",
					"method":      "POST",
					"path":        "/api/client/delegated-sessions",
					"requestBody": "json",
				}},
			})
		case "/api/client/delegated-sessions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["email"] != "actor@example.com" {
				t.Fatalf("unexpected body %#v", body)
			}
			writeJSON(t, w, map[string]any{"token": "delegated-token"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), "dev", []string{
		"do", "client.delegated-sessions.create",
		"--token", "operator-token",
		"--base-url", server.URL,
		"--email", "actor@example.com",
		"--save-token-profile", "actor",
	}, strings.NewReader(""), &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(requests, ",") != "GET /api/client,POST /api/client/delegated-sessions" {
		t.Fatalf("unexpected requests %#v", requests)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Profiles["actor"].Token; got != "delegated-token" {
		t.Fatalf("expected delegated token, got %q", got)
	}
}

func TestDeviceLoginStoresPolledToken(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()

	loginURLCh := make(chan string, 1)
	openBrowser = func(target string) {
		loginURLCh <- target
	}

	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/device-authorizations":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected authorization method %s", r.Method)
			}
			writeJSON(t, w, map[string]any{
				"deviceCode":              "device-code",
				"expiresIn":               30,
				"interval":                1,
				"userCode":                "ABCD-EFGH",
				"verificationUri":         serverURL(r) + "/api/client/device",
				"verificationUriComplete": serverURL(r) + "/api/client/device?user_code=ABCD-EFGH",
			})
		case "/api/client/device-token":
			polls++
			if polls == 1 {
				w.WriteHeader(http.StatusTooEarly)
				writeJSON(t, w, map[string]any{"error": "authorization_pending"})
				return
			}
			writeJSON(t, w, map[string]any{
				"session":   map[string]any{"email": "browser@example.com"},
				"token":     "browser-token",
				"tokenType": "Bearer",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(context.Background(), "dev", []string{"auth", "login", "--profile", "browser", "--base-url", server.URL, "--timeout-ms", "5000"}, strings.NewReader(""), &stdout, &stderr)
	}()

	loginURL := <-loginURLCh
	if loginURL != server.URL+"/api/client/device?user_code=ABCD-EFGH" {
		t.Fatalf("unexpected login URL %s", loginURL)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Profiles["browser"].Token; got != "browser-token" {
		t.Fatalf("expected browser token, got %q", got)
	}
	if !strings.Contains(stderr.String(), "Code: ABCD-EFGH") {
		t.Fatalf("expected user code in stderr, got %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "browser@example.com") {
		t.Fatalf("expected session in stdout, got %s", stdout.String())
	}
}

func TestDeviceLoginNoOpenPrintsVerificationURL(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()

	openBrowser = func(target string) {
		t.Fatalf("did not expect browser to open %s", target)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/device-authorizations":
			writeJSON(t, w, map[string]any{
				"deviceCode":              "device-code",
				"expiresIn":               30,
				"interval":                1,
				"userCode":                "WXYZ-2345",
				"verificationUri":         serverURL(r) + "/api/client/device",
				"verificationUriComplete": serverURL(r) + "/api/client/device?user_code=WXYZ-2345",
			})
		case "/api/client/device-token":
			writeJSON(t, w, map[string]any{
				"session":   map[string]any{"email": "manual@example.com"},
				"token":     "manual-token",
				"tokenType": "Bearer",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stderr bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"auth", "login", "--no-open", "--profile", "manual", "--base-url", server.URL, "--timeout-ms", "5000"}, strings.NewReader(""), &bytes.Buffer{}, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), server.URL+"/api/client/device?user_code=WXYZ-2345") {
		t.Fatalf("expected verification URL in stderr, got %s", stderr.String())
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Profiles["manual"].Token; got != "manual-token" {
		t.Fatalf("expected manual token, got %q", got)
	}
}

func TestRawPostReadsJSONFromFile(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	bodyPath := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(bodyPath, []byte(`{"name":"raw-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["name"] != "raw-test" {
			t.Fatalf("unexpected body %#v", body)
		}
		writeJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	err := Run(context.Background(), "dev", []string{"post", "/api/workspaces", "--token", "test-token", "--base-url", server.URL, "--json-file", bodyPath}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChannelSendResolvesWorkspaceChannelAndSender(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "GET /api/workspaces/workspace-id":
			writeJSON(t, w, map[string]any{
				"channels": []map[string]any{{"id": "channel-id", "name": "general"}},
				"members":  []map[string]any{},
			})
		case "POST /api/workspaces/workspace-id/channels/channel-id/messages":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), "dev", []string{
		"channel", "send",
		"--token", "test-token",
		"--base-url", server.URL,
		"--workspace", "test0",
		"--channel", "general",
		"--sender-email", "sender@example.com",
		"hello", "there",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if posted["body"] != "hello there" || posted["senderEmail"] != "sender@example.com" {
		t.Fatalf("unexpected posted body %#v", posted)
	}
}

func TestChannelWaitResolvesWorkspaceChannelAndPrintsWaitResponse(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "GET /api/workspaces/workspace-id":
			writeJSON(t, w, map[string]any{
				"channels": []map[string]any{{"id": "channel-id", "name": "general"}},
				"members":  []map[string]any{},
			})
		case "GET /api/workspaces/workspace-id/channels/channel-id/messages/wait":
			if got := r.URL.Query().Get("after"); got != "message-1" {
				t.Fatalf("unexpected after query %q", got)
			}
			if got := r.URL.Query().Get("timeoutMs"); got != "60000" {
				t.Fatalf("unexpected timeoutMs query %q", got)
			}
			writeJSON(t, w, map[string]any{
				"message":  map[string]any{"body": "ready", "id": "message-2"},
				"timedOut": false,
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), "dev", []string{
		"channel", "wait",
		"--token", "test-token",
		"--base-url", server.URL,
		"--workspace", "test0",
		"--channel", "general",
		"--after", "message-1",
		"--timeout-ms", "60000",
	}, strings.NewReader(""), &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, expected := range []string{`"body": "ready"`, `"timedOut": false`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestWikiSaveAndAgentPlanCheck(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	contentPath := filepath.Join(t.TempDir(), "home.md")
	if err := os.WriteFile(contentPath, []byte("# Home\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "PATCH /api/workspaces/workspace-id/wiki/pages/Home":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["title"] != "Home" || body["content"] != "# Home\n" || body["baseVersionNumber"] != float64(12) {
				t.Fatalf("unexpected wiki body %#v", body)
			}
			writeJSON(t, w, map[string]any{"page": body})
		case "POST /api/admin/agent-job-plan/progression":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["nextPlan"] == nil {
				t.Fatalf("missing nextPlan %#v", body)
			}
			writeJSON(t, w, map[string]any{"ok": true, "violations": []any{}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), "dev", []string{"wiki", "save", "--token", "test-token", "--base-url", server.URL, "--workspace", "test0", "--existing-title", "Home", "--title", "Home", "--content-file", contentPath, "--base-version", "12"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), "dev", []string{"agent", "plan-check", "--token", "test-token", "--base-url", server.URL, "--next-json", `{"status":"completed"}`}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("unexpected request count %d: %#v", len(seen), seen)
	}
}

func TestWikiSaveReportsBaseVersionConflict(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "PATCH /api/workspaces/workspace-id/wiki/pages/Home":
			w.WriteHeader(http.StatusConflict)
			writeJSON(t, w, map[string]any{
				"conflict": map[string]any{
					"currentVersionNumber":      13,
					"receivedBaseVersionNumber": 12,
					"reason":                    "stale_base_version",
				},
				"error": "Wiki page update for \"Home\" conflicted: baseVersionNumber 12 does not match current version 13.",
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), "dev", []string{"wiki", "save", "--token", "test-token", "--base-url", server.URL, "--workspace", "test0", "--existing-title", "Home", "--content", "stale", "--base-version", "12"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected stale wiki save to fail")
	}
	if message := err.Error(); !strings.Contains(message, "failed with 409") || !strings.Contains(message, "stale_base_version") {
		t.Fatalf("expected 409 conflict details, got %q", message)
	}
}

func TestFileUploadUsesMultipartAndFolderCreate(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	uploadPath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(uploadPath, []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "POST /api/workspaces/workspace-id/files":
			if err := r.ParseMultipartForm(1024 * 1024); err != nil {
				t.Fatal(err)
			}
			if got := r.FormValue("scope"); got != "workspace" {
				t.Fatalf("unexpected scope %q", got)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			writeJSON(t, w, map[string]any{"ok": true})
		case "POST /api/workspaces/workspace-id/folders":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["name"] != "research" {
				t.Fatalf("unexpected folder body %#v", body)
			}
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{"file", "upload", "--token", "test-token", "--base-url", server.URL, "--workspace", "test0", "--path", uploadPath}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), "dev", []string{"folder", "create", "--token", "test-token", "--base-url", server.URL, "--workspace", "test0", "--name", "research"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestRealtimeFormattingHelpers(t *testing.T) {
	items, err := realtimeItems([]byte(`{"activities":[{"event":{"type":"message.created","conversation":{"kind":"channel","channel":{"name":"general"}},"message":{"body":"hi","sender":{"name":"Orca"}}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := printRealtimeItem(&stdout, items[0], command{Flags: map[string]bool{"pretty": true}}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "message.created #general Orca: hi") {
		t.Fatalf("unexpected pretty output %q", got)
	}
	protocols := bearerProtocols("token-value")
	if len(protocols) != 2 || protocols[0] != "craken-bearer" || !strings.HasPrefix(protocols[1], "craken-bearer-payload.") {
		t.Fatalf("unexpected protocols %#v", protocols)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
