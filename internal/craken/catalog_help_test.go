package craken

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogHelpTextInputsAndVariants(t *testing.T) {
	t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !writeTestCatalog(t, w, r) {
			t.Errorf("help issued unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	for _, update := range []bool{false, true} {
		args := []string{"wiki", "save", "--base-url", server.URL, "--help"}
		operation := "wiki.pages.create\tPOST"
		if update {
			args = append(args, "--existing-title", "Home")
			operation = "wiki.pages.update\tPATCH"
		}
		var out bytes.Buffer
		if err := Run(context.Background(), "dev", args, strings.NewReader(""), &out, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--content TEXT", "--content-file PATH", "not both", "stdin", "--existing-title", "--base-version N", operation} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("expected %q in help without route body schemas:\n%s", want, out.String())
			}
		}
	}
}
