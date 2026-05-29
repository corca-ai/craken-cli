package craken

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestVersionFlagPrintsVersionWithoutNetwork pins that every documented version
// form prints the version and exits without touching the network. Before the
// fix, `--version` was parsed into cmd.Flags and fell through to the catalog
// help path, which dials /api/client.
func TestVersionFlagPrintsVersionWithoutNetwork(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	for _, arg := range []string{"version", "-version", "--version"} {
		var stdout, stderr bytes.Buffer
		// An unroutable base-url makes the buggy help/network path fail fast.
		err := Run(context.Background(), "v1.2.3", []string{arg, "--base-url", "http://127.0.0.1:0"}, strings.NewReader(""), &stdout, &stderr)
		if err != nil {
			t.Fatalf("arg %q: unexpected error %v", arg, err)
		}
		if stdout.String() != "v1.2.3\n" {
			t.Fatalf("arg %q: expected stdout %q, got %q", arg, "v1.2.3\n", stdout.String())
		}
	}
}
