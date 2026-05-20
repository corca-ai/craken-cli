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
	baseURL    string
	token      string
	httpClient *http.Client
	bookmark   string
	logger     *logger
}

func newClient(cmd command, log *logger) (*client, error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, err
	}
	prof := cfg.Profiles[profileName(cmd)]
	baseURL := cmd.string("base-url", "")
	if baseURL == "" {
		baseURL = prof.BaseURL
	}
	if baseURL == "" {
		baseURL = os.Getenv("CRAKEN_BASE_URL")
	}
	if trim(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	token := selectedBearerToken(cmd, prof)
	if trim(token) == "" {
		return nil, fmt.Errorf("bearer token is required. Run auth import-token, set CRAKEN_TOKEN, or pass --token/--bearer-token")
	}
	return &client{
		baseURL:    baseURL,
		token:      token,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		logger:     log,
	}, nil
}

func (c *client) json(ctx context.Context, method string, path string, body any) (any, error) {
	response, err := c.raw(ctx, method, path, requestSpec{JSONBody: body})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	var parsed any
	if len(text) > 0 {
		if err := json.Unmarshal(text, &parsed); err != nil {
			return nil, err
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s failed with %d: %s", method, path, response.StatusCode, string(text))
	}
	return parsed, nil
}

func (c *client) raw(ctx context.Context, method string, path string, spec requestSpec) (*http.Response, error) {
	endpoint, err := c.resolve(path)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if spec.Body != nil {
		body = spec.Body
	} else if spec.JSONBody != nil && method != http.MethodGet {
		bytes, err := json.Marshal(spec.JSONBody)
		if err != nil {
			return nil, err
		}
		body = bytesReader(bytes)
		if spec.Headers == nil {
			spec.Headers = http.Header{}
		}
		spec.Headers.Set("Content-Type", "application/json")
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
	request.Header.Set("Authorization", "Bearer "+c.token)
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json")
	}
	if c.bookmark != "" {
		request.Header.Set(d1BookmarkHeader, c.bookmark)
	}

	started := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if bookmark := response.Header.Get(d1BookmarkHeader); bookmark != "" {
		c.bookmark = bookmark
	}
	c.logger.line("http", fmt.Sprintf(`{"durationMs":%d,"method":%q,"path":%q,"status":%d,"updatedBookmark":%t}`,
		time.Since(started).Milliseconds(), request.Method, endpoint.RequestURI(), response.StatusCode, response.Header.Get(d1BookmarkHeader) != ""))

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		contentType := response.Header.Get("Content-Type")
		if !strings.Contains(contentType, "json") {
			defer func() { _ = response.Body.Close() }()
			text, _ := io.ReadAll(response.Body)
			return nil, fmt.Errorf("%s %s failed with %d: %s", method, path, response.StatusCode, string(text))
		}
	}
	return response, nil
}

func (c *client) multipart(ctx context.Context, method string, path string, fields map[string]string, fileField string, filePath string, fileName string, contentType string) (any, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if value != "" {
			if err := writer.WriteField(key, value); err != nil {
				return nil, err
			}
		}
	}
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
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return c.jsonFromResponse(ctx, method, path, requestSpec{
		Body: bytesReader(body.Bytes()),
		Headers: http.Header{
			"Content-Type": []string{writer.FormDataContentType()},
		},
	})
}

func (c *client) jsonFromResponse(ctx context.Context, method string, path string, spec requestSpec) (any, error) {
	response, err := c.raw(ctx, method, path, spec)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	var parsed any
	if len(text) > 0 {
		if err := json.Unmarshal(text, &parsed); err != nil {
			return nil, err
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s failed with %d: %s", method, path, response.StatusCode, string(text))
	}
	return parsed, nil
}

func (c *client) resolve(path string) (*url.URL, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	return base.Parse(path)
}

type requestSpec struct {
	Body     io.Reader
	Headers  http.Header
	JSONBody any
}

func bytesReader(data []byte) io.Reader {
	return bytes.NewReader(data)
}

func escapeQuotes(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(value)
}
