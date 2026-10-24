package craken

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentLoginBindsProfileCredentialsToDestination(t *testing.T) {
	for _, tc := range []struct {
		name                                                                         string
		sourceName                                                                   string
		sameOrigin, matchingUser, secondMatching, explicit, device, legacy, override bool
		envToken                                                                     string
		wantToken, wantPath, wantError                                               string
	}{
		{name: "default other origin", sourceName: "default", wantPath: "/api/client/agent-device-authorizations"},
		{name: "sole user other origin", sourceName: "user-a", wantPath: "/api/client/agent-device-authorizations"},
		{name: "default same origin", sourceName: "default", sameOrigin: true, wantToken: "source-user", wantPath: "/api/client/agent-sessions"},
		{name: "sole user same origin", sourceName: "user-a", sameOrigin: true, wantToken: "source-user", wantPath: "/api/client/agent-sessions"},
		{name: "skip foreign default for matching user", sourceName: "default", matchingUser: true, wantToken: "matching-user", wantPath: "/api/client/agent-sessions"},
		{name: "ambiguous matching users", sourceName: "user-a", matchingUser: true, secondMatching: true, wantPath: "/api/client/agent-device-authorizations"},
		{name: "matching default takes priority", sourceName: "default", sameOrigin: true, matchingUser: true, wantToken: "source-user", wantPath: "/api/client/agent-sessions"},
		{name: "explicit foreign profile", sourceName: "user-a", explicit: true, wantError: "origin"},
		{name: "explicit matching profile", sourceName: "user-a", sameOrigin: true, explicit: true, wantToken: "source-user", wantPath: "/api/client/agent-sessions"},
		{name: "explicit profile before environment", sourceName: "user-a", sameOrigin: true, explicit: true, envToken: "explicit-env", wantToken: "source-user", wantPath: "/api/client/agent-sessions"},
		{name: "environment credential is explicit", sourceName: "default", envToken: "explicit-env", wantToken: "explicit-env", wantPath: "/api/client/agent-sessions"},
		{name: "force device approval", sourceName: "default", sameOrigin: true, device: true, envToken: "explicit-env", wantPath: "/api/client/agent-device-authorizations"},
		{name: "legacy origin is not rebound by environment", sourceName: "default", legacy: true, wantPath: "/api/client/agent-device-authorizations"},
		{name: "base URL override cannot rebind credential", sourceName: "default", override: true, wantPath: "/api/client/agent-device-authorizations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CRAKEN_CONFIG_DIR", t.TempDir())
			t.Setenv("CRAKEN_PROFILE", "")
			t.Setenv("CRAKEN_BASE_URL", "")
			t.Setenv("CRAKEN_TOKEN", tc.envToken)
			type observation struct{ path, authorization string }
			seen := make(chan observation, 4)
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- observation{r.URL.Path, r.Header.Get("Authorization")}
				http.Error(w, "synthetic refusal", http.StatusUnauthorized)
			}))
			defer destination.Close()
			sourceURL := "https://source.example.invalid"
			if tc.sameOrigin {
				sourceURL = destination.URL
			}
			if tc.legacy {
				sourceURL = ""
			}
			profiles := map[string]profile{
				tc.sourceName:  {BaseURL: sourceURL, Token: "source-user"},
				"agent-target": {BaseURL: destination.URL},
			}
			if tc.matchingUser {
				profiles["matching-user"] = profile{BaseURL: destination.URL, Token: "matching-user"}
			}
			if tc.secondMatching {
				profiles["another-matching-user"] = profile{BaseURL: destination.URL, Token: "another-matching-user"}
			}
			if tc.legacy {
				profiles["agent-target"] = profile{}
				t.Setenv("CRAKEN_BASE_URL", destination.URL)
			}
			if tc.override {
				profiles["agent-target"] = profile{BaseURL: sourceURL}
			}
			if err := writeConfig(config{Profiles: profiles}); err != nil {
				t.Fatal(err)
			}
			args := []string{"auth", "login", "--as-agent", "--workspace", "synthetic-workspace", "--agent-name", "synthetic-agent", "--profile", "agent-target", "--no-open"}
			if tc.explicit {
				args = append(args, "--use-user-profile", tc.sourceName)
			}
			if tc.device {
				args = append(args, "--device-code")
			}
			if tc.override {
				args = append(args, "--base-url", destination.URL)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := Run(ctx, "test", args, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("synthetic receiver must refuse login")
			}
			if tc.wantError != "" {
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("got error %v, want %q", err, tc.wantError)
				}
				select {
				case got := <-seen:
					t.Fatalf("explicit mismatch made a request: %s", got.path)
				default:
				}
				return
			}
			select {
			case got := <-seen:
				wantAuthorization := ""
				if tc.wantToken != "" {
					wantAuthorization = "Bearer " + tc.wantToken
				}
				if got.path != tc.wantPath || got.authorization != wantAuthorization {
					t.Fatalf("got %s Authorization=%q; want %s Authorization=%q", got.path, got.authorization, tc.wantPath, wantAuthorization)
				}
			default:
				t.Fatalf("no destination request: %v", err)
			}
		})
	}
}

func TestAgentSessionRedirectOrigin(t *testing.T) {
	for _, same := range []bool{false, true} {
		name := "other origin"
		if same {
			name = "same origin"
		}
		t.Run(name, func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				http.Error(w, "synthetic refusal", http.StatusUnauthorized)
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-user" {
					t.Errorf("missing source bearer")
				}
				if same && r.URL.Path == "/redirected" {
					received.Add(1)
					http.Error(w, "synthetic refusal", http.StatusUnauthorized)
					return
				}
				destination := target.URL
				if same {
					destination = "/redirected"
				}
				http.Redirect(w, r, destination, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			_, err := receiveAgentSession(context.Background(), source.URL, "synthetic-user", agentLoginRequest{})
			if err == nil {
				t.Fatal("synthetic receiver must refuse")
			}
			want := int32(0)
			if same {
				want = 1
			}
			if received.Load() != want {
				t.Fatalf("redirect receiver got %d requests, want %d", received.Load(), want)
			}
			if !same && !strings.Contains(err.Error(), "cross-origin") {
				t.Fatalf("unexpected redirect refusal: %v", err)
			}
		})
	}
}

func TestAgentLoginProfileOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, source, destination, kind, token string
		want                                   bool
	}{
		{name: "HTTPS default port and host case", source: "https://EXAMPLE.test/a", destination: "https://example.test:443/b", token: "user", want: true},
		{name: "HTTP default port", source: "http://example.test:80/a", destination: "http://example.test/b", token: "user", want: true},
		{name: "effective numeric port", source: "https://example.test:0443", destination: "https://example.test", token: "user", want: true},
		{name: "other port", source: "https://example.test:444", destination: "https://example.test", token: "user"},
		{name: "other scheme", source: "https://example.test", destination: "http://example.test", token: "user"},
		{name: "subdomain", source: "https://example.test", destination: "https://other.example.test", token: "user"},
		{name: "legacy built-in origin", destination: defaultBaseURL, token: "user", want: true},
		{name: "relative source", source: "/relative", destination: "https://example.test", token: "user"},
		{name: "invalid source", source: "://invalid", destination: "https://example.test", token: "user"},
		{name: "relative destination", source: "https://example.test", destination: "//example.test", token: "user"},
		{name: "destination userinfo", source: "https://example.test", destination: "https://other@example.test", token: "user"},
		{name: "unsupported scheme", source: "ftp://example.test", destination: "ftp://example.test", token: "user"},
		{name: "invalid port", source: "https://example.test:65536", destination: "https://example.test:65536", token: "user"},
		{name: "IPv6", source: "http://[::1]:80/path", destination: "http://[::1]/", token: "user", want: true},
		{name: "IPv6 zone", source: "http://[fe80::AB%25en0]:80", destination: "http://[FE80::ab%25en0]", token: "user", want: true},
		{name: "different zone case", source: "http://[fe80::ab%25en0]", destination: "http://[fe80::ab%25EN0]", token: "user"},
		{name: "agent is ineligible", source: "https://example.test", destination: "https://example.test", token: "agent", kind: "agent"},
		{name: "empty token", source: "https://example.test", destination: "https://example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agentLoginProfileToken(profile{BaseURL: tc.source, Token: tc.token, Kind: tc.kind}, tc.destination)
			if (got != "") != tc.want {
				t.Fatalf("credential admitted=%t, want %t", got != "", tc.want)
			}
		})
	}
}
