package craken

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStreamCheckpointScopesAndTokenRotation(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	plan := testStreamPlan()
	plan.ResumeQuery = "after"
	output := &streamOutput{plan: plan, messages: true}
	cmd := command{Options: map[string]string{"profile": "agent-a"}}
	c := &client{baseURL: "https://example.test", token: streamFixtureToken("owner@example.test", "a")}
	query := map[string]any{"channelId": "channel-a", "includeDM": true}
	resolved := map[string]string{"workspaceId": "w"}
	path, scope, err := streamCheckpointPath(c, cmd, "/w/realtime", query, resolved, output)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeStreamCheckpoint(path, scope, 7); err != nil {
		t.Fatal(err)
	}
	if cursor, found, err := readStreamCheckpoint(path, scope); err != nil || !found || cursor != 7 {
		t.Fatalf("cursor %d found %t error %v", cursor, found, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint permissions %v", info.Mode())
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("unremoved temporary files: %v", files)
	}
	// Bearer expiry/signature/approving-user changes do not change agent identity.
	c.token = streamFixtureToken("another-owner@example.test", "a")
	same, _, err := streamCheckpointPath(c, cmd, "/w/realtime", map[string]any{"channelId": "channel-a", "includeDM": true, "after": 99}, resolved, output)
	if err != nil || same != path {
		t.Fatalf("token rotation changed scope: %s %v", same, err)
	}
	for _, change := range []struct{ name, server, profile, token, workspace, channel string }{
		{"server", "https://other.test", "agent-a", c.token, "w", "channel-a"},
		{"profile", c.baseURL, "agent-b", c.token, "w", "channel-a"},
		{"identity", c.baseURL, "agent-a", streamFixtureToken("owner@example.test", "b"), "w", "channel-a"},
		{"workspace", c.baseURL, "agent-a", c.token, "other", "channel-a"},
		{"channel", c.baseURL, "agent-a", c.token, "w", "other"},
	} {
		t.Run(change.name, func(t *testing.T) {
			other, _, err := streamCheckpointPath(&client{baseURL: change.server, token: change.token}, command{Options: map[string]string{"profile": change.profile}}, "/w/realtime", map[string]any{"channelId": change.channel, "includeDM": true}, map[string]string{"workspaceId": change.workspace}, output)
			if err != nil || other == path {
				t.Fatalf("shared resume scope: %s %v", other, err)
			}
		})
	}
	if _, _, err = readStreamCheckpoint(path, "wrong-scope"); err == nil {
		t.Fatal("accepted wrong scope")
	}
	if err = os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = readStreamCheckpoint(path, scope); err == nil {
		t.Fatal("accepted corrupt checkpoint")
	}
}
