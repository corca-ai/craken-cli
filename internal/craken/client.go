package craken

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const d1BookmarkHeader = "x-d1-bookmark"

type client struct {
	baseURL       string
	token         string
	httpClient    *http.Client
	bookmark      string
	logger        *logger
	lastResponse  *responseEvidence
	resolverReads map[string]any
}

func newClient(cmd command, log *logger) (*client, error) {
	return newClientWithAuthRequirement(cmd, log, true)
}

func newCatalogClient(cmd command, log *logger) (*client, error) {
	return newClientWithAuthRequirement(cmd, log, false)
}

func newClientWithAuthRequirement(cmd command, log *logger, requireToken bool) (*client, error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, err
	}
	prof := cfg.Profiles[profileName(cmd)]
	baseURL := selectedBaseURL(cmd, prof)

	token := selectedBearerToken(cmd, prof)
	if requireToken && trim(token) == "" {
		return nil, fmt.Errorf("bearer token is required. Run auth import-token, set CRAKEN_TOKEN, or pass --token/--bearer-token")
	}
	return &client{
		baseURL:       baseURL,
		token:         token,
		httpClient:    &http.Client{Timeout: httpTimeout(cmd), CheckRedirect: sameOriginRedirect},
		resolverReads: map[string]any{},
		logger:        log,
	}, nil
}

func (c *client) json(ctx context.Context, path string) (any, error) {
	return c.jsonFromResponse(ctx, http.MethodGet, path, requestSpec{})
}

func (c *client) raw(ctx context.Context, method string, path string, spec requestSpec) (*http.Response, error) {
	endpoint, err := c.resolve(path)
	if err != nil {
		return nil, stageError("prepare", err, nil)
	}
	if c.token != "" {
		base, _ := url.Parse(c.baseURL)
		if endpoint.Scheme != base.Scheme || endpoint.Host != base.Host {
			return nil, fmt.Errorf("refusing to send bearer credentials to another origin")
		}
	}
	for name, values := range spec.Query {
		query := endpoint.Query()
		for _, value := range values {
			query.Add(name, value)
		}
		endpoint.RawQuery = query.Encode()
	}
	var body io.Reader
	if spec.Body != nil {
		body = spec.Body
	} else if (spec.JSONBody != nil || spec.HasJSONBody) && method != http.MethodGet {
		bytes, err := json.Marshal(spec.JSONBody)
		if err != nil {
			return nil, err
		}
		body = bytesReader(bytes)
		if spec.Headers == nil {
			spec.Headers = http.Header{}
		}
		if spec.Headers.Get("Content-Type") == "" {
			spec.Headers.Set("Content-Type", "application/json")
		}
	}
	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	for key, values := range spec.Headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if trim(c.token) != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json")
	}
	if c.bookmark != "" {
		request.Header.Set(d1BookmarkHeader, c.bookmark)
	}

	started := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, stageError("request", err, nil)
	}
	c.lastResponse = responseInfo(response)
	if spec.MetadataPath != "" {
		if err := writeResponseMetadata(spec.MetadataPath, c.lastResponse); err != nil {
			_ = response.Body.Close()
			return nil, stageError("local-persist", err, c.lastResponse)
		}
	}
	if bookmark := response.Header.Get(d1BookmarkHeader); bookmark != "" {
		c.bookmark = bookmark
	}
	c.logger.line("http", fmt.Sprintf(`{"durationMs":%d,"method":%q,"path":%q,"status":%d,"updatedBookmark":%t}`,
		time.Since(started).Milliseconds(), request.Method, endpoint.RequestURI(), response.StatusCode, response.Header.Get(d1BookmarkHeader) != ""))

	return response, nil
}

func (c *client) multipart(ctx context.Context, method string, path string, fields map[string]string, fileField string, filePath string, fileName string, contentType string, headers http.Header, cmd command) (any, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if value != "" {
			if err := writer.WriteField(key, value); err != nil {
				return nil, err
			}
		}
	}
	if filePath != "" {
		file, err := os.Open(filepath.Clean(filePath))
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		partHeader := make(textproto.MIMEHeader)
		partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(fileField), escapeQuotes(fileName)))
		partHeader.Set("Content-Type", contentType)
		part, err := writer.CreatePart(partHeader)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(part, file); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", writer.FormDataContentType())
	return c.jsonFromResponse(ctx, method, path, requestSpec{Body: bytesReader(body.Bytes()), Headers: headers, Query: rawQuery(cmd), MetadataPath: cmd.string("response-meta", "")})
}

func (c *client) jsonFromResponse(ctx context.Context, method string, path string, spec requestSpec) (any, error) {
	response, err := c.raw(ctx, method, path, spec)
	if err != nil {
		return nil, err
	}
	payload, err := readPayload(response, method, path)
	if err != nil {
		return nil, err
	}
	if payload.Parsed == nil && payload.Text != "" {
		var value any
		if err := json.Unmarshal([]byte(payload.Text), &value); err != nil {
			return nil, stageError("decode", err, payload.Evidence)
		}
		return value, nil
	}
	return payload.Parsed, nil
}

func (c *client) resolve(path string) (*url.URL, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	return base.Parse(path)
}

type requestSpec struct {
	Body         io.Reader
	Headers      http.Header
	JSONBody     any
	HasJSONBody  bool
	Query        url.Values
	MetadataPath string
}

func bytesReader(data []byte) io.Reader {
	return bytes.NewReader(data)
}

func escapeQuotes(value string) string {
	// Strip CR/LF as well as escaping backslash and quote: these values land in a
	// multipart Content-Disposition header line, so an unescaped CR/LF in a
	// filename or field name could inject header lines or split the part.
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "", "\n", "").Replace(value)
}

func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}
	if len(via) > 0 && (req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host) {
		return fmt.Errorf("refusing cross-origin redirect")
	}
	return nil
}
func writeResponseMetadata(path string, evidence *responseEvidence) error {
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Clean(path), append(data, '\n'), 0o600)
}
