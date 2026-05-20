package craken

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"
)

var openBrowser = openBrowserDefault

func runAuth(ctx context.Context, cmd command, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	switch cmd.Action {
	case "login":
		return login(ctx, cmd, stdout, stderr)
	case "import-token":
		token, err := requiredTokenOption(cmd, stdin)
		if err != nil {
			return err
		}
		cfg, err := readConfig()
		if err != nil {
			return err
		}
		name := profileName(cmd)
		prof := cfg.Profiles[name]
		if baseURL := cmd.string("base-url", ""); baseURL != "" {
			prof.BaseURL = baseURL
		}
		prof.Token = token
		cfg.Profiles[name] = prof
		if err := writeConfig(cfg); err != nil {
			return err
		}
		return printJSON(stdout, map[string]any{"profile": name, "tokenType": "Bearer"})
	default:
		return fmt.Errorf("unknown auth action: %s", cmd.Action)
	}
}

func login(ctx context.Context, cmd command, stdout io.Writer, stderr io.Writer) error {
	cfg, err := readConfig()
	if err != nil {
		return err
	}
	name := profileName(cmd)
	prof := cfg.Profiles[name]
	baseURL := cmd.string("base-url", "")
	if baseURL == "" {
		baseURL = prof.BaseURL
	}
	if baseURL == "" {
		baseURL = getenvTrim("CRAKEN_BASE_URL")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	timeoutMS, err := numberOption(cmd, "timeout-ms", 120000)
	if err != nil {
		return err
	}
	state, err := randomState()
	if err != nil {
		return err
	}
	result, err := receiveBrowserLogin(ctx, baseURL, state, time.Duration(timeoutMS)*time.Millisecond, boolOption(cmd, "no-open"), stderr)
	if err != nil {
		return err
	}
	prof.BaseURL = baseURL
	prof.Token = result.Token
	cfg.Profiles[name] = prof
	if err := writeConfig(cfg); err != nil {
		return err
	}
	return printJSON(stdout, map[string]any{"profile": name, "session": result.Session, "tokenType": result.TokenType})
}

type loginResult struct {
	Session   any
	Token     string
	TokenType string
}

func receiveBrowserLogin(ctx context.Context, baseURL string, state string, timeout time.Duration, noOpen bool, stderr io.Writer) (loginResult, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return loginResult{}, err
	}
	defer func() { _ = listener.Close() }()

	resultCh := make(chan loginResult, 1)
	errCh := make(chan error, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			if err := r.ParseForm(); err != nil {
				errCh <- err
				http.Error(w, "Invalid callback", http.StatusBadRequest)
				return
			}
			if r.PostForm.Get("state") != state {
				errCh <- fmt.Errorf("browser login returned an invalid state")
				http.Error(w, "Invalid login state", http.StatusBadRequest)
				return
			}
			token := trim(r.PostForm.Get("token"))
			if token == "" {
				errCh <- fmt.Errorf("browser login did not return a token")
				http.Error(w, "Missing token", http.StatusBadRequest)
				return
			}
			var session any
			if raw := r.PostForm.Get("session"); raw != "" {
				_ = json.Unmarshal([]byte(raw), &session)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, "<!doctype html><title>Craken CLI Login</title><p>Craken CLI login complete. You can close this tab.</p>")
			resultCh <- loginResult{Session: session, Token: token, TokenType: firstNonEmpty(r.PostForm.Get("tokenType"), "Bearer")}
		}),
	}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()
	defer func() { _ = server.Shutdown(context.Background()) }()

	loginURL, err := buildLoginURL(baseURL, listener.Addr().(*net.TCPAddr).Port, state)
	if err != nil {
		return loginResult{}, err
	}
	if _, err := fmt.Fprintf(stderr, "Open this URL to log in:\n%s\n", loginURL); err != nil {
		return loginResult{}, err
	}
	if !noOpen {
		openBrowser(loginURL)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return loginResult{}, ctx.Err()
	case <-timer.C:
		return loginResult{}, fmt.Errorf("browser login timed out after %dms", timeout.Milliseconds())
	case err := <-errCh:
		return loginResult{}, err
	case result := <-resultCh:
		return result, nil
	}
}

func buildLoginURL(baseURL string, port int, state string) (string, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	login := endpoint.ResolveReference(&url.URL{Path: "/api/client/login"})
	params := login.Query()
	params.Set("redirect_uri", fmt.Sprintf("http://127.0.0.1:%d/callback", port))
	params.Set("state", state)
	login.RawQuery = params.Encode()
	return login.String(), nil
}

func randomState() (string, error) {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func openBrowserDefault(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}

func getenvTrim(name string) string {
	return trim(getenv(name))
}

var getenv = func(name string) string {
	return os.Getenv(name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trim(value) != "" {
			return trim(value)
		}
	}
	return ""
}
