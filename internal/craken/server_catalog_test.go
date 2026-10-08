package craken

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestActualServerCatalogInputs(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	fixture, err := os.ReadFile("testdata/server-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(fixture, &value); err != nil {
		t.Fatal(err)
	}
	catalog, err := catalogFromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	if routeByID(catalog.Routes, "wiki.pages.sections.metadata.update").Method != "PATCH" {
		t.Fatal("server metadata update must use PATCH")
	}
	var lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture)
		case "/api/workspaces":
			lookups.Add(1)
			writeJSON(t, w, map[string]any{"workspaces": []any{}})
		default:
			if r.URL.Query().Get("surfaces") != "file,wiki" || r.URL.Query().Get("anchor") != `{"kind":"workspace"}` || r.URL.Query().Get("limit") != "3" {
				t.Errorf("actual wire query=%s", r.URL)
			}
			writeJSON(t, w, map[string]any{"activities": []any{}})
		}
	}))
	defer server.Close()
	args := []string{"do", "workspaces.activity", "--workspace-id", "12345678-1234-1234-1234-123456789abc", "--surfaces", "file", "--surfaces", "wiki", "--anchor", `{"kind":"workspace"}`, "--limit", "3", "--token", "test", "--base-url", server.URL}
	if err := Run(context.Background(), "dev", args, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 0 {
		t.Fatalf("declared canonical ID triggered %d lookups", lookups.Load())
	}
}

func TestActualServerShortcutPreservesNullableStringPurpose(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	fixture, err := os.ReadFile("testdata/server-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	purpose := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["purpose"] != purpose || body["name"] != "general" {
			t.Errorf("purpose=%q body=%v", purpose, body)
		}
		writeJSON(t, w, map[string]any{"channel": map[string]any{"id": "created"}})
	}))
	defer server.Close()
	for _, value := range []string{"Project discussion", "null"} {
		purpose = value
		args := []string{"channel", "create", "--workspace", "12345678-1234-1234-1234-123456789abc", "--name", "general", "--purpose", value, "--token", "test", "--base-url", server.URL}
		if err := Run(context.Background(), "dev", args, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
}
