package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSignalsStopBlockedSubscriptions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide Unix process signal exit codes")
	}
	binary := filepath.Join(t.TempDir(), "craken")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, scenario := range []struct {
		name   string
		signal os.Signal
		code   int
	}{{"interrupt", os.Interrupt, 130}, {"terminate", syscall.SIGTERM, 143}} {
		t.Run(scenario.name, func(t *testing.T) {
			ready, closed := make(chan struct{}), make(chan struct{})
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/client" {
					_, _ = io.WriteString(w, `{"schemaVersion":1,"commands":[{"id":"workspace.listen","group":"Workspace","command":"craken workspace listen","description":"Listen","operationId":"realtime","execution":{"transport":"websocket"}}],"routes":[{"id":"realtime","method":"GET","path":"/stream","description":"Stream","auth":"required","requestBody":"none","stream":"websocket"}]}`)
					return
				}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				close(ready)
				_, _, _ = conn.ReadMessage()
				close(closed)
			}))
			defer server.Close()
			child := exec.Command(binary, "workspace", "listen", "--base-url", server.URL, "--token", "fixture")
			child.Env = append(os.Environ(), "CRAKEN_CONFIG_DIR="+t.TempDir())
			var stderr bytes.Buffer
			child.Stderr = &stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer child.Process.Kill()
			finished := make(chan error, 1)
			go func() { finished <- child.Wait() }()
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				t.Fatal("subscription did not connect")
			}
			if err := child.Process.Signal(scenario.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != scenario.code {
					t.Fatalf("exit %v, want %d; stderr %s", err, scenario.code, &stderr)
				}
			case <-time.After(time.Second):
				t.Fatal("signal left CLI running")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("signal left subscription open")
			}
		})
	}
}
