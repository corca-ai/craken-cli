package craken

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DiagnosticError records evidence, not whether a mutation had an effect.
type DiagnosticError struct {
	Stage    string            `json:"stage"`
	Message  string            `json:"message"`
	Response *responseEvidence `json:"response,omitempty"`
	cause    error
}

func (e *DiagnosticError) Error() string { return e.Message }
func (e *DiagnosticError) Unwrap() error { return e.cause }

type responseEvidence struct {
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
	Body    any         `json:"body,omitempty"`
}

func stageError(stage string, err error, evidence *responseEvidence) error {
	if err == nil {
		return nil
	}
	var diagnostic *DiagnosticError
	if errors.As(err, &diagnostic) {
		if diagnostic.Response == nil && evidence != nil {
			copy := *diagnostic
			copy.Response = evidence
			return &copy
		}
		return err
	}
	return &DiagnosticError{Stage: stage, Message: err.Error(), Response: evidence, cause: err}
}
func responseInfo(response *http.Response) *responseEvidence {
	headers := response.Header.Clone()
	for _, name := range []string{"Set-Cookie", "Authorization", "Proxy-Authorization", "Cookie"} {
		headers.Del(name)
	}
	return &responseEvidence{Status: response.StatusCode, Headers: headers}
}
func httpFailure(response *http.Response, method, path, text string) error {
	evidence := responseInfo(response)
	var body any
	if json.Unmarshal([]byte(text), &body) != nil {
		body = text
	}
	evidence.Body = redactSecrets(body)
	if _, structured := body.(string); !structured {
		if encoded, err := json.Marshal(evidence.Body); err == nil {
			text = string(encoded)
		}
	}
	return &DiagnosticError{Stage: "response", Message: fmt.Sprintf("%s %s failed with %d: %s", method, path, response.StatusCode, text), Response: evidence}
}
func redactSecrets(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for name, child := range typed {
			switch strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name)) {
			case "token", "accesstoken", "refreshtoken", "authorization", "cookie", "password", "secret":
				out[name] = "[redacted]"
			default:
				out[name] = redactSecrets(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactSecrets(child)
		}
		return out
	default:
		return value
	}
}

// WriteError keeps stdout untouched and leaves exit status selection to the executable.
func WriteError(stderr io.Writer, err error, args []string) {
	jsonFormat := false
	for index, arg := range args {
		if arg == "--error-format=json" || (arg == "--error-format" && index+1 < len(args) && args[index+1] == "json") {
			jsonFormat = true
		}
	}
	if !jsonFormat {
		fmt.Fprintln(stderr, err)
		return
	}
	wrapped := stageError("prepare", err, nil)
	var diagnostic *DiagnosticError
	if errors.As(wrapped, &diagnostic) {
		copy := *diagnostic
		if copy.Response != nil {
			copy.Message = "request failed; inspect response evidence"
		}
		_ = json.NewEncoder(stderr).Encode(&copy)
	}
}
