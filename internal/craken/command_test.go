package craken

import (
	"bytes"
	"context"
	"encoding/base64"
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
		if writeTestCatalog(t, w, r) {
			return
		}
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

// A bare resource (no action token) resolves the server-advertised defaultAction
// from the catalog shortcut. The workspace shortcut declares defaultAction "list",
// so `craken workspace` runs workspace.list and hits GET /api/workspaces without
// the binary hard-coding the workspace resource or its default action.
func TestBareResourceResolvesCatalogDefaultAction(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	var listedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		listedPath = r.Method + " " + r.URL.Path
		writeJSON(t, w, map[string]any{"workspaces": []any{}})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	// A bare resource with no stored credentials still resolves the catalog default
	// action; the catalog request is anonymous and the list call carries the
	// explicit --token, mirroring the other token-driven shortcut tests.
	if err := Run(context.Background(), "dev", []string{"workspace", "--token", "test-token", "--base-url", server.URL}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if listedPath != "GET /api/workspaces" {
		t.Fatalf("expected bare `workspace` to resolve defaultAction list and GET /api/workspaces, got %q", listedPath)
	}
	if !strings.Contains(stdout.String(), "workspaces") {
		t.Fatalf("expected workspace list JSON, got %s", stdout.String())
	}
}

func TestCatalogDefaultAction(t *testing.T) {
	shortcuts := []shortcut{
		{Resource: "workspace", Actions: []string{"list", "get"}, DefaultAction: "list", Description: "Workspace shortcuts"},
		{Resource: "channel", Actions: []string{"messages", "send"}, Description: "Channel shortcuts"},
	}
	cases := []struct {
		resource string
		want     string
	}{
		{resource: "workspace", want: "list"},
		{resource: "channel", want: ""},
		{resource: "unknown", want: ""},
	}
	for _, tc := range cases {
		if got := catalogDefaultAction(shortcuts, tc.resource); got != tc.want {
			t.Fatalf("catalogDefaultAction(%q) = %q, want %q", tc.resource, got, tc.want)
		}
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
			"help": map[string]any{
				"title":   "Example Product",
				"summary": "Example Product publishes this overview from its API catalog.",
				"sections": []map[string]any{
					{
						"title": "Getting started",
						"items": []map[string]any{{
							"command":     "craken custom run --name NAME",
							"description": "Run a custom product command discovered from the server.",
						}},
					},
					{
						"title": "Authentication",
						"body":  "Use the local device login command to store a bearer profile.",
					},
				},
			},
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
	// The default overview is compact: title, summary, the (fallback) next step,
	// a grouped command summary, and pointers to the fuller views.
	for _, expected := range []string{
		"Example Product",
		"Example Product publishes this overview from its API catalog.",
		"Next steps:",
		"craken auth login",
		"Server commands:",
		"Custom: run",
		"craken commands",
		"craken help --verbose",
		"craken help --format json",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("expected help to contain %q, got:\n%s", expected, help)
		}
	}
	if seenAuthorization != "" {
		t.Fatalf("expected anonymous catalog request, got auth %q", seenAuthorization)
	}
	// The compact view defers the full reference: no section bodies, per-command
	// descriptions/examples, local-flag block, or route dump leak into it.
	for _, deferred := range []string{
		"Getting started:",
		"Run a custom product command discovered from the server.",
		"Use the local device login command to store a bearer profile.",
		"Local client commands:",
		"Run the server-provided command",
		"e.g. craken custom run --name demo",
		"custom.operation\tPOST\t/api/custom",
		"Server operations:",
		"craken do OPERATION_ID",
	} {
		if strings.Contains(help, deferred) {
			t.Fatalf("expected compact help to defer %q to --verbose, got:\n%s", deferred, help)
		}
	}
}

func TestHelpResourceRendersCatalogHelp(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client" {
			t.Fatalf("unexpected help path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"help": map[string]any{
				"title":   "Catalog Help",
				"summary": "Server-owned overview.",
				"sections": []map[string]any{{
					"title": "Start",
					"items": []map[string]any{{"label": "First step", "description": "Read this first."}},
				}},
			},
			"routes":        []map[string]any{},
			"schemaVersion": 1,
		})
	}))
	defer server.Close()

	// The compact default shows the title and summary but defers the help sections
	// to the verbose reference.
	var compact bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"help", "--base-url", server.URL}, strings.NewReader(""), &compact, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Catalog Help", "Server-owned overview."} {
		if !strings.Contains(compact.String(), expected) {
			t.Fatalf("expected compact help to contain %q, got:\n%s", expected, compact.String())
		}
	}
	if strings.Contains(compact.String(), "Read this first.") {
		t.Fatalf("expected compact help to defer section bodies to --verbose, got:\n%s", compact.String())
	}

	// --verbose renders the full server help, including section items.
	var verbose bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"help", "--verbose", "--base-url", server.URL}, strings.NewReader(""), &verbose, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Catalog Help", "Server-owned overview.", "First step", "Read this first."} {
		if !strings.Contains(verbose.String(), expected) {
			t.Fatalf("expected verbose help to contain %q, got:\n%s", expected, verbose.String())
		}
	}
}

// statefulCatalogHandler serves a catalog carrying the server-owned auth and
// nextSteps guidance the compact and JSON help views render.
func statefulCatalogHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client" {
			t.Fatalf("unexpected help path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"auth": map[string]any{
				"status":   "agent",
				"identity": map[string]any{"email": "owner@example.com"},
				"agent":    map[string]any{"agentId": "agent_1", "clientKind": "codex", "workspaceId": "ws_x"},
			},
			"nextSteps": []map[string]any{
				{
					"title":       "Subscribe to the workspace",
					"command":     "craken workspace subs --workspace ws_x --pretty",
					"description": "Stream realtime events for synchronous collaboration.",
				},
				{
					"title":       "Wait for the next message",
					"command":     "craken channel wait --workspace ws_x --channel CHANNEL --after MESSAGE_ID --timeout-ms 60000",
					"description": "Bounded long-poll for an asynchronous agent loop.",
				},
			},
			"commands": []map[string]any{{
				"command":     "craken channel send --workspace WORKSPACE --channel CHANNEL MESSAGE",
				"description": "Send a channel message.",
				"group":       "Channel",
				"id":          "channel.send",
			}},
			"help":          map[string]any{"title": "Craken", "summary": "Collaborate with people and agents."},
			"routes":        []map[string]any{},
			"schemaVersion": 1,
		})
	}
}

func TestHelpCompactRendersAuthStatusAndNextSteps(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(statefulCatalogHandler(t))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"--base-url", server.URL}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	for _, expected := range []string{
		"Acting as a delegated agent in workspace ws_x (owner owner@example.com).",
		"Next steps:",
		"1. craken workspace subs --workspace ws_x --pretty",
		"Stream realtime events for synchronous collaboration.",
		"2. craken channel wait --workspace ws_x --channel CHANNEL --after MESSAGE_ID --timeout-ms 60000",
		"Server commands:",
		"Channel: send",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("expected compact help to contain %q, got:\n%s", expected, help)
		}
	}
}

func TestHelpFormatJSONEmitsGuidance(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(statefulCatalogHandler(t))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"help", "--format", "json", "--base-url", server.URL}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Auth struct {
			Status string `json:"status"`
			Agent  struct {
				WorkspaceID string `json:"workspaceId"`
			} `json:"agent"`
		} `json:"auth"`
		NextSteps []struct {
			Command string `json:"command"`
		} `json:"nextSteps"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("expected JSON guidance, got error %v and output:\n%s", err, stdout.String())
	}
	if out.Auth.Status != "agent" || out.Auth.Agent.WorkspaceID != "ws_x" {
		t.Fatalf("expected agent auth in JSON, got %+v", out.Auth)
	}
	if len(out.NextSteps) == 0 || out.NextSteps[0].Command != "craken workspace subs --workspace ws_x --pretty" {
		t.Fatalf("expected nextSteps in JSON, got %+v", out.NextSteps)
	}
	if out.Summary == "" {
		t.Fatalf("expected summary in JSON, got:\n%s", stdout.String())
	}
}

func TestHelpVerboseRendersFullReference(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			"help": map[string]any{"title": "Craken", "summary": "Overview."},
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
	if err := Run(context.Background(), "dev", []string{"help", "--verbose", "--base-url", server.URL}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	for _, expected := range []string{
		"Local client commands:",
		"craken auth login",
		"Server commands:",
		"Run the server-provided command",
		"e.g. craken custom run --name demo",
		"Server operations:",
		"custom.operation\tPOST\t/api/custom\tRun a custom operation",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("expected verbose help to contain %q, got:\n%s", expected, help)
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
					"execution":   map[string]any{"operationId": "channels.messages.list", "output": messagesOutputPlan()},
					"examples":    []string{"craken channel messages --workspace W --channel C --after MESSAGE_ID"},
					"group":       "Channel",
					"id":          "channel.messages",
					"operationId": "channels.messages.list",
				},
				{
					"command":     "craken workspace activity --workspace WORKSPACE [--limit N]",
					"description": "Read workspace activity.",
					"execution": map[string]any{
						"operationId": "workspaces.activity",
						"queryParams": map[string]any{
							"anchor":         map[string]any{"source": "option", "option": "anchor-json", "aliases": []string{"anchor"}, "type": "json"},
							"beforeSequence": map[string]any{"source": "option", "option": "before-sequence", "type": "integer"},
							"surfaces":       map[string]any{"source": "option", "option": "surfaces"},
						},
					},
					"group":       "Workspace",
					"id":          "workspace.activity",
					"operationId": "workspaces.activity",
				},
				{
					"command":     "craken wiki recent --workspace WORKSPACE",
					"description": "List recent wiki changes.",
					"execution":   map[string]any{"operationId": "wiki.recent-changes", "output": wikiRecentOutputPlan()},
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
						{"description": "Limit returned rows.", "name": "limit", "type": "integer"},
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
					"queryParameters": []map[string]any{
						{"description": "Limit returned rows.", "name": "limit", "type": "integer"},
					},
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
		"--limit  integer",
		"--compact",
		"--fields LIST",
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
	for _, expected := range []string{"--anchor-json JSON", "--before-sequence N", "--surfaces VALUE", "activities"} {
		if !strings.Contains(workspaceHelp, expected) {
			t.Fatalf("expected focused workspace help to contain %q, got:\n%s", expected, workspaceHelp)
		}
	}

	stdout.Reset()
	if err := Run(context.Background(), "dev", []string{"wiki", "recent", "--base-url", server.URL, "--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if help := stdout.String(); !strings.Contains(help, "--limit  integer") || !strings.Contains(help, "--compact") || !strings.Contains(help, "--fields LIST") {
		t.Fatalf("expected focused wiki help to contain output options, got:\n%s", help)
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
		if writeTestCatalog(t, w, r) {
			return
		}
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

func TestAgentDeviceLoginStoresAgentProfileMetadata(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()

	openBrowser = func(target string) {
		t.Fatalf("did not expect browser to open %s", target)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/agent-device-authorizations":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["workspaceId"] != "workspace-id" || body["agentName"] != "Ak's Codex" || body["clientKind"] != "codex" {
				t.Fatalf("unexpected agent authorization body %#v", body)
			}
			writeJSON(t, w, map[string]any{
				"deviceCode":              "agent-device-code",
				"expiresIn":               30,
				"interval":                1,
				"userCode":                "AGNT-2345",
				"verificationUri":         serverURL(r) + "/api/client/device",
				"verificationUriComplete": serverURL(r) + "/api/client/device?user_code=AGNT-2345",
			})
		case "/api/client/agent-device-token":
			writeJSON(t, w, map[string]any{
				"agent":     map[string]any{"agentId": "agent-id", "clientKind": "codex", "name": "Ak's Codex"},
				"session":   map[string]any{"email": "ak@example.com"},
				"token":     "agent-token",
				"tokenType": "Bearer",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stderr bytes.Buffer
	if err := Run(context.Background(), "dev", []string{
		"auth", "login",
		"--as-agent",
		"--no-open",
		"--profile", "codex",
		"--base-url", server.URL,
		"--workspace", "workspace-id",
		"--agent-name", "Ak's Codex",
		"--client-kind", "codex",
		"--timeout-ms", "5000",
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	prof := cfg.Profiles["codex"]
	if prof.Token != "agent-token" || prof.Kind != "agent" || prof.AgentID != "agent-id" || prof.AgentName != "Ak's Codex" {
		t.Fatalf("unexpected agent profile %#v", prof)
	}
	if !strings.Contains(stderr.String(), "authorize Ak's Codex") {
		t.Fatalf("expected agent authorization prompt in stderr, got %s", stderr.String())
	}
}

func resolverCatalogRoutes() []map[string]any {
	return []map[string]any{
		{
			"auth": "required", "description": "List workspaces", "id": "workspaces.list",
			"method": "GET", "path": "/api/workspaces", "requestBody": "none",
		},
		{
			"auth": "required", "description": "Read workspace detail", "id": "workspaces.get",
			"method": "GET", "path": "/api/workspaces/{workspaceId}", "requestBody": "none",
		},
		{
			"auth": "required", "description": "Create a channel message", "id": "channels.messages.create",
			"method": "POST", "path": "/api/workspaces/{workspaceId}/channels/{channelId}/messages", "requestBody": "json",
			"pathParams": []map[string]any{
				{"name": "workspaceId", "type": "string", "required": true, "description": "Workspace id or name from the path.", "resolver": map[string]any{
					"collectionPath": "workspaces", "label": "workspace", "matchFields": []string{"id", "name"}, "operationId": "workspaces.list", "resultPath": "id",
				}},
				{"name": "channelId", "type": "string", "required": true, "description": "Channel id or name from the path.", "resolver": map[string]any{
					"collectionPath": "channels", "label": "channel", "matchFields": []string{"id", "name"}, "operationId": "workspaces.get", "resultPath": "id", "scope": "workspace",
				}},
			},
			"bodyFields": []map[string]any{
				{"name": "body", "type": "string", "required": true, "description": "Message body."},
			},
		},
	}
}

func TestDoOperationHelpRendersFocusedRouteWithResolvers(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client" {
			t.Fatalf("unexpected help path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{"schemaVersion": 1, "routes": resolverCatalogRoutes()})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"do", "channels.messages.create", "--base-url", server.URL, "--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	for _, expected := range []string{
		"craken do channels.messages.create [options]",
		"Path parameters:",
		"--workspace-id",
		"accepts a workspace name or id",
		"--channel-id",
		"accepts a channel name or id",
		"Body fields:",
		"--body",
		"channels.messages.create\tPOST\t/api/workspaces/{workspaceId}/channels/{channelId}/messages",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("expected do help to contain %q, got:\n%s", expected, help)
		}
	}
	if strings.Contains(help, "Server commands:") {
		t.Fatalf("expected focused do help, got global help:\n%s", help)
	}
}

func TestGenericDoResolvesPathParamNames(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	var posted string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/client":
			writeJSON(t, w, map[string]any{"schemaVersion": 1, "routes": resolverCatalogRoutes()})
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "ws-uuid", "name": "acme"}}})
		case "GET /api/workspaces/ws-uuid":
			writeJSON(t, w, map[string]any{"channels": []map[string]any{{"id": "ch-uuid", "name": "general"}}})
		case "POST /api/workspaces/ws-uuid/channels/ch-uuid/messages":
			posted = r.URL.Path
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
		"do", "channels.messages.create",
		"--token", "user-token",
		"--base-url", server.URL,
		"--workspace-id", "acme",
		"--channel-id", "general",
		"--json", `{"body":"hi"}`,
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if posted != "/api/workspaces/ws-uuid/channels/ch-uuid/messages" {
		t.Fatalf("expected resolved UUID path, got %q", posted)
	}
	if body["body"] != "hi" {
		t.Fatalf("unexpected posted body %#v", body)
	}
}

func TestGenericDoResolverErrorNamesFailedField(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/client":
			writeJSON(t, w, map[string]any{"schemaVersion": 1, "routes": resolverCatalogRoutes()})
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "ws-uuid", "name": "acme"}}})
		case "GET /api/workspaces/ws-uuid":
			// The workspace has no channel named "missing", so channelId resolution fails.
			writeJSON(t, w, map[string]any{"channels": []map[string]any{{"id": "ch-uuid", "name": "general"}}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), "dev", []string{
		"do", "channels.messages.create",
		"--token", "user-token",
		"--base-url", server.URL,
		"--workspace-id", "acme",
		"--channel-id", "missing",
		"--json", `{"body":"hi"}`,
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected resolver error for unknown channel")
	}
	message := err.Error()
	// The error must name the field that failed (channelId) so a multi-resolver
	// command tells the user which input was bad, while keeping the resolver
	// label and value.
	for _, expected := range []string{"resolving channelId", "unknown channel: missing"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("expected resolver error to contain %q, got %q", expected, message)
		}
	}
}

func TestAuthWhoamiReportsAgentScopesFromToken(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	token := makeSessionToken(t, map[string]any{
		"email":    "ak@example.com",
		"provider": "google",
		"delegatedAgent": map[string]any{
			"agentId":     "agent-id",
			"clientKind":  "codex",
			"workspaceId": "ws-uuid",
			"scopes":      []string{"channel:write", "dm:write"},
		},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" {
			t.Fatalf("unexpected whoami path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{"authenticated": true, "user": map[string]any{
			"email": "ak@example.com", "delegatedAgent": map[string]any{"agentId": "agent-id"},
		}})
	}))
	defer server.Close()
	cfg := config{Profiles: map[string]profile{"codex": {BaseURL: server.URL, Token: token, Kind: "agent", AgentName: "Codex"}}}
	if err := writeConfig(cfg); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"auth", "whoami", "--profile", "codex"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	for _, expected := range []string{`"actingAs": "agent"`, "channel:write", "dm:write", "agent-id", `"authenticated": true`} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected whoami output to contain %q, got:\n%s", expected, out)
		}
	}
}

func TestAuthWhoamiWarnsWhenAgentLabelHoldsUserToken(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	userToken := makeSessionToken(t, map[string]any{"email": "owner@example.com", "provider": "google"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"authenticated": true, "user": map[string]any{"email": "owner@example.com"}})
	}))
	defer server.Close()
	cfg := config{Profiles: map[string]profile{"claude": {BaseURL: server.URL, Token: userToken, Kind: "agent", AgentName: "Claude"}}}
	if err := writeConfig(cfg); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"auth", "whoami", "--profile", "claude"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"actingAs": "user"`) {
		t.Fatalf("expected actingAs user, got:\n%s", out)
	}
	if !strings.Contains(out, "labeled kind=agent") {
		t.Fatalf("expected mismatch warning, got:\n%s", out)
	}
}

func TestAgentLoginRefusesAgentLabelWhenTokenLacksDelegation(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()
	openBrowser = func(string) { t.Fatal("did not expect browser to open") }

	userToken := makeSessionToken(t, map[string]any{"email": "owner@example.com", "provider": "google"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/agent-device-authorizations":
			writeJSON(t, w, map[string]any{
				"deviceCode": "code", "expiresIn": 30, "interval": 1, "userCode": "AB-12",
				"verificationUri": serverURL(r) + "/api/client/device",
			})
		case "/api/client/agent-device-token":
			// Server hands back a user-scoped token despite the agent-shaped response.
			writeJSON(t, w, map[string]any{
				"agent":     map[string]any{"agentId": "agent-id", "clientKind": "codex", "name": "Codex"},
				"token":     userToken,
				"tokenType": "Bearer",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stderr bytes.Buffer
	if err := Run(context.Background(), "dev", []string{
		"auth", "login", "--as-agent", "--device-code", "--no-open",
		"--profile", "codex", "--base-url", server.URL,
		"--workspace", "ws-uuid", "--agent-name", "Codex", "--client-kind", "codex",
		"--timeout-ms", "5000",
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	prof := cfg.Profiles["codex"]
	if prof.Token != userToken {
		t.Fatalf("expected token stored, got %q", prof.Token)
	}
	if prof.Kind == "agent" {
		t.Fatalf("expected profile not labeled agent when token lacks delegated-agent claims")
	}
	if !strings.Contains(stderr.String(), "no delegated-agent claims") {
		t.Fatalf("expected warning about missing delegated-agent claims, got:\n%s", stderr.String())
	}
}

func TestAuthLoginRejectsSaveTokenProfileFlag(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()
	openBrowser = func(string) { t.Fatal("did not expect a login flow to start") }

	// Seed a user credential in default; the rejected flag must leave it untouched.
	userToken := makeSessionToken(t, map[string]any{"email": "owner@example.com", "provider": "google"})
	if err := writeConfig(config{Profiles: map[string]profile{"default": {Token: userToken}}}); err != nil {
		t.Fatal(err)
	}

	err := Run(context.Background(), "dev", []string{
		"auth", "login", "--as-agent",
		"--workspace", "ws", "--agent-name", "Claude",
		"--save-token-profile", "kait-agent",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for unsupported --save-token-profile on auth login")
	}
	for _, want := range []string{"--save-token-profile", "--profile kait-agent"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected guidance %q, got: %v", want, err)
		}
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["default"].Token != userToken {
		t.Fatalf("default credential must be left intact, got %q", cfg.Profiles["default"].Token)
	}
	if _, exists := cfg.Profiles["kait-agent"]; exists {
		t.Fatal("must not create the kait-agent profile the flag was silently ignored into")
	}
}

func TestAuthLoginRefusesCrossIdentityOverwrite(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()
	openBrowser = func(string) { t.Fatal("did not expect a login flow to start") }

	userToken := makeSessionToken(t, map[string]any{"email": "ak@corca.ai", "provider": "google"})
	if err := writeConfig(config{Profiles: map[string]profile{"default": {Token: userToken, BaseURL: "https://example.test"}}}); err != nil {
		t.Fatal(err)
	}

	err := Run(context.Background(), "dev", []string{
		"auth", "login", "--as-agent", "--no-open",
		"--workspace", "ws", "--agent-name", "Claude",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected refusal when an agent login would overwrite a user credential")
	}
	for _, want := range []string{`profile "default"`, "user (ak@corca.ai)", "logging in as agent", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got: %v", want, err)
		}
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["default"].Token != userToken {
		t.Fatalf("user token must be preserved on refusal, got %q", cfg.Profiles["default"].Token)
	}
}

func TestAuthLoginForceOverwritesAcrossIdentity(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()
	openBrowser = func(string) {}

	userToken := makeSessionToken(t, map[string]any{"email": "ak@corca.ai", "provider": "google"})
	agentToken := makeSessionToken(t, map[string]any{
		"delegatedAgent": map[string]any{
			"agentId": "agent-id", "clientKind": "claude_code", "workspaceId": "ws-uuid",
			"scopes": []string{"channel:write"},
		},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/agent-device-authorizations":
			writeJSON(t, w, map[string]any{
				"deviceCode": "code", "expiresIn": 30, "interval": 1, "userCode": "AB-12",
				"verificationUri": serverURL(r) + "/api/client/device",
			})
		case "/api/client/agent-device-token":
			writeJSON(t, w, map[string]any{
				"agent":     map[string]any{"agentId": "agent-id", "clientKind": "claude_code", "name": "Claude"},
				"token":     agentToken,
				"tokenType": "Bearer",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := writeConfig(config{Profiles: map[string]profile{"default": {Token: userToken, BaseURL: server.URL}}}); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), "dev", []string{
		"auth", "login", "--as-agent", "--device-code", "--no-open", "--force",
		"--base-url", server.URL, "--workspace", "ws-uuid", "--agent-name", "Claude",
		"--client-kind", "claude_code", "--timeout-ms", "5000",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["default"].Token != agentToken {
		t.Fatalf("expected --force to replace the user token with the agent token, got %q", cfg.Profiles["default"].Token)
	}
	if cfg.Profiles["default"].Kind != "agent" {
		t.Fatalf("expected the overwritten profile labeled agent, got %q", cfg.Profiles["default"].Kind)
	}
}

func TestAuthLoginAllowsSameIdentityRefresh(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	originalOpen := openBrowser
	defer func() { openBrowser = originalOpen }()
	openBrowser = func(string) {}

	oldUser := makeSessionToken(t, map[string]any{"email": "ak@corca.ai", "provider": "google"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/device-authorizations":
			writeJSON(t, w, map[string]any{
				"deviceCode": "device-code", "expiresIn": 30, "interval": 1, "userCode": "WXYZ-2345",
				"verificationUri": serverURL(r) + "/api/client/device",
			})
		case "/api/client/device-token":
			writeJSON(t, w, map[string]any{
				"session": map[string]any{"email": "ak@corca.ai"}, "token": "refreshed-token", "tokenType": "Bearer",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := writeConfig(config{Profiles: map[string]profile{"default": {Token: oldUser, BaseURL: server.URL}}}); err != nil {
		t.Fatal(err)
	}

	// Re-logging in as the same identity kind (user over user) is a routine refresh
	// and must proceed without --force.
	if err := Run(context.Background(), "dev", []string{
		"auth", "login", "--no-open", "--base-url", server.URL, "--timeout-ms", "5000",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["default"].Token != "refreshed-token" {
		t.Fatalf("expected same-kind refresh to update the token, got %q", cfg.Profiles["default"].Token)
	}
}

func makeSessionToken(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return sessionBearerPrefix + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
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
		if writeTestCatalog(t, w, r) {
			return
		}
		if writeWorkspaceLookup(t, w, r, map[string]any{
			"channels": []map[string]any{{"id": "channel-id", "name": "general"}},
			"members":  []map[string]any{},
		}) {
			return
		}
		switch r.Method + " " + r.URL.Path {
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

func TestMessageCommandsSendLimitQuery(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	seenChannel := false
	seenDM := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		if writeWorkspaceLookup(t, w, r, map[string]any{
			"channels": []map[string]any{{"id": "channel-id", "name": "general"}},
			"members":  []map[string]any{{"id": "participant-id", "kind": "agent", "name": "orca"}},
		}) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces/workspace-id/channels/channel-id/messages":
			seenChannel = true
			if got := r.URL.Query().Get("after"); got != "message-1" {
				t.Fatalf("unexpected channel after query %q", got)
			}
			if got := r.URL.Query().Get("limit"); got != "10" {
				t.Fatalf("unexpected channel limit query %q", got)
			}
			writeJSON(t, w, map[string]any{"messages": []any{}})
		case "GET /api/workspaces/workspace-id/direct-messages/participant-id/messages":
			seenDM = true
			if got := r.URL.Query().Get("limit"); got != "10" {
				t.Fatalf("unexpected dm limit query %q", got)
			}
			writeJSON(t, w, map[string]any{"messages": []any{}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), "dev", []string{
		"channel", "messages",
		"--token", "test-token",
		"--base-url", server.URL,
		"--workspace", "test0",
		"--channel", "general",
		"--after", "message-1",
		"--limit", "10",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), "dev", []string{
		"dm", "messages",
		"--token", "test-token",
		"--base-url", server.URL,
		"--workspace", "test0",
		"--target", "orca",
		"--limit", "10",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !seenChannel || !seenDM {
		t.Fatalf("expected both message routes, saw channel=%t dm=%t", seenChannel, seenDM)
	}
}

func TestChannelMessagesCompactOutputOmitsPictures(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	picture := strings.Repeat("picture-data", 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		if writeWorkspaceLookup(t, w, r, map[string]any{
			"channels": []map[string]any{{"id": "channel-id", "name": "general"}},
			"members":  []map[string]any{},
		}) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces/workspace-id/channels/channel-id/messages":
			writeJSON(t, w, map[string]any{
				"messages": []map[string]any{{
					"body":      "hello\tthere\nnext",
					"createdAt": "2026-05-21T12:00:00.000Z",
					"id":        "message-1",
					"sender": map[string]any{
						"id":      "user:ada",
						"name":    "Ada",
						"picture": picture,
					},
				}},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{
		"channel", "messages",
		"--token", "test-token",
		"--base-url", server.URL,
		"--workspace", "test0",
		"--channel", "general",
		"--compact",
	}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "2026-05-21T12:00:00.000Z\tAda\thello\\tthere\\nnext\n"; got != want {
		t.Fatalf("unexpected compact output %q", got)
	}
	if strings.Contains(stdout.String(), "picture-data") {
		t.Fatalf("compact output leaked picture data: %s", stdout.String())
	}
}

func TestDMMessagesFieldsProjectNestedJSON(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	picture := strings.Repeat("picture-data", 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		if writeWorkspaceLookup(t, w, r, map[string]any{
			"channels": []map[string]any{},
			"members":  []map[string]any{{"id": "participant-id", "kind": "agent", "name": "orca"}},
		}) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces/workspace-id/direct-messages/participant-id/messages":
			writeJSON(t, w, map[string]any{
				"messages": []map[string]any{{
					"body":      "ready",
					"createdAt": "2026-05-21T12:00:00.000Z",
					"id":        "message-1",
					"sender": map[string]any{
						"id":      "agent:orca",
						"name":    "Orca",
						"picture": picture,
					},
				}},
				"newestCursor": "message-1",
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{
		"dm", "messages",
		"--token", "test-token",
		"--base-url", server.URL,
		"--workspace", "test0",
		"--target", "orca",
		"--fields", "messages.id,messages.createdAt,messages.sender.name,messages.body",
	}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "picture-data") || strings.Contains(stdout.String(), "newestCursor") {
		t.Fatalf("projected output leaked omitted fields: %s", stdout.String())
	}

	var projected map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &projected); err != nil {
		t.Fatal(err)
	}
	messages, _ := projected["messages"].([]any)
	first, _ := messages[0].(map[string]any)
	sender, _ := first["sender"].(map[string]any)
	if first["id"] != "message-1" || first["createdAt"] != "2026-05-21T12:00:00.000Z" || first["body"] != "ready" || sender["name"] != "Orca" {
		t.Fatalf("unexpected projected output %#v", projected)
	}
}

func TestWikiRecentCompactOutputOmitsAuthorPictures(t *testing.T) {
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
		case "GET /api/workspaces/workspace-id/wiki/recent-changes":
			writeJSON(t, w, map[string]any{
				"changes": []map[string]any{{
					"createdAt":     "2026-05-21T12:00:00.000Z",
					"createdBy":     map[string]any{"id": "user:ada", "name": "Ada", "picture": picture},
					"page":          map[string]any{"title": "Home"},
					"versionNumber": 3,
				}},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(context.Background(), "dev", []string{
		"wiki", "recent",
		"--token", "test-token",
		"--base-url", server.URL,
		"--workspace", "test0",
		"--compact",
	}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "2026-05-21T12:00:00.000Z\tAda\tHome\t3\n"; got != want {
		t.Fatalf("unexpected compact wiki output %q", got)
	}
	if strings.Contains(stdout.String(), "picture-data") {
		t.Fatalf("compact wiki output leaked picture data: %s", stdout.String())
	}
}

func TestMessageLimitServiceValidationIsPreserved(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		if writeWorkspaceLookup(t, w, r, map[string]any{
			"channels": []map[string]any{{"id": "channel-id", "name": "general"}},
			"members":  []map[string]any{},
		}) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces/workspace-id/channels/channel-id/messages":
			if got := r.URL.Query().Get("limit"); got != "0" {
				t.Fatalf("unexpected limit query %q", got)
			}
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(t, w, map[string]any{"error": "limit must be a positive integer"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	err := Run(context.Background(), "dev", []string{
		"channel", "messages",
		"--token", "test-token",
		"--base-url", server.URL,
		"--workspace", "test0",
		"--channel", "general",
		"--limit", "0",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected service validation error")
	}
	if message := err.Error(); !strings.Contains(message, "failed with 400") || !strings.Contains(message, "limit must be a positive integer") {
		t.Fatalf("expected service validation message, got %q", message)
	}
}

func TestChannelWaitResolvesWorkspaceChannelAndPrintsWaitResponse(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
		if writeWorkspaceLookup(t, w, r, map[string]any{
			"channels": []map[string]any{{"id": "channel-id", "name": "general"}},
			"members":  []map[string]any{},
		}) {
			return
		}
		switch r.Method + " " + r.URL.Path {
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
		if writeTestCatalog(t, w, r) {
			return
		}
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/workspaces":
			writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		case "POST /api/workspaces/workspace-id/wiki/pages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["title"] != "Home" || body["content"] != "# Home\n" || body["baseVersionNumber"] != float64(12) {
				t.Fatalf("unexpected wiki create body %#v", body)
			}
			writeJSON(t, w, map[string]any{"page": body})
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

	err := Run(context.Background(), "dev", []string{"wiki", "save", "--token", "test-token", "--base-url", server.URL, "--workspace", "test0", "--title", "Home", "--content-file", contentPath, "--base-version", "12"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), "dev", []string{"wiki", "save", "--token", "test-token", "--base-url", server.URL, "--workspace", "test0", "--existing-title", "Home", "--title", "Home", "--content-file", contentPath, "--base-version", "12"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), "dev", []string{"agent", "plan-check", "--token", "test-token", "--base-url", server.URL, "--next-json", `{"status":"completed"}`}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 5 {
		t.Fatalf("unexpected request count %d: %#v", len(seen), seen)
	}
}

func TestWikiSaveReportsBaseVersionConflict(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CRAKEN_CONFIG_DIR", configDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTestCatalog(t, w, r) {
			return
		}
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
		if writeTestCatalog(t, w, r) {
			return
		}
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

func TestWebSocketProtocolPlanBuildsBearerPayload(t *testing.T) {
	client := &client{token: "token-value"}
	var stdout bytes.Buffer
	if err := printWebSocketMessage(&stdout, []byte(`{"event":{"type":"message.created"}}`), command{Flags: map[string]bool{"pretty": true}}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, `"message.created"`) {
		t.Fatalf("unexpected pretty output %q", got)
	}
	protocols, err := catalogWebSocketProtocols(context.Background(), client, nil, []commandWebSocketProtocol{
		{Source: "literal", Value: "craken-bearer"},
		{Source: "json-payload", Prefix: "craken-bearer-payload.", Payload: map[string]commandBinding{"token": {Source: commandBindingSourceBearerToken}}},
	}, command{}, map[string]string{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
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

// writeWorkspaceLookup answers the standard workspace list + detail requests
// that the channel/DM message tests share, returning true once it has handled
// the request. The detail body (channels/members) varies per test, so callers
// supply it; the list response is the common workspace-id/test0 fixture.
func writeWorkspaceLookup(t *testing.T, w http.ResponseWriter, r *http.Request, detail map[string]any) bool {
	t.Helper()
	switch r.Method + " " + r.URL.Path {
	case "GET /api/workspaces":
		writeJSON(t, w, map[string]any{"workspaces": []map[string]any{{"id": "workspace-id", "name": "test0"}}})
		return true
	case "GET /api/workspaces/workspace-id":
		writeJSON(t, w, detail)
		return true
	}
	return false
}

func writeTestCatalog(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	if r.Method != http.MethodGet || r.URL.Path != "/api/client" {
		return false
	}
	writeJSON(t, w, map[string]any{
		"commands": []map[string]any{
			testCommand("workspace.list", "workspaces.list", nil),
			testCommand("workspace.accept", "workspace-invitations.accept", map[string]any{
				"pathParams": map[string]any{"token": map[string]any{"source": "option", "option": "invitation-token", "aliases": []string{"token"}, "required": true}},
			}),
			testCommand("channel.send", "channels.messages.create", map[string]any{
				"bodyFields": map[string]any{
					"body":        map[string]any{"source": "text", "option": "body", "fileOption": "body-file", "positionals": "join", "required": true},
					"senderEmail": map[string]any{"source": "option", "option": "sender-email", "aliases": []string{"sender"}},
				},
			}),
			testCommand("channel.messages", "channels.messages.list", map[string]any{"output": messagesOutputPlan()}),
			testCommand("channel.wait", "channels.messages.wait", map[string]any{
				"queryParams": map[string]any{
					"after":     map[string]any{"source": "option", "option": "after"},
					"timeoutMs": map[string]any{"source": "option", "option": "timeout-ms", "type": "integer"},
				},
			}),
			testCommand("dm.messages", "direct-messages.list", map[string]any{"output": messagesOutputPlan()}),
			testCommand("wiki.recent", "wiki.recent-changes", map[string]any{"output": wikiRecentOutputPlan()}),
			{
				"command":     "craken wiki save",
				"description": "Save wiki page.",
				"execution": map[string]any{
					"bodyFields": map[string]any{
						"baseVersionNumber": map[string]any{"source": "option", "option": "base-version", "type": "integer"},
						"content":           map[string]any{"source": "text", "option": "content", "fileOption": "content-file", "required": true},
						"title":             map[string]any{"source": "option", "option": "title", "required": true},
					},
					"operationId": "wiki.pages.create",
					"pathParams":  map[string]any{"workspaceId": workspaceOptionBinding()},
					"transport":   "http",
					"variants": []map[string]any{{
						"bodyFields": map[string]any{
							"baseVersionNumber": map[string]any{"source": "option", "option": "base-version", "type": "integer"},
							"content":           map[string]any{"source": "text", "option": "content", "fileOption": "content-file", "required": true},
							"title":             map[string]any{"source": "option", "option": "title"},
						},
						"operationId": "wiki.pages.update",
						"pathParams": map[string]any{
							"workspaceId": workspaceOptionBinding(),
							"pageTitle":   map[string]any{"source": "option", "option": "existing-title", "required": true},
						},
						"when": map[string]any{"option": "existing-title"},
					}},
				},
				"group":       "Wiki",
				"id":          "wiki.save",
				"operationId": "wiki.pages.update",
			},
			testCommand("agent.plan-check", "sysop.agent-job-plan.progression", map[string]any{
				"bodyFields": map[string]any{
					"nextPlan":     map[string]any{"source": "option", "option": "next-json", "aliases": []string{"next-file"}, "required": true, "type": "json"},
					"previousPlan": map[string]any{"source": "option", "option": "previous-json", "aliases": []string{"previous-file"}, "type": "json"},
				},
			}),
			testCommand("file.upload", "files.create", map[string]any{
				"multipart": map[string]any{
					"contentTypeDefault": "application/octet-stream",
					"contentTypeOption":  "type",
					"fields": map[string]any{
						"folderPath": map[string]any{"source": "option", "option": "folder"},
						"scope":      map[string]any{"source": "option", "option": "scope", "default": "workspace"},
					},
					"fileField":       "file",
					"fileNameDefault": "basename",
					"fileNameOption":  "name",
					"fileOption":      "path",
				},
				"transport": "multipart",
			}),
			testCommand("folder.create", "folders.create", map[string]any{
				"bodyFields": map[string]any{
					"name":       map[string]any{"source": "option", "option": "name", "required": true},
					"parentPath": map[string]any{"source": "option", "option": "parent"},
					"scope":      map[string]any{"source": "option", "option": "scope"},
				},
			}),
		},
		"routes": []map[string]any{
			testRoute("workspaces.list", http.MethodGet, "/api/workspaces", "none"),
			testRoute("workspaces.get", http.MethodGet, "/api/workspaces/{workspaceId}", "none"),
			testRoute("workspace-invitations.accept", http.MethodPost, "/api/workspace-invitations/{token}/accept", "json"),
			testRoute("channels.messages.create", http.MethodPost, "/api/workspaces/{workspaceId}/channels/{channelId}/messages", "json"),
			testRoute("channels.messages.list", http.MethodGet, "/api/workspaces/{workspaceId}/channels/{channelId}/messages", "none"),
			testRoute("channels.messages.wait", http.MethodGet, "/api/workspaces/{workspaceId}/channels/{channelId}/messages/wait", "none"),
			testRoute("direct-messages.list", http.MethodGet, "/api/workspaces/{workspaceId}/direct-messages/{participantId}/messages", "none"),
			testRoute("wiki.recent-changes", http.MethodGet, "/api/workspaces/{workspaceId}/wiki/recent-changes", "none"),
			testRoute("wiki.pages.create", http.MethodPost, "/api/workspaces/{workspaceId}/wiki/pages", "json"),
			testRoute("wiki.pages.update", http.MethodPatch, "/api/workspaces/{workspaceId}/wiki/pages/{pageTitle}", "json"),
			testRoute("sysop.agent-job-plan.progression", http.MethodPost, "/api/admin/agent-job-plan/progression", "json"),
			testRoute("files.create", http.MethodPost, "/api/workspaces/{workspaceId}/files", "multipart"),
			testRoute("folders.create", http.MethodPost, "/api/workspaces/{workspaceId}/folders", "json"),
		},
		"schemaVersion": 1,
		"shortcuts": []map[string]any{
			{"resource": "workspace", "actions": []string{"list", "get", "accept"}, "defaultAction": "list", "description": "Workspace shortcuts"},
			{"resource": "channel", "actions": []string{"messages", "send", "wait"}, "description": "Channel shortcuts"},
		},
	})
	return true
}

func testCommand(id string, operationID string, execution map[string]any) map[string]any {
	if execution == nil {
		execution = map[string]any{}
	}
	execution["operationId"] = operationID
	if _, ok := execution["pathParams"]; !ok {
		if pathParams := testPathParams(operationID); len(pathParams) > 0 {
			execution["pathParams"] = pathParams
		}
	}
	return map[string]any{
		"command":     "craken " + strings.Replace(id, ".", " ", 1),
		"description": "Test command " + id,
		"execution":   execution,
		"group":       "Test",
		"id":          id,
		"operationId": operationID,
	}
}

func testPathParams(operationID string) map[string]any {
	switch operationID {
	case "workspace-invitations.accept":
		return map[string]any{"token": map[string]any{"source": "option", "option": "invitation-token", "aliases": []string{"token"}, "required": true}}
	case "workspaces.get", "wiki.recent-changes", "wiki.pages.create", "folders.create", "files.create":
		return map[string]any{"workspaceId": workspaceOptionBinding()}
	case "channels.messages.create", "channels.messages.list", "channels.messages.wait":
		return map[string]any{"workspaceId": workspaceOptionBinding(), "channelId": channelOptionBinding()}
	case "direct-messages.list":
		return map[string]any{"workspaceId": workspaceOptionBinding(), "participantId": participantOptionBinding()}
	case "wiki.pages.update":
		return map[string]any{"workspaceId": workspaceOptionBinding(), "pageTitle": map[string]any{"source": "option", "option": "title", "aliases": []string{"page"}, "required": true}}
	default:
		return nil
	}
}

func workspaceOptionBinding() map[string]any {
	return map[string]any{
		"source": "option", "option": "workspace", "required": true,
		"resolver": map[string]any{
			"collectionPath": "workspaces",
			"label":          "workspace",
			"matchFields":    []string{"id", "name"},
			"operationId":    "workspaces.list",
			"resultPath":     "id",
		},
	}
}

func channelOptionBinding() map[string]any {
	return map[string]any{
		"source": "option", "option": "channel", "required": true,
		"resolver": map[string]any{
			"collectionPath": "channels",
			"label":          "channel",
			"matchFields":    []string{"id", "name"},
			"operationId":    "workspaces.get",
			"pathParams":     map[string]any{"workspaceId": map[string]any{"source": "resolved", "name": "workspaceId", "required": true}},
			"resultPath":     "id",
		},
	}
}

func participantOptionBinding() map[string]any {
	return map[string]any{
		"source": "option", "option": "target", "aliases": []string{"participant"}, "required": true,
		"resolver": map[string]any{
			"collectionPath": "members",
			"label":          "participant",
			"matchFields":    []string{"id", "email", "name"},
			"operationId":    "workspaces.get",
			"pathParams":     map[string]any{"workspaceId": map[string]any{"source": "resolved", "name": "workspaceId", "required": true}},
			"resultPath":     "id",
		},
	}
}

func messagesOutputPlan() map[string]any {
	return map[string]any{
		"columns": []map[string]any{
			{"paths": []string{"createdAt"}},
			{"paths": []string{"sender.name", "sender.email", "sender.id"}},
			{"paths": []string{"body"}},
		},
		"mode":     "table",
		"rowsPath": "messages",
	}
}

func wikiRecentOutputPlan() map[string]any {
	return map[string]any{
		"columns": []map[string]any{
			{"paths": []string{"createdAt"}},
			{"paths": []string{"createdBy.name", "createdBy.email", "createdBy.id"}},
			{"paths": []string{"page.title"}},
			{"paths": []string{"versionNumber"}},
		},
		"mode":     "table",
		"rowsPath": "changes",
	}
}

func testRoute(id string, method string, path string, requestBody string) map[string]any {
	return map[string]any{
		"auth":        "required",
		"description": "Test route " + id,
		"id":          id,
		"method":      method,
		"path":        path,
		"requestBody": requestBody,
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
