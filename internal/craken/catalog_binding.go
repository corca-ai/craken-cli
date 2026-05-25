package craken

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func catalogValues(
	ctx context.Context,
	client *client,
	cmd command,
	bindings map[string]commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (map[string]any, error) {
	values := map[string]any{}
	for name, binding := range bindings {
		value, ok, err := catalogBindingValue(ctx, client, cmd, binding, resolved, consumed)
		if err != nil {
			return nil, err
		}
		if ok {
			values[name] = value
		}
	}
	return values, nil
}

func catalogBindingString(
	ctx context.Context,
	client *client,
	cmd command,
	binding commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (string, error) {
	value, ok, err := catalogBindingValue(ctx, client, cmd, binding, resolved, consumed)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(value), nil
	}
	return text, nil
}

func catalogBindingValue(
	ctx context.Context,
	client *client,
	cmd command,
	binding commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (any, bool, error) {
	switch binding.Source {
	case "literal":
		return binding.Value, true, nil
	case "flag":
		if binding.Option == "" {
			return nil, false, nil
		}
		consumed[binding.Option] = true
		return boolOption(cmd, binding.Option), true, nil
	case "text":
		value, ok, err := textBindingValue(cmd, binding)
		if err != nil || !ok {
			if binding.Required && !ok {
				return nil, false, fmt.Errorf("expected --%s", binding.Option)
			}
			return value, ok, err
		}
		resolvedValue, err := resolveBoundValue(ctx, client, binding, fmt.Sprint(value), resolved)
		return resolvedValue, true, err
	case "option", "":
		value, source, ok := bindingOptionValue(cmd, binding)
		if !ok {
			if binding.Required {
				return nil, false, fmt.Errorf("expected --%s", binding.Option)
			}
			return nil, false, nil
		}
		consumed[source] = true
		if binding.Type == "json" {
			parsed, err := jsonBindingValue(value, source)
			return parsed, true, err
		}
		if binding.Type == "integer" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return nil, false, fmt.Errorf("expected integer for --%s, got %s", source, value)
			}
			return parsed, true, nil
		}
		resolvedValue, err := resolveBoundValue(ctx, client, binding, value, resolved)
		return resolvedValue, true, err
	default:
		return nil, false, fmt.Errorf("unsupported catalog binding source: %s", binding.Source)
	}
}

func bindingOptionValue(cmd command, binding commandBinding) (string, string, bool) {
	for _, name := range bindingOptionNames(binding) {
		if value, ok := cmd.Options[name]; ok {
			return value, name, true
		}
		if value, ok := cmd.Flags[name]; ok && value {
			return "true", name, true
		}
	}
	return "", "", false
}

func bindingOptionNames(binding commandBinding) []string {
	names := []string{}
	if binding.Option != "" {
		names = append(names, binding.Option)
	}
	names = append(names, binding.Aliases...)
	return names
}

func textBindingValue(cmd command, binding commandBinding) (string, bool, error) {
	value, valueSource, hasValue := bindingOptionValue(cmd, commandBinding{Option: binding.Option})
	file, fileSource, hasFile := bindingOptionValue(cmd, commandBinding{Option: binding.FileOption})
	if hasValue && hasFile {
		return "", false, fmt.Errorf("use either --%s or --%s, not both", valueSource, fileSource)
	}
	if hasFile {
		bytes, err := os.ReadFile(filepath.Clean(file))
		return string(bytes), true, err
	}
	if hasValue {
		return value, true, nil
	}
	if binding.Positionals == "join" && len(cmd.Positionals) > 0 {
		return strings.Join(cmd.Positionals, " "), true, nil
	}
	return "", false, nil
}

func jsonBindingValue(value string, source string) (any, error) {
	if strings.HasSuffix(source, "-file") {
		bytes, err := os.ReadFile(filepath.Clean(value))
		if err != nil {
			return nil, err
		}
		value = string(bytes)
	}
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func resolveBoundValue(
	ctx context.Context,
	client *client,
	binding commandBinding,
	value string,
	resolved map[string]string,
) (string, error) {
	switch binding.Resolver {
	case "":
		return value, nil
	case "workspace":
		return resolveWorkspaceID(ctx, client, value)
	case "channel":
		workspaceID := resolved[binding.Scope]
		if workspaceID == "" {
			return "", fmt.Errorf("resolver channel requires scope %s", binding.Scope)
		}
		return resolveChannelID(ctx, client, workspaceID, value)
	case "participant", "agent":
		workspaceID := resolved[binding.Scope]
		if workspaceID == "" {
			return "", fmt.Errorf("resolver %s requires scope %s", binding.Resolver, binding.Scope)
		}
		participantID, err := resolveParticipantID(ctx, client, workspaceID, value)
		if err != nil {
			return "", err
		}
		if binding.Resolver == "agent" {
			if !strings.HasPrefix(participantID, "agent:") {
				return "", fmt.Errorf("dream agent must resolve to an agent participant, got %s", participantID)
			}
			return strings.TrimPrefix(participantID, "agent:"), nil
		}
		return participantID, nil
	default:
		return "", fmt.Errorf("unsupported catalog resolver: %s", binding.Resolver)
	}
}
