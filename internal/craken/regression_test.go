package craken

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
