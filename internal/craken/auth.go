package craken

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const sessionBearerPrefix = "craken-session."

// sessionClaims holds the parts of a craken session bearer token that decide who
// the CLI is acting as. The token is base64url(JSON).signature; decoding the
// payload is informational only (no signature check) and lets the CLI confirm a
// profile labeled "agent" really carries delegated-agent scopes.
type sessionClaims struct {
	Email          string                `json:"email"`
	Name           string                `json:"name"`
	Provider       string                `json:"provider"`
	DelegatedAgent *delegatedAgentClaims `json:"delegatedAgent"`
}

type delegatedAgentClaims struct {
	AgentID     string   `json:"agentId"`
	ClientKind  string   `json:"clientKind"`
	Scopes      []string `json:"scopes"`
	WorkspaceID string   `json:"workspaceId"`
}

func decodeSessionClaims(token string) (*sessionClaims, bool) {
	token = trim(token)
	token = strings.TrimPrefix(token, sessionBearerPrefix)
	payload, _, found := strings.Cut(token, ".")
	if !found || payload == "" {
		return nil, false
	}
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	var claims sessionClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, false
	}
	if claims.Email == "" && claims.DelegatedAgent == nil && claims.Provider == "" {
		return nil, false
	}
	return &claims, true
}

func whoami(ctx context.Context, cmd command, stdout io.Writer) error {
	cfg, err := readConfig()
	if err != nil {
		return err
	}
	name := profileName(cmd)
	prof := cfg.Profiles[name]
	token := selectedBearerToken(cmd, prof)

	out := map[string]any{"profile": name}
	if prof.BaseURL != "" {
		out["baseUrl"] = prof.BaseURL
	}
	if prof.Kind != "" {
		out["profileKind"] = prof.Kind
	}
	warnings := []string{}

	if trim(token) == "" {
		out["authenticated"] = false
		out["warnings"] = []string{"no bearer token for this profile; run 'craken auth login'"}
		return printJSON(stdout, out)
	}

	claims, decoded := decodeSessionClaims(token)
	if decoded {
		identity := map[string]any{}
		if claims.Email != "" {
			identity["email"] = claims.Email
		}
		if claims.Name != "" {
			identity["name"] = claims.Name
		}
		if claims.Provider != "" {
			identity["provider"] = claims.Provider
		}
		if len(identity) > 0 {
			out["identity"] = identity
		}
		if claims.DelegatedAgent != nil {
			out["actingAs"] = "agent"
			agent := map[string]any{}
			if claims.DelegatedAgent.AgentID != "" {
				agent["agentId"] = claims.DelegatedAgent.AgentID
			}
			if claims.DelegatedAgent.ClientKind != "" {
				agent["clientKind"] = claims.DelegatedAgent.ClientKind
			}
			if claims.DelegatedAgent.WorkspaceID != "" {
				agent["workspaceId"] = claims.DelegatedAgent.WorkspaceID
			}
			if len(claims.DelegatedAgent.Scopes) > 0 {
				agent["scopes"] = claims.DelegatedAgent.Scopes
			}
			out["agent"] = agent
		} else {
			out["actingAs"] = "user"
		}
		if prof.Kind == "agent" && claims.DelegatedAgent == nil {
			warnings = append(warnings, "profile is labeled kind=agent but its token carries no delegated-agent claims; writes would post under the user identity, not the agent")
		}
	} else {
		warnings = append(warnings, "token is not a decodable craken session token; cannot verify delegated-agent scopes locally")
	}

	if server, err := fetchCurrentSession(ctx, cmd); err != nil {
		warnings = append(warnings, fmt.Sprintf("could not confirm identity with the server: %v", err))
	} else if server != nil {
		out["server"] = server
	}

	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return printJSON(stdout, out)
}

// fetchCurrentSession asks the server who the selected token authenticates as via
// /api/me, returning a compact view of the authenticated identity.
func fetchCurrentSession(ctx context.Context, cmd command) (map[string]any, error) {
	logger, err := newLogger(cmd.string("log-file", ""))
	if err != nil {
		return nil, err
	}
	defer logger.close()
	client, err := newClient(cmd, logger)
	if err != nil {
		return nil, err
	}
	value, err := client.json(ctx, "/api/me")
	if err != nil {
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, nil
	}
	server := map[string]any{}
	if authenticated, ok := root["authenticated"].(bool); ok {
		server["authenticated"] = authenticated
	}
	if user, ok := root["user"].(map[string]any); ok {
		if email, ok := user["email"].(string); ok && email != "" {
			server["email"] = email
		}
		if _, ok := user["delegatedAgent"].(map[string]any); ok {
			server["actingAs"] = "agent"
		} else {
			server["actingAs"] = "user"
		}
	}
	return server, nil
}

var openBrowser = openBrowserDefault

// errAgentSessionUnsupported signals that the server does not expose the direct
// agent-session endpoint, so the caller should fall back to the device flow.
var errAgentSessionUnsupported = errors.New("agent session endpoint not available")

func runAuth(ctx context.Context, cmd command, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	switch cmd.Action {
	case "login":
		return login(ctx, cmd, stdout, stderr)
	case "whoami", "status":
		return whoami(ctx, cmd, stdout)
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
		// Only label the profile as an agent when the minted token actually carries
		// delegated-agent claims. A token that decodes cleanly but lacks them would
		// silently post under the approving user's identity, so refuse the label and
		// warn instead. An undecodable token (unknown format) keeps the prior trust.
		claims, decoded := decodeSessionClaims(result.Token)
		if decoded && claims.DelegatedAgent == nil {
			if _, ferr := fmt.Fprintf(stderr, "warning: the authorization response was labeled an agent login, but the returned token carries no delegated-agent claims (delegatedAgent/scopes). Leaving profile %q unlabeled so it is not mistaken for the agent. Run 'craken auth whoami' to inspect.\n", name); ferr != nil {
				return ferr
			}
		} else {
			prof.Kind = "agent"
			prof.AgentID = result.Agent.ID
			prof.AgentName = result.Agent.Name
			prof.ClientKind = result.Agent.ClientKind
			prof.WorkspaceID = result.Agent.WorkspaceID
		}
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
