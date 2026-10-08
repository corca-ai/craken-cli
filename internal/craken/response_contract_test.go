package craken

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestHTTPFailurePreservesProblemEvidenceWithoutSecrets(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("Retry-After", "5")
		w.Header().Set("X-Request-Id", "request-1")
		w.Header().Set("Set-Cookie", "private-cookie")
		w.WriteHeader(500)
		_, _ = fmt.Fprint(w, `{"type":"https://example.test/problem","title":"failed","status":500,"extension":42,"access_token":"private-token"}`)
	}))
	defer server.Close()
	args := []string{"post", "/write", "--json", "null", "--token", "test", "--base-url", server.URL, "--error-format", "json"}
	var out, diagnostics bytes.Buffer
	err := Run(context.Background(), "dev", args, strings.NewReader(""), &out, &diagnostics)
	if err == nil {
		t.Fatal("expected HTTP failure")
	}
	WriteError(&diagnostics, err, args)
	var result DiagnosticError
	if err := json.Unmarshal(diagnostics.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Stage != "response" || result.Response == nil || result.Response.Status != 500 || result.Response.Headers.Get("Retry-After") != "5" || result.Response.Headers.Get("X-Request-Id") != "request-1" {
		t.Fatalf("evidence=%+v", result)
	}
	if valueAtPath(result.Response.Body, "extension") != float64(42) || strings.Contains(diagnostics.String(), "private-") || out.Len() != 0 || requests != 1 {
		t.Fatalf("diagnostic=%s requests=%d stdout=%s", diagnostics.String(), requests, out.String())
	}
}

func TestSuccessfulResponseOutputFailureKeepsHTTPStatus(t *testing.T) {
	payload := responsePayload{Parsed: map[string]any{"items": []any{map[string]any{"name": "result"}}}, Evidence: &responseEvidence{Status: 201, Headers: http.Header{"Location": []string{"/created"}}}}
	cmd := command{Options: map[string]string{"format": "ndjson"}}
	err := printCatalogCommandPayload(failingOutput{}, payload, cmd, &commandOutputPlan{Mode: commandOutputModeTable})
	var diagnostic *DiagnosticError
	if !errors.As(err, &diagnostic) || diagnostic.Stage != "output" || diagnostic.Response == nil || diagnostic.Response.Status != 201 {
		t.Fatalf("lost successful response: %#v", err)
	}
}

func TestConditionalAndRangeDownloadsPreserveFilesAndHeaders(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	fixture, err := os.ReadFile("testdata/server-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog clientCatalog
	if err := json.Unmarshal(fixture, &catalog); err != nil {
		t.Fatal(err)
	}
	operation := routeByID(catalog.Routes, "files.content.get")
	if operation == nil {
		t.Fatal("actual server download route missing")
	}
	output := filepath.Join(t.TempDir(), "download.bin")
	metadata := filepath.Join(t.TempDir(), "response.json")
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := 304
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture)
			return
		}
		if r.Header.Get("Accept") != "*/*" || r.Header.Get("If-None-Match") != "\"old\"" || r.URL.Query().Get("tag") != "false" || len(r.URL.Query()["tag"]) != 2 {
			t.Errorf("request=%s headers=%v", r.URL, r.Header)
		}
		w.Header().Set("ETag", "\"version\"")
		if status == 206 {
			w.Header().Set("Content-Range", "bytes 1-3/7")
		}
		w.WriteHeader(status)
		if status == 206 {
			_, _ = fmt.Fprint(w, "abc")
		}
	}))
	defer server.Close()
	args := []string{"do", operation.ID, "--workspace-id", "12345678-1234-1234-1234-123456789abc", "--file-id", "file-1", "--output", output, "--response-meta", metadata, "--header", `If-None-Match: "old"`, "--query", "tag=false", "--query", "tag=null", "--token", "test", "--base-url", server.URL}
	for _, expected := range []struct {
		status  int
		content string
	}{{304, "existing"}, {206, "abc"}} {
		status = expected.status
		if err := Run(context.Background(), "dev", args, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(output)
		if err != nil || string(content) != expected.content {
			t.Fatalf("file=%s error=%v", content, err)
		}
		raw, err := os.ReadFile(metadata)
		if err != nil {
			t.Fatal(err)
		}
		var evidence responseEvidence
		if err := json.Unmarshal(raw, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.Status != status || evidence.Headers.Get("ETag") != "\"version\"" || (status == 206 && evidence.Headers.Get("Content-Range") == "") {
			t.Fatalf("metadata=%s", raw)
		}
	}
}

type truncatedDownload struct{ sent bool }

func (r *truncatedDownload) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial"), nil
	}
	return 0, io.ErrUnexpectedEOF
}
func TestTruncatedDownloadDoesNotReplaceExistingFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "result.bin")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDownload(path, &truncatedDownload{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error=%v", err)
	}
	value, err := os.ReadFile(path)
	if err != nil || string(value) != "existing" {
		t.Fatalf("value=%s err=%v", value, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}
