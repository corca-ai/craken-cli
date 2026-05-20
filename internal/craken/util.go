package craken

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var idPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f-]{27,}$`)

func trim(value string) string {
	return strings.TrimSpace(value)
}

func joinNames(names []string) string {
	return strings.Join(names, " or --")
}

func first(values []string, fallback string) string {
	if len(values) > 0 {
		return values[0]
	}
	return fallback
}

func rest(values []string) []string {
	if len(values) <= 1 {
		return nil
	}
	return values[1:]
}

func compact(body map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range body {
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func printJSON(stdout interface{ Write([]byte) (int, error) }, value any) error {
	bytes, err := json.MarshalIndent(value, "", "\t")
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(bytes, '\n'))
	return err
}

func readTextOption(cmd command, valueName string, fileName string) (string, error) {
	value := cmd.string(valueName, "")
	file := cmd.string(fileName, "")
	if value != "" && file != "" {
		return "", fmt.Errorf("use either --%s or --%s, not both", valueName, fileName)
	}
	if file != "" {
		bytes, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return "", err
		}
		return string(bytes), nil
	}
	if value != "" {
		return value, nil
	}
	return "", fmt.Errorf("expected --%s", valueName)
}

func jsonOption(cmd command, jsonName string, fileName string, required bool) (any, error) {
	value := cmd.string(jsonName, "")
	file := cmd.string(fileName, "")
	if value != "" && file != "" {
		return nil, fmt.Errorf("use either --%s or --%s, not both", jsonName, fileName)
	}
	if value != "" {
		var parsed any
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			return nil, err
		}
		return parsed, nil
	}
	if file != "" {
		bytes, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return nil, err
		}
		var parsed any
		if err := json.Unmarshal(bytes, &parsed); err != nil {
			return nil, err
		}
		return parsed, nil
	}
	if required {
		return nil, fmt.Errorf("expected --%s or --%s", jsonName, fileName)
	}
	return nil, nil
}

func numberOption(cmd command, name string, fallback int) (int, error) {
	value := cmd.string(name, "")
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("expected non-negative integer, got %s", value)
	}
	return parsed, nil
}

func boolOption(cmd command, name string) bool {
	return cmd.Flags[name] || cmd.string(name, "") != ""
}

func stringListOption(cmd command, name string) []string {
	value := cmd.string(name, "")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = trim(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func looksLikeID(value string) bool {
	return idPattern.MatchString(value)
}

func workspacePath(workspaceID string) string {
	return "/api/workspaces/" + url.PathEscape(workspaceID)
}

func wikiPagePath(workspaceID string, title string) string {
	return workspacePath(workspaceID) + "/wiki/pages/" + url.PathEscape(title)
}

func bearerProtocols(token string) []string {
	payload, _ := json.Marshal(map[string]string{"token": token})
	return []string{
		"craken-bearer",
		"craken-bearer-payload." + base64.RawURLEncoding.EncodeToString(payload),
	}
}
