package craken

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestWebSocketProjection(t *testing.T) {
	var out bytes.Buffer
	err := printWebSocketMessage(&out, []byte(`{"activity":{"event":{"message":{"id":"m","body":"first\nsecond","sender":{"id":"u","picture":"huge"}}}},"type":"workspace.activity"}`), command{Options: map[string]string{"fields": "activity.event.message.id,activity.event.message.body"}})
	if err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := `{"activity":{"event":{"message":{"body":"first\nsecond","id":"m"}}}}`
	encoded, _ := json.Marshal(got)
	if string(encoded) != want {
		t.Fatalf("got %s, want %s", encoded, want)
	}
}
