package craken

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

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
