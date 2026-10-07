package craken

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
)

func TestDoDiscoveredMultipartPreservesMethodResolverAndOptionalSource(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "public URL"}[optional], func(t *testing.T) {
			t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
			filePath := filepath.Join(t.TempDir(), "input.txt")
			if err := os.WriteFile(filePath, []byte("new upload"), 0o600); err != nil {
				t.Fatal(err)
			}
			var catalogReads, uploads atomic.Int32
			operation := genericTransportRoute("new.import.v2", http.MethodPut, "/custom/{workspaceId}/import", "multipart", &commandExecution{
				Transport: commandTransportMultipart,
				Multipart: commandMultipartPlan{FileOption: "file", FileField: "source", FileOptional: optional, FileNameDefault: "basename", ContentTypeDefault: "text/plain", Fields: map[string]commandBinding{
					"sourceUrl": {Source: commandBindingSourceOption, Option: "source-url"},
					"folder":    {Source: commandBindingSourceOption, Option: "folder", Required: true},
				}},
				QueryParams: map[string]commandBinding{"mode": {Source: commandBindingSourceOption, Option: "mode"}},
			})
			operation.PathParams = []catalogField{{Name: "workspaceId", Resolver: &catalogFieldResolver{OperationID: "custom.list", CollectionPath: "items", MatchFields: []string{"name"}, ResultPath: "id", Label: "workspace"}}}
			list := genericTransportRoute("custom.list", http.MethodGet, "/custom/list", "none", nil)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer transport-token" {
					t.Errorf("authorization=%q", r.Header.Get("Authorization"))
				}
				switch r.URL.Path {
				case "/api/client":
					catalogReads.Add(1)
					w.Header().Set(d1BookmarkHeader, "catalog-bookmark")
					writeJSON(t, w, clientCatalog{SchemaVersion: 1, Routes: []route{operation, list}})
				case "/custom/list":
					if r.Header.Get(d1BookmarkHeader) != "catalog-bookmark" {
						t.Error("resolver lost catalog bookmark")
					}
					w.Header().Set(d1BookmarkHeader, "resolved-bookmark")
					writeJSON(t, w, map[string]any{"items": []any{map[string]any{"name": "Research", "id": "workspace-42"}}})
				case "/custom/workspace-42/import":
					uploads.Add(1)
					if r.Method != http.MethodPut || r.URL.Query().Get("mode") != "safe" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					if r.Header.Get("Accept") != "application/vnd.custom+json" || r.Header.Get(d1BookmarkHeader) != "resolved-bookmark" {
						t.Errorf("headers=%v", r.Header)
					}
					if err := r.ParseMultipartForm(1024); err != nil {
						t.Errorf("multipart: %v", err)
						return
					}
					defer func() { _ = r.MultipartForm.RemoveAll() }()
					if r.FormValue("folder") != "reports" {
						t.Errorf("folder=%q", r.FormValue("folder"))
					}
					if optional {
						if len(r.MultipartForm.File) != 0 || r.FormValue("sourceUrl") != "https://public.test/data.txt" {
							t.Errorf("URL-only form=%v", r.MultipartForm)
						}
					} else {
						file, header, err := r.FormFile("source")
						if err != nil {
							t.Errorf("file: %v", err)
							return
						}
						defer func() { _ = file.Close() }()
						content, _ := io.ReadAll(file)
						if string(content) != "new upload" || header.Filename != "input.txt" || header.Header.Get("Content-Type") != "text/plain" {
							t.Errorf("file=%q header=%v", content, header)
						}
					}
					writeJSON(t, w, map[string]any{"created": true})
				default:
					t.Errorf("unexpected path=%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			args := []string{"do", operation.ID, "Research", "--folder", "reports", "--mode", "safe", "--accept", "application/vnd.custom+json"}
			if optional {
				args = append(args, "--source-url", "https://public.test/data.txt")
			} else {
				args = append(args, "--file", filePath)
			}
			stdout, err := runGenericTransport(t, server.URL, args)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout, `"created": true`) || catalogReads.Load() != 1 || uploads.Load() != 1 {
				t.Fatalf("stdout=%s catalog=%d uploads=%d", stdout, catalogReads.Load(), uploads.Load())
			}
			if !optional {
				_, err := runGenericTransport(t, server.URL, []string{"do", operation.ID, "Research", "--folder", "reports"})
				if err == nil || !strings.Contains(err.Error(), "expected --file") || uploads.Load() != 1 {
					t.Fatalf("missing required file: %v uploads=%d", err, uploads.Load())
				}
			}
		})
	}
}

func TestDoDiscoveredDownloadWritesBytesAndRetainsErrorBody(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var catalogReads atomic.Int32
	operation := genericTransportRoute("documents.custom-download", http.MethodGet, "/exports/{itemId}", "none", &commandExecution{Transport: commandTransportDownload})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client" {
			catalogReads.Add(1)
			w.Header().Set(d1BookmarkHeader, "download-bookmark")
			writeJSON(t, w, clientCatalog{SchemaVersion: 1, Routes: []route{operation}})
			return
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer transport-token" || r.Header.Get("Accept") != "application/octet-stream" || r.Header.Get(d1BookmarkHeader) != "download-bookmark" {
			t.Errorf("download request=%s headers=%v", r.Method, r.Header)
		}
		if r.URL.Path == "/exports/missing" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"error":"export not ready","code":"pending"}`)
			return
		}
		if r.URL.Path != "/exports/report/a" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte{0, 1, 2, 255})
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "out.bin")
	stdout, err := runGenericTransport(t, server.URL, []string{"do", operation.ID, "--item-id", "report/a", "--output", output, "--accept", "application/octet-stream"})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(content, []byte{0, 1, 2, 255}) || stdout != "" || catalogReads.Load() != 1 {
		t.Fatalf("output=%v stdout=%q reads=%d err=%v", content, stdout, catalogReads.Load(), err)
	}
	_, err = runGenericTransport(t, server.URL, []string{"do", operation.ID, "missing", "--output", output, "--accept", "application/octet-stream"})
	if err == nil || !strings.Contains(err.Error(), `"code":"pending"`) || !strings.Contains(err.Error(), "409") {
		t.Fatalf("error body lost: %v", err)
	}
	content, _ = os.ReadFile(output)
	if !bytes.Equal(content, []byte{0, 1, 2, 255}) {
		t.Fatalf("failed download overwrote output: %v", content)
	}
}

func TestDoDiscoveredWebSocketUsesAdvertisedProtocols(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var catalogReads atomic.Int32
	operation := genericTransportRoute("events.protocol.vNext", http.MethodGet, "/events/{selection}", "none", &commandExecution{
		Transport:   commandTransportWebSocket,
		QueryParams: map[string]commandBinding{"cursor": {Source: commandBindingSourceOption, Option: "cursor"}},
		WebSocket: commandWebSocketPlan{Protocols: []commandWebSocketProtocol{
			{Source: "literal", Value: "custom-live"},
			{Source: "json-payload", Prefix: "custom-token.", Payload: map[string]commandBinding{"token": {Source: commandBindingSourceBearerToken}}},
		}},
	})
	operation.Stream = "websocket"
	upgrader := websocket.Upgrader{Subprotocols: []string{"custom-live"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client" {
			catalogReads.Add(1)
			writeJSON(t, w, clientCatalog{SchemaVersion: 1, Routes: []route{operation}})
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/events/new-channel" || r.URL.Query().Get("cursor") != "9" {
			t.Errorf("websocket request=%s %s", r.Method, r.URL)
		}
		protocols := websocket.Subprotocols(r)
		if len(protocols) != 2 || protocols[0] != "custom-live" {
			t.Errorf("protocols=%v", protocols)
			return
		}
		payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(protocols[1], "custom-token."))
		if err != nil || string(payload) != `{"token":"transport-token"}` {
			t.Errorf("token payload=%q err=%v", payload, err)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade=%v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"created"}`))
	}))
	defer server.Close()
	stdout, err := runGenericTransport(t, server.URL, []string{"do", operation.ID, "new-channel", "--cursor", "9", "--limit", "1", "--format", "ndjson"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != `{"event":"created"}` || catalogReads.Load() != 1 {
		t.Fatalf("stream=%q catalog=%d", stdout, catalogReads.Load())
	}
}

func genericTransportRoute(id, method, path, body string, execution *commandExecution) route {
	return route{ID: id, Method: method, Path: path, RequestBody: body, Execution: execution, Auth: "user", Description: "Synthetic transport operation"}
}

func runGenericTransport(t *testing.T, baseURL string, args []string) (string, error) {
	t.Helper()
	args = append(args, "--base-url", baseURL, "--token", "transport-token")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), "dev", args, strings.NewReader(""), &stdout, &stderr)
	return stdout.String(), err
}

func TestDoHelpDescribesRouteTransportPlanWithoutShortcutPathOptions(t *testing.T) {
	for _, scenario := range []struct {
		name string
		plan commandExecution
		want []string
	}{
		{"multipart", commandExecution{Transport: commandTransportMultipart, Multipart: commandMultipartPlan{
			FileOption: "file", FileField: "source", FileOptional: true,
			Fields: map[string]commandBinding{"sourceUrl": {Source: commandBindingSourceOption, Option: "source-url"}},
		}}, []string{"--file PATH  optional", "--source-url VALUE"}},
		{"download", commandExecution{Transport: commandTransportDownload}, []string{"--output PATH"}},
		{"websocket", commandExecution{Transport: commandTransportWebSocket, WebSocket: commandWebSocketPlan{Stream: &commandStreamPlan{}}}, []string{"--timeout-ms MS", "--resume[=false]", "--reconnect[=false]"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
			// Path execution bindings may repeat the canonical route flag; generic
			// help renders its resolver-rich route field once instead.
			scenario.plan.PathParams = map[string]commandBinding{"workspaceId": {Source: commandBindingSourceOption, Option: "workspace-id"}}
			operation := genericTransportRoute("new.operation."+scenario.name, http.MethodPost, "/custom/{workspaceId}", "none", &scenario.plan)
			operation.PathParams = []catalogField{{Name: "workspaceId", Required: true}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/client" {
					t.Errorf("help requested %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				writeJSON(t, w, clientCatalog{SchemaVersion: 1, Routes: []route{operation}, Commands: []cliCommand{{
					ID: "custom.shortcut", OperationID: operation.ID, Command: "craken custom shortcut", Description: "Different shortcut plan", Group: "Custom",
					Execution: commandExecution{Multipart: commandMultipartPlan{FileOption: "shortcut-file"}},
				}}})
			}))
			defer server.Close()
			stdout, err := runGenericTransport(t, server.URL, []string{"do", operation.ID, "--help"})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range scenario.want {
				if !strings.Contains(stdout, want) {
					t.Errorf("missing %q in help:\n%s", want, stdout)
				}
			}
			if strings.Count(stdout, "--workspace-id") != 1 || strings.Contains(stdout, "--shortcut-file") {
				t.Fatalf("duplicate/shortcut-only path options:\n%s", stdout)
			}
		})
	}
}

func TestDoHTTPExecutionPreservesTokenProfileSaving(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	operation := genericTransportRoute("new.session.issue", http.MethodPost, "/custom/session", "json", &commandExecution{
		Transport:  commandTransportHTTP,
		BodyFields: map[string]commandBinding{"email": {Source: commandBindingSourceOption, Option: "email", Required: true}},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client" {
			writeJSON(t, w, clientCatalog{SchemaVersion: 1, Routes: []route{operation}})
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || string(body) != `{"email":"actor@example.com"}` {
			t.Errorf("request=%s %q", r.Method, body)
		}
		writeJSON(t, w, map[string]any{"token": "new-session-token"})
	}))
	defer server.Close()
	_, err := runGenericTransport(t, server.URL, []string{"do", operation.ID, "--email", "actor@example.com", "--save-token-profile", "saved"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["saved"].Token != "new-session-token" || cfg.Profiles["saved"].BaseURL != server.URL {
		t.Fatalf("saved profile=%v", cfg.Profiles["saved"])
	}
}
