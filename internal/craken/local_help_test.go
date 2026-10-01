package craken

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginHelpWorksWithoutConfigOrNetwork(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"auth", "login", "--help", "--base-url", server.URL}, nil, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("local help made %d requests", requests)
	}
	for option := range loginSupportedOptions {
		if !strings.Contains(out.String(), "--"+option) {
			t.Errorf("supported option --%s missing from help:\n%s", option, out.String())
		}
	}
}

func TestResourceHelpUsesAnonymousCatalog(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestCatalog(t, w, r)
	}))
	defer server.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"workspace", "--help", "--base-url", server.URL}, nil, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "craken workspace accept") || strings.Contains(out.String(), "Server commands:") {
		t.Fatalf("expected workspace-specific commands, got:\n%s", out.String())
	}
}

func TestUnavailableCatalogHelpHasExplicitLocalFallback(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, format := range []string{"text", "json"} {
		var out, diagnostics bytes.Buffer
		if err := Run(context.Background(), "dev", []string{"help", "--base-url", server.URL, "--format", format}, nil, &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diagnostics.String(), "catalog unavailable") {
			t.Fatalf("expected failure diagnostic, got %q", diagnostics.String())
		}
		if format == "json" {
			var guidance map[string]any
			if err := json.Unmarshal(out.Bytes(), &guidance); err != nil {
				t.Fatal(err)
			}
			if guidance["source"] != "local-fallback" || guidance["auth"] != nil {
				t.Fatalf("fallback must not claim authentication state: %#v", guidance)
			}
		} else if !strings.Contains(out.String(), "Local fallback") || !strings.Contains(out.String(), "auth login --help") {
			t.Fatalf("expected usable local fallback, got:\n%s", out.String())
		}
	}
}

func TestWhoamiReportsEffectiveBaseURL(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	t.Setenv("CRAKEN_BASE_URL", "https://environment.example")
	var out bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"auth", "whoami", "--profile", "empty", "--base-url", "https://override.example"}, nil, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["baseUrl"] != "https://override.example" || status["profile"] != "empty" {
		t.Fatalf("incorrect effective connection: %#v", status)
	}
}
