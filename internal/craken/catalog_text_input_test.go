package craken

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogTextInput(t *testing.T) {
	body := "## Summary\n\n한글 🙂\n$literal `code` \\n\n"
	for _, resource := range []string{"channel", "dm", "wiki"} {
		for _, source := range []string{"stdin", "file", "inline", "conflict", "read failure"} {
			t.Run(resource+"/"+source, func(t *testing.T) {
				t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
				option, fileOption := "body", "body-file"
				action := "send"
				if resource == "wiki" {
					option, fileOption, action = "content", "content-file", "save"
				}
				posts := 0
				var posted map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path == "/api/client" {
						writeJSON(t, w, clientCatalog{
							SchemaVersion: 1,
							Routes:        []route{{ID: "text.create", Method: http.MethodPost, Path: "/text", Description: "Write text", Auth: "required", RequestBody: "json"}},
							Commands: []cliCommand{{ID: resource + "." + action, Group: "Text", Command: "craken " + resource + " " + action, Description: "Write text", Execution: commandExecution{
								OperationID: "text.create", BodyFields: map[string]commandBinding{option: {Source: commandBindingSourceText, Option: option, FileOption: fileOption, Required: true}},
							}}},
						})
						return
					}
					if r.Method != http.MethodPost || r.URL.Path != "/text" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					posts++
					if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
						t.Error(err)
					}
					writeJSON(t, w, map[string]any{"ok": true})
				}))
				defer server.Close()
				args := []string{resource, action, "--base-url", server.URL, "--token", "local-test"}
				var stdin io.Reader = strings.NewReader(body)
				switch source {
				case "stdin", "read failure":
					args = append(args, "--"+fileOption, "-")
					if source == "read failure" {
						stdin = failingTextReader{}
					}
				case "file":
					path := filepath.Join(t.TempDir(), "text.md")
					if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
					args = append(args, "--"+fileOption, path)
				case "inline":
					args = append(args, "--"+option, body)
				case "conflict":
					args = append(args, "--"+option, body, "--"+fileOption, "-")
				}
				err := Run(context.Background(), "dev", args, stdin, &bytes.Buffer{}, &bytes.Buffer{})
				if source == "conflict" || source == "read failure" {
					if err == nil || posts != 0 {
						t.Fatalf("expected validation failure without writes, got err=%v posts=%d", err, posts)
					}
					want := "not both"
					if source == "read failure" {
						want = "stdin failed"
					}
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("expected %q error, got %v", want, err)
					}
					return
				}
				if err != nil || posts != 1 || posted[option] != body {
					t.Fatalf("expected one verbatim write, got err=%v posts=%d body=%#v", err, posts, posted)
				}
			})
		}
	}
}

type failingTextReader struct{}

func (failingTextReader) Read([]byte) (int, error) {
	return 0, errors.New("stdin failed")
}
