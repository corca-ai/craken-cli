package craken

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func testStreamPlan() *commandStreamPlan {
	return &commandStreamPlan{SinglePath: "entry", BatchPath: "entries", CursorPath: "cursor", CheckpointPath: "scanned", RequiredPath: "message", OutputFields: map[string]string{"body": "message.text", "messageId": "message.id", "sender.id": "message.author.id", "sequence": "cursor"}}
}

func TestMessageStreamBatchLimitsAndCheckpoints(t *testing.T) {
	cmd := command{Options: map[string]string{"limit": "1"}, Flags: map[string]bool{"messages": true}}
	output, err := newStreamOutput(cmd, testStreamPlan())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	frame := []byte(`{"entries":[{"cursor":10,"trace":"ignore"},{"cursor":11,"message":{"id":"m1","text":"first\nsecond","author":{"id":"u","picture":"large"}}},{"cursor":12,"message":{"id":"m2"}}],"scanned":20}`)
	done, err := output.emitFrame(&out, frame)
	if err != nil {
		t.Fatal(err)
	}
	if !done || output.seen != 1 || output.cursor != 11 {
		t.Fatalf("done=%t seen=%d cursor=%d", done, output.seen, output.cursor)
	}
	if want := `{"body":"first\nsecond","messageId":"m1","sender":{"id":"u"},"sequence":11}` + "\n"; out.String() != want {
		t.Fatalf("output %q, want %q", out.String(), want)
	}
	// A separate complete pass advances past filtered/private rows in the scan.
	output.limit = 0
	if _, err = output.emitFrame(&out, []byte(`{"entries":[],"scanned":20}`)); err != nil {
		t.Fatal(err)
	}
	if output.cursor != 20 || output.seen != 1 {
		t.Fatalf("checkpoint %d, count %d", output.cursor, output.seen)
	}
}

func TestMessageStreamProjectionAndWriteFailure(t *testing.T) {
	cmd := command{Options: map[string]string{"fields": "messageId,sender.id"}, Flags: map[string]bool{"messages": true}}
	output, err := newStreamOutput(cmd, testStreamPlan())
	if err != nil {
		t.Fatal(err)
	}
	frame := []byte(`{"entry":{"cursor":7,"message":{"id":"m","text":"hi","author":{"id":"u"}}}}`)
	if _, err = output.emitFrame(errorWriter{}, frame); err == nil {
		t.Fatal("expected write failure")
	}
	if output.cursor != 0 {
		t.Fatal("failed output advanced cursor")
	}
	var out bytes.Buffer
	if _, err = output.emitFrame(&out, frame); err != nil {
		t.Fatal(err)
	}
	if want := `{"messageId":"m","sender":{"id":"u"}}` + "\n"; out.String() != want {
		t.Fatalf("got %s", &out)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestStreamRejectsUnsupportedOutput(t *testing.T) {
	for _, args := range [][]string{{"--messages", "--pretty"}, {"--compact"}, {"--format=json"}, {"--wait-timeout-ms=10"}, {"--fields=a,"}} {
		cmd, err := parseCommand(args)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = newStreamOutput(cmd, testStreamPlan()); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := newStreamOutput(command{Flags: map[string]bool{"messages": true}}, nil); err == nil {
		t.Fatal("accepted missing server descriptor")
	}
}

func TestMessageWaitDoesNotResetOnUnrelatedFrames(t *testing.T) {
	output, err := newStreamOutput(command{Options: map[string]string{"wait-timeout-ms": "100"}, Flags: map[string]bool{"messages": true}}, testStreamPlan())
	if err != nil {
		t.Fatal(err)
	}
	original := time.Now().Add(time.Second)
	output.waitUntil = original
	if _, err = output.emitFrame(&bytes.Buffer{}, []byte(`{"entries":[],"scanned":12}`)); err != nil {
		t.Fatal(err)
	}
	if !output.waitUntil.Equal(original) {
		t.Fatal("unrelated frame reset message wait")
	}
	if _, err = output.emitFrame(&bytes.Buffer{}, []byte(`{"entry":{"cursor":13,"message":{"id":"m"}}}`)); err != nil {
		t.Fatal(err)
	}
	if output.waitUntil.Equal(original) {
		t.Fatal("matching record did not reset message wait")
	}
}

func TestRawStreamProjectionPreservesEnvelope(t *testing.T) {
	output, err := newStreamOutput(command{Options: map[string]string{"fields": "entry.message.id"}}, testStreamPlan())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err = output.emitFrame(&out, []byte(`{"entry":{"cursor":3,"message":{"id":"m","text":"hello"}}}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"entry":{"message":{"id":"m"}}`) || output.cursor != 3 {
		t.Fatalf("output %s cursor %d", &out, output.cursor)
	}
}
