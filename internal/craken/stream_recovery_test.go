package craken

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestStreamRecoversGapAndResumesAfterRestart(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	var connections atomic.Int32
	var snapshotReads atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workspaces/w/snapshot" {
			snapshotReads.Add(1)
			_, _ = io.WriteString(w, `{"head":5}`)
			return
		}
		index := connections.Add(1)
		expected := map[int32]string{1: "5", 2: "6", 3: "7"}[index]
		if after := r.URL.Query().Get("after"); after != expected {
			t.Errorf("connection %d after=%s, want %s", index, after, expected)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		switch index {
		case 1:
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"entry":{"cursor":6,"message":{"id":"m6","text":"before reset"}}}`))
		case 2:
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"entries":[{"cursor":7,"message":{"id":"m7","text":"during disconnect"}},{"cursor":8,"message":{"id":"m8","text":"later"}}],"scanned":8}`))
		case 3:
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"entry":{"cursor":8,"message":{"id":"m8","text":"later"}}}`))
		}
	}))
	defer server.Close()
	plan := testStreamPlan()
	plan.ResumeQuery = "after"
	plan.DefaultMessages = true
	plan.DefaultReconnect = true
	plan.DefaultResume = true
	plan.Bootstrap.OperationID = "snapshot"
	plan.Bootstrap.ResultPath = "head"
	execution := commandExecution{WebSocket: commandWebSocketPlan{Stream: plan}}
	c := &client{baseURL: server.URL, token: "opaque-identity", httpClient: server.Client()}
	routes := []route{{ID: "snapshot", Method: "GET", Path: "/workspaces/{workspaceId}/snapshot"}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var first bytes.Buffer
	err := runCatalogWebSocketCommand(ctx, c, routes, execution, command{Options: map[string]string{"limit": "2", "retry-delay-ms": "1"}}, "/stream", map[string]string{"workspaceId": "w"}, map[string]bool{}, &first, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(first.String()), "\n"); len(lines) != 2 || !strings.Contains(lines[1], "m7") {
		t.Fatalf("gap output %q", first.String())
	}
	var next bytes.Buffer
	err = runCatalogWebSocketCommand(ctx, c, routes, execution, command{Options: map[string]string{"limit": "1"}}, "/stream", map[string]string{"workspaceId": "w"}, map[string]bool{}, &next, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next.String(), "m8") || snapshotReads.Load() != 1 {
		t.Fatalf("restart output %q snapshot reads %d", next.String(), snapshotReads.Load())
	}
}

func TestStreamCancellationInterruptsBlockedRead(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	ready := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		close(ready)
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runCatalogWebSocketCommand(ctx, &client{baseURL: server.URL}, nil, commandExecution{}, command{}, "/stream", nil, map[string]bool{}, io.Discard, io.Discard)
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation left a blocked read")
	}
}

func TestStreamTerminalErrorsAndRetryBudget(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		status, want int
	}{{"authentication", 401, 1}, {"permission", 403, 1}, {"transient", 503, 3}} {
		t.Run(scenario.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(scenario.status) }))
			defer server.Close()
			descriptor := testStreamPlan()
			descriptor.ResumeQuery = "after"
			plan := commandExecution{QueryParams: map[string]commandBinding{"after": {Source: commandBindingSourceLiteral, Value: 0}}, WebSocket: commandWebSocketPlan{Stream: descriptor}}
			cmd := command{Options: map[string]string{"max-retries": "2", "retry-delay-ms": "1"}, Flags: map[string]bool{"reconnect": true}}
			var stdout, stderr bytes.Buffer
			err := runCatalogWebSocketCommand(context.Background(), &client{baseURL: server.URL}, nil, plan, cmd, "/stream", nil, map[string]bool{}, &stdout, &stderr)
			if err == nil || int(calls.Load()) != scenario.want || stdout.Len() != 0 {
				t.Fatalf("error %v calls %d stdout %s", err, calls.Load(), &stdout)
			}
			if scenario.status == 503 && (!strings.Contains(err.Error(), "exhausted 2 retries") || !strings.Contains(stderr.String(), "retry 2/2")) {
				t.Fatalf("missing retry outcome: %v %s", err, &stderr)
			}
		})
	}
}

func TestStreamCancellationInterruptsBackoff(t *testing.T) {
	ready := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); close(ready) }))
	defer server.Close()
	descriptor := testStreamPlan()
	descriptor.ResumeQuery = "after"
	plan := commandExecution{QueryParams: map[string]commandBinding{"after": {Source: commandBindingSourceLiteral, Value: 0}}, WebSocket: commandWebSocketPlan{Stream: descriptor}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runCatalogWebSocketCommand(ctx, &client{baseURL: server.URL}, nil, plan, command{Options: map[string]string{"retry-delay-ms": "5000"}, Flags: map[string]bool{"reconnect": true}}, "/stream", nil, map[string]bool{}, io.Discard, io.Discard)
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation left a reconnect delay")
	}
}

func TestStreamMessageWaitIgnoresContinuousOtherFrames(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for index := 0; index < 40; index++ {
			if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"entries":[],"scanned":1}`)); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer server.Close()
	descriptor := testStreamPlan()
	var out, diagnostics bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := runCatalogWebSocketCommand(ctx, &client{baseURL: server.URL}, nil, commandExecution{WebSocket: commandWebSocketPlan{Stream: descriptor}}, command{Options: map[string]string{"wait-timeout-ms": "30"}, Flags: map[string]bool{"messages": true}}, "/stream", nil, map[string]bool{}, &out, &diagnostics)
	if err != nil || out.Len() != 0 || !strings.Contains(diagnostics.String(), "matching-message wait timeout") {
		t.Fatalf("error %v output %s diagnostic %s", err, &out, &diagnostics)
	}
}

func streamFixtureToken(email, agent string) string {
	claims := sessionClaims{Email: email}
	if agent != "" {
		claims.DelegatedAgent = &delegatedAgentClaims{AgentID: agent, WorkspaceID: "w"}
	}
	raw, _ := json.Marshal(claims)
	return fmt.Sprintf("%s%s.sig", sessionBearerPrefix, base64.RawURLEncoding.EncodeToString(raw))
}
