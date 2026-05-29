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
