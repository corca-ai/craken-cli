package craken

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestBrowserLoginStoresCallbackToken(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()

	loginURLCh := make(chan string, 1)
	openBrowser = func(target string) {
		loginURLCh <- target
	}

	var stdout bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(context.Background(), "dev", []string{"auth", "login", "--profile", "browser", "--base-url", "https://craken.example", "--timeout-ms", "5000"}, strings.NewReader(""), &stdout, &bytes.Buffer{})
	}()

	loginURL := <-loginURLCh
	parsed, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	redirectURI := parsed.Query().Get("redirect_uri")
	state := parsed.Query().Get("state")
	response, err := http.PostForm(redirectURI, url.Values{
		"session":   {`{"email":"browser@example.com"}`},
		"state":     {state},
		"token":     {"browser-token"},
		"tokenType": {"Bearer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
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
		case "POST /api/workspaces/workspace-id/wiki/pages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["title"] != "Home" || body["content"] != "# Home\n" {
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

	err := Run(context.Background(), "dev", []string{"wiki", "save", "--token", "test-token", "--base-url", server.URL, "--workspace", "test0", "--title", "Home", "--content-file", contentPath}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
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
