package craken

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGlobalFlagsAndPositionalSentinel(t *testing.T) {
	cmd, err := parseCommand([]string{"--compact", "workspace", "list", "--header", "A: 1", "--header", "A: 2", "--", "--looks-like-text"})
	if err != nil || cmd.Resource != "workspace" || cmd.Action != "list" || !cmd.Flags["compact"] || len(cmd.Values["header"]) != 2 || cmd.Positionals[0] != "--looks-like-text" {
		t.Fatalf("parsed=%+v err=%v", cmd, err)
	}
}
func TestSelectedHelpPreservesRawMetadata(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	catalog := map[string]any{"schemaVersion": 1, "routes": []any{map[string]any{"id": "future.write", "method": "POST", "path": "/future", "description": "Future", "auth": "none", "requestBody": "json", "newMetadata": map[string]any{"keep": true}}}, "commands": []any{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, catalog) }))
	defer server.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), "dev", []string{"do", "future.write", "--help", "--format", "json", "--base-url", server.URL}, strings.NewReader(""), &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("not pure JSON: %s", out.String())
	}
	if valueAtPath(result, "route.newMetadata.keep") != true {
		t.Fatal("unknown metadata lost")
	}
}
func TestWireInputsAndPrepareFailures(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	operation := genericTransportRoute("future.write", "POST", "/future", "json", nil)
	if err := json.Unmarshal([]byte(`{"wire":{"query":{"properties":{"limit":{"type":"integer"},"anchor":{"type":"object"},"surfaces":{"type":"array","items":{"type":"string"}}}},"queryEncoding":{"anchor":"json","surfaces":"comma"},"body":{"properties":{"enabled":{"type":"boolean"},"owner":{"anyOf":[{"type":"string"},{"type":"null"}]}}}}}`), &operation.Specification); err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client" {
			writeJSON(t, w, clientCatalog{SchemaVersion: 1, Routes: []route{operation}})
			return
		}
		writes.Add(1)
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"enabled":false,"owner":null}` || r.URL.Query().Get("surfaces") != "file,wiki" || r.URL.Query().Get("limit") != "3" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("wire %s %s %v", body, r.URL, r.Header)
		}
		writeJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()
	base := []string{"do", "future.write", "--token", "test", "--base-url", server.URL}
	for _, extra := range [][]string{{"--format", "invalid"}, {"--compact", "--fields", "ok"}, {"--typo", "value"}, {"--json", "{}", "--enabled", "false"}} {
		if err := Run(context.Background(), "dev", append(append([]string{}, base...), extra...), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("expected prepare error for %v", extra)
		}
	}
	if writes.Load() != 0 {
		t.Fatal("prepare failures executed mutation")
	}
	args := append(base, "--enabled", "false", "--owner", "null", "--limit", "3", "--anchor", `{"kind":"workspace"}`, "--surfaces", `["file","wiki"]`, "--format", "ndjson")
	if err := Run(context.Background(), "dev", args, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatalf("writes=%d", writes.Load())
	}
}

func TestDeclaredStringAndJSONBindingsKeepTheirValues(t *testing.T) {
	schema := wireSchema{Properties: map[string]wireField{"value": {AnyOf: []wireField{{Type: "string"}, {Type: "null"}}}}}
	for _, binding := range []commandBinding{{Source: commandBindingSourceOption, Type: "string"}, {Source: commandBindingSourceText}, {Source: commandBindingSourceOption, Type: commandBindingValueTypeJSON}, {Source: commandBindingSourceOption}} {
		values, err := convertBoundWire(map[string]any{"value": "null"}, schema, map[string]commandBinding{"value": binding})
		if err != nil || values["value"] != "null" {
			t.Fatalf("binding=%+v values=%v err=%v", binding, values, err)
		}
	}
	cmd, err := parseCommand([]string{"do", "future.write", "--tags", "false", "--tags", "null", "--tags", "123"})
	if err != nil {
		t.Fatal(err)
	}
	selected := route{Specification: &operationSpecification{}}
	selected.Specification.Wire.Body = wireSchema{Properties: map[string]wireField{"tags": {Type: "array", Items: &wireField{Type: "string"}}}}
	normalized, err := normalizeArrayOptions(cmd, selected, commandExecution{})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Options["tags"] != `["false","null","123"]` {
		t.Fatalf("string items=%s", normalized.Options["tags"])
	}
	path, err := appendWireQuery("/future", map[string]any{"nullable": nil}, route{})
	if err != nil || path != "/future?nullable=null" {
		t.Fatalf("null query=%s err=%v", path, err)
	}
}

func TestShortcutAliasesRequireValuesBeforeResolverReads(t *testing.T) {
	plan := commandExecution{Transport: commandTransportHTTP, PathParams: map[string]commandBinding{"pageTitle": {Source: commandBindingSourceOption, Option: "existing-title", Required: true}}, BodyFields: map[string]commandBinding{"content": {Source: commandBindingSourceText, Option: "content", FileOption: "content-file"}}}
	selected := route{Specification: &operationSpecification{}}
	for _, name := range []string{"existing-title", "content-file"} {
		cmd := command{Options: map[string]string{"existing-title": "page"}, Flags: map[string]bool{name: true}}
		if err := validateCatalogInputs(cmd, selected, plan, false); err == nil {
			t.Fatalf("bare --%s accepted", name)
		}
	}
}
