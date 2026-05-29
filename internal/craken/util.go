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
		// Drop only never-provided (nil) values; an explicit empty string is an
		// intentional value the user set (e.g. --name "" to clear a field) and
		// must round-trip into the request body.
		if value == nil {
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
	if cmd.Flags[name] {
		return true
	}
	// A bare flag (above) is true; an inline value is parsed so `--flag=false`
	// (which the parser stores in Options, not Flags) is honored as false.
	switch strings.ToLower(cmd.string(name, "")) {
	case "":
		return false
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
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
