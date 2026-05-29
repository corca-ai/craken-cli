package craken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"
)

var openBrowser = openBrowserDefault

// errAgentSessionUnsupported signals that the server does not expose the direct
// agent-session endpoint, so the caller should fall back to the device flow.
var errAgentSessionUnsupported = errors.New("agent session endpoint not available")

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
	timeoutMS, err := numberOption(cmd, "timeout-ms", 300000)
	if err != nil {
		return err
	}
	var result loginResult
	if boolOption(cmd, "as-agent") {
		request, err := agentLoginRequestFromCommand(cmd)
		if err != nil {
			return err
		}
		agentResult, err := authorizeAgent(ctx, cfg, cmd, baseURL, time.Duration(timeoutMS)*time.Millisecond, request, stderr)
		if err != nil {
			return err
		}
		result = agentResult
	} else {
		userResult, err := receiveDeviceLogin(ctx, baseURL, time.Duration(timeoutMS)*time.Millisecond, boolOption(cmd, "no-open"), stderr)
		if err != nil {
			return err
		}
		result = userResult
	}
	prof.BaseURL = baseURL
	prof.Token = result.Token
	if result.Agent != nil {
		prof.Kind = "agent"
		prof.AgentID = result.Agent.ID
		prof.AgentName = result.Agent.Name
		prof.ClientKind = result.Agent.ClientKind
		prof.WorkspaceID = result.Agent.WorkspaceID
	}
	cfg.Profiles[name] = prof
	if err := writeConfig(cfg); err != nil {
		return err
	}
	return printJSON(stdout, map[string]any{"profile": name, "session": result.Session, "tokenType": result.TokenType})
}

type loginResult struct {
	Agent     *loginAgent
	Session   any
	Token     string
	TokenType string
}

type loginAgent struct {
	ClientKind  string
	ID          string
	Name        string
	WorkspaceID string
}

type deviceAuthorizationResponse struct {
	DeviceCode              string `json:"deviceCode"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
}

type deviceTokenResponse struct {
	Agent     *deviceTokenAgent `json:"agent"`
	Error     string            `json:"error"`
	Message   string            `json:"message"`
	Session   any               `json:"session"`
	Token     string            `json:"token"`
	TokenType string            `json:"tokenType"`
}

type deviceTokenAgent struct {
	AgentID    string `json:"agentId"`
	ClientKind string `json:"clientKind"`
	Name       string `json:"name"`
}

type agentLoginRequest struct {
	AgentName   string   `json:"agentName"`
	ClientKind  string   `json:"clientKind"`
	ClientLabel string   `json:"clientLabel,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	WorkspaceID string   `json:"workspaceId"`
}

func receiveDeviceLogin(ctx context.Context, baseURL string, timeout time.Duration, noOpen bool, stderr io.Writer) (loginResult, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	authorization, err := startDeviceAuthorization(ctx, httpClient, baseURL)
	if err != nil {
		return loginResult{}, err
	}
	loginURL := firstNonEmpty(authorization.VerificationURIComplete, authorization.VerificationURI)
	if loginURL == "" {
		return loginResult{}, fmt.Errorf("device login response did not include a verification URL")
	}
	if _, err := fmt.Fprintf(stderr, "Open this URL to log in:\n%s\n\nCode: %s\nWaiting for authorization...\n", loginURL, authorization.UserCode); err != nil {
		return loginResult{}, err
	}
	if !noOpen {
		openBrowser(loginURL)
	}

	timeout = shorterPositiveDuration(timeout, time.Duration(authorization.ExpiresIn)*time.Second)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	pollInterval := time.Duration(maxInt(authorization.Interval, 1)) * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		result, pending, err := pollDeviceToken(ctx, httpClient, baseURL, authorization.DeviceCode)
		if err != nil {
			return loginResult{}, err
		}
		if !pending {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return loginResult{}, ctx.Err()
		case <-deadline.C:
			return loginResult{}, fmt.Errorf("browser login timed out after %dms", timeout.Milliseconds())
		case <-ticker.C:
		}
	}
}

func receiveAgentDeviceLogin(ctx context.Context, baseURL string, timeout time.Duration, request agentLoginRequest, noOpen bool, stderr io.Writer) (loginResult, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	authorization, err := startAgentDeviceAuthorization(ctx, httpClient, baseURL, request)
	if err != nil {
		return loginResult{}, err
	}
	loginURL := firstNonEmpty(authorization.VerificationURIComplete, authorization.VerificationURI)
	if loginURL == "" {
		return loginResult{}, fmt.Errorf("agent device login response did not include a verification URL")
	}
	if _, err := fmt.Fprintf(stderr, "Open this URL to authorize %s:\n%s\n\nCode: %s\nWaiting for authorization...\n", request.AgentName, loginURL, authorization.UserCode); err != nil {
		return loginResult{}, err
	}
	if !noOpen {
		openBrowser(loginURL)
	}

	timeout = shorterPositiveDuration(timeout, time.Duration(authorization.ExpiresIn)*time.Second)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	pollInterval := time.Duration(maxInt(authorization.Interval, 1)) * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		result, pending, err := pollAgentDeviceToken(ctx, httpClient, baseURL, authorization.DeviceCode, request.WorkspaceID)
		if err != nil {
			return loginResult{}, err
		}
		if !pending {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return loginResult{}, ctx.Err()
		case <-deadline.C:
			return loginResult{}, fmt.Errorf("agent browser login timed out after %dms", timeout.Milliseconds())
		case <-ticker.C:
		}
	}
}

func startDeviceAuthorization(ctx context.Context, httpClient *http.Client, baseURL string) (deviceAuthorizationResponse, error) {
	var authorization deviceAuthorizationResponse
	status, raw, err := postPublicJSON(ctx, httpClient, baseURL, "/api/client/device-authorizations", map[string]any{}, &authorization)
	if err != nil {
		return deviceAuthorizationResponse{}, err
	}
	if status < 200 || status >= 300 {
		return deviceAuthorizationResponse{}, fmt.Errorf("device login authorization failed with %d: %s", status, string(raw))
	}
	if trim(authorization.DeviceCode) == "" || trim(authorization.UserCode) == "" {
		return deviceAuthorizationResponse{}, fmt.Errorf("device login authorization response was incomplete")
	}
	return authorization, nil
}

func startAgentDeviceAuthorization(ctx context.Context, httpClient *http.Client, baseURL string, request agentLoginRequest) (deviceAuthorizationResponse, error) {
	var authorization deviceAuthorizationResponse
	status, raw, err := postPublicJSON(ctx, httpClient, baseURL, "/api/client/agent-device-authorizations", request, &authorization)
	if err != nil {
		return deviceAuthorizationResponse{}, err
	}
	if status < 200 || status >= 300 {
		return deviceAuthorizationResponse{}, fmt.Errorf("agent device login authorization failed with %d: %s", status, string(raw))
	}
	if trim(authorization.DeviceCode) == "" || trim(authorization.UserCode) == "" {
		return deviceAuthorizationResponse{}, fmt.Errorf("agent device login authorization response was incomplete")
	}
	return authorization, nil
}

func pollDeviceToken(ctx context.Context, httpClient *http.Client, baseURL string, deviceCode string) (loginResult, bool, error) {
	var token deviceTokenResponse
	status, raw, err := postPublicJSON(ctx, httpClient, baseURL, "/api/client/device-token", map[string]string{"deviceCode": deviceCode}, &token)
	if err != nil {
		return loginResult{}, false, err
	}
	if status == http.StatusTooEarly || token.Error == "authorization_pending" {
		return loginResult{}, true, nil
	}
	if status < 200 || status >= 300 {
		if token.Message != "" {
			return loginResult{}, false, fmt.Errorf("device login failed: %s", token.Message)
		}
		return loginResult{}, false, fmt.Errorf("device login failed with %d: %s", status, string(raw))
	}
	if trim(token.Token) == "" {
		return loginResult{}, false, fmt.Errorf("device login token response did not include a token")
	}
	return loginResult{Session: token.Session, Token: token.Token, TokenType: firstNonEmpty(token.TokenType, "Bearer")}, false, nil
}

func pollAgentDeviceToken(ctx context.Context, httpClient *http.Client, baseURL string, deviceCode string, workspaceID string) (loginResult, bool, error) {
	var token deviceTokenResponse
	status, raw, err := postPublicJSON(ctx, httpClient, baseURL, "/api/client/agent-device-token", map[string]string{"deviceCode": deviceCode}, &token)
	if err != nil {
		return loginResult{}, false, err
	}
	if status == http.StatusTooEarly || token.Error == "authorization_pending" {
		return loginResult{}, true, nil
	}
	if status < 200 || status >= 300 {
		if token.Message != "" {
			return loginResult{}, false, fmt.Errorf("agent device login failed: %s", token.Message)
		}
		return loginResult{}, false, fmt.Errorf("agent device login failed with %d: %s", status, string(raw))
	}
	if trim(token.Token) == "" {
		return loginResult{}, false, fmt.Errorf("agent device login token response did not include a token")
	}
	var agent *loginAgent
	if token.Agent != nil {
		agent = &loginAgent{ClientKind: token.Agent.ClientKind, ID: token.Agent.AgentID, Name: token.Agent.Name, WorkspaceID: workspaceID}
	}
	return loginResult{Agent: agent, Session: token.Session, Token: token.Token, TokenType: firstNonEmpty(token.TokenType, "Bearer")}, false, nil
}

func agentLoginRequestFromCommand(cmd command) (agentLoginRequest, error) {
	workspaceID := cmd.string("workspace", cmd.string("workspace-id", ""))
	agentName := cmd.string("agent-name", "")
	clientKind := cmd.string("client-kind", "custom")
	if workspaceID == "" || agentName == "" {
		return agentLoginRequest{}, fmt.Errorf("--as-agent requires --workspace and --agent-name")
	}
	return agentLoginRequest{
		AgentName:   agentName,
		ClientKind:  clientKind,
		ClientLabel: cmd.string("client-label", ""),
		Scopes:      stringListOption(cmd, "scopes"),
		WorkspaceID: workspaceID,
	}, nil
}

// authorizeAgent provisions an agent identity. When an existing user bearer token
// is available it mints the agent session directly (no browser approval); it only
// falls back to the browser device flow when there is no user token, when the
// server lacks the direct endpoint, or when the caller forces it with --device-code.
func authorizeAgent(ctx context.Context, cfg config, cmd command, baseURL string, timeout time.Duration, request agentLoginRequest, stderr io.Writer) (loginResult, error) {
	if boolOption(cmd, "device-code") {
		return receiveAgentDeviceLogin(ctx, baseURL, timeout, request, boolOption(cmd, "no-open"), stderr)
	}
	userToken := userTokenForAgentLogin(cfg, cmd)
	if userToken == "" {
		return receiveAgentDeviceLogin(ctx, baseURL, timeout, request, boolOption(cmd, "no-open"), stderr)
	}
	result, err := receiveAgentSession(ctx, baseURL, userToken, request)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, errAgentSessionUnsupported) && !boolOption(cmd, "no-device-fallback") {
		if _, ferr := fmt.Fprintf(stderr, "Direct agent authorization is unavailable on this server; falling back to browser approval...\n"); ferr != nil {
			return loginResult{}, ferr
		}
		return receiveAgentDeviceLogin(ctx, baseURL, timeout, request, boolOption(cmd, "no-open"), stderr)
	}
	return loginResult{}, err
}

// userTokenForAgentLogin resolves the user bearer token used to authorize an
// agent without a browser round-trip. Precedence: explicit --use-user-profile,
// then CRAKEN_TOKEN, then the "default" profile, then a single user profile if
// exactly one exists. Agent profiles are never used to mint further agents.
func userTokenForAgentLogin(cfg config, cmd command) string {
	if name := cmd.string("use-user-profile", ""); name != "" {
		return trim(cfg.Profiles[name].Token)
	}
	if env := getenvTrim("CRAKEN_TOKEN"); env != "" {
		return env
	}
	if prof, ok := cfg.Profiles["default"]; ok && trim(prof.Token) != "" && prof.Kind != "agent" {
		return trim(prof.Token)
	}
	token := ""
	count := 0
	for _, prof := range cfg.Profiles {
		if trim(prof.Token) != "" && prof.Kind != "agent" {
			token = trim(prof.Token)
			count++
		}
	}
	if count == 1 {
		return token
	}
	return ""
}

// receiveAgentSession mints an agent bearer token from the authenticated
// /api/client/agent-sessions endpoint using the caller's existing user token.
func receiveAgentSession(ctx context.Context, baseURL string, userToken string, request agentLoginRequest) (loginResult, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	var token deviceTokenResponse
	status, raw, err := postJSONWithToken(ctx, httpClient, baseURL, "/api/client/agent-sessions", userToken, request, &token)
	if err != nil {
		return loginResult{}, err
	}
	if status == http.StatusNotFound {
		return loginResult{}, errAgentSessionUnsupported
	}
	if status < 200 || status >= 300 {
		if token.Message != "" {
			return loginResult{}, fmt.Errorf("agent authorization failed: %s", token.Message)
		}
		return loginResult{}, fmt.Errorf("agent authorization failed with %d: %s", status, string(raw))
	}
	if trim(token.Token) == "" {
		return loginResult{}, fmt.Errorf("agent authorization response did not include a token")
	}
	var agent *loginAgent
	if token.Agent != nil {
		agent = &loginAgent{ClientKind: token.Agent.ClientKind, ID: token.Agent.AgentID, Name: token.Agent.Name, WorkspaceID: request.WorkspaceID}
	}
	return loginResult{Agent: agent, Session: token.Session, Token: token.Token, TokenType: firstNonEmpty(token.TokenType, "Bearer")}, nil
}

func postPublicJSON(ctx context.Context, httpClient *http.Client, baseURL string, path string, body any, target any) (int, []byte, error) {
	return postJSONWithToken(ctx, httpClient, baseURL, path, "", body, target)
}

// postJSONWithToken posts a JSON body to a public endpoint, attaching a bearer
// token when one is provided. An empty token leaves the request unauthenticated.
func postJSONWithToken(ctx context.Context, httpClient *http.Client, baseURL string, path string, token string, body any, target any) (int, []byte, error) {
	endpoint, err := resolvePublicEndpoint(baseURL, path)
	if err != nil {
		return 0, nil, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytesReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if trim(token) != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, nil, err
	}
	if len(raw) > 0 && target != nil {
		_ = json.Unmarshal(raw, target)
	}
	return response.StatusCode, raw, nil
}

func resolvePublicEndpoint(baseURL string, path string) (string, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	return endpoint.ResolveReference(&url.URL{Path: path}).String(), nil
}

func shorterPositiveDuration(left time.Duration, right time.Duration) time.Duration {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
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
