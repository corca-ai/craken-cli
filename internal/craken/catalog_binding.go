package craken

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func catalogValues(
	ctx context.Context,
	client *client,
	routes []route,
	cmd command,
	bindings map[string]commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (map[string]any, error) {
	values := map[string]any{}
	for name, binding := range bindings {
		value, ok, err := catalogBindingValue(ctx, client, routes, cmd, binding, resolved, consumed)
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
	routes []route,
	cmd command,
	binding commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (string, error) {
	value, ok, err := catalogBindingValue(ctx, client, routes, cmd, binding, resolved, consumed)
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
	routes []route,
	cmd command,
	binding commandBinding,
	resolved map[string]string,
	consumed map[string]bool,
) (any, bool, error) {
	switch binding.Source {
	case commandBindingSourceBearerToken:
		return client.token, client.token != "", nil
	case commandBindingSourceLiteral:
		return binding.Value, true, nil
	case commandBindingSourceResolved:
		value := resolved[binding.Name]
		if value == "" {
			if binding.Required {
				return nil, false, fmt.Errorf("expected resolved value %s", binding.Name)
			}
			return nil, false, nil
		}
		return value, true, nil
	case commandBindingSourceFlag:
		if binding.Option == "" {
			return nil, false, nil
		}
		consumed[binding.Option] = true
		return boolOption(cmd, binding.Option), true, nil
	case commandBindingSourceText:
		value, ok, err := textBindingValue(cmd, binding)
		if err != nil || !ok {
			// Only report the binding as missing when it is genuinely absent
			// (err == nil); otherwise surface the real error (e.g. the
			// value/file mutual-exclusion message) instead of masking it.
			if err == nil && binding.Required && !ok {
				return nil, false, fmt.Errorf("expected --%s", binding.Option)
			}
			return value, ok, err
		}
		resolvedValue, err := resolveBoundValue(ctx, client, routes, binding, fmt.Sprint(value), resolved)
		return resolvedValue, true, err
	case commandBindingSourceOption, "":
		value, source, ok := bindingOptionValue(cmd, binding)
		if !ok {
			if binding.Default != nil {
				return binding.Default, true, nil
			}
			if binding.Required {
				return nil, false, fmt.Errorf("expected --%s", binding.Option)
			}
			return nil, false, nil
		}
		consumed[source] = true
		if binding.Type == commandBindingValueTypeJSON {
			parsed, err := jsonBindingValue(value, source)
			return parsed, true, err
		}
		if binding.Type == commandBindingValueTypeInteger {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return nil, false, fmt.Errorf("expected integer for --%s, got %s", source, value)
			}
			return parsed, true, nil
		}
		resolvedValue, err := resolveBoundValue(ctx, client, routes, binding, value, resolved)
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
	if binding.Positionals == commandBindingPositionalsJoin && len(cmd.Positionals) > 0 {
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
	routes []route,
	binding commandBinding,
	value string,
	resolved map[string]string,
) (string, error) {
	if binding.Resolver == nil {
		return value, nil
	}
	return resolveCatalogValue(ctx, client, routes, *binding.Resolver, value, resolved)
}

func resolveCatalogValue(
	ctx context.Context,
	client *client,
	routes []route,
	plan commandResolverPlan,
	value string,
	resolved map[string]string,
) (string, error) {
	route := routeByID(routes, plan.OperationID)
	if route == nil {
		return "", fmt.Errorf("unknown resolver operation: %s", plan.OperationID)
	}
	path, err := catalogResolverPath(ctx, client, routes, *route, plan.PathParams, resolved)
	if err != nil {
		return "", err
	}
	root, err := client.json(ctx, path)
	if err != nil {
		return "", err
	}
	items, ok := valueAtPath(root, plan.CollectionPath).([]any)
	if !ok {
		return "", fmt.Errorf("resolver %s expected array at %s", planLabel(plan), plan.CollectionPath)
	}
	var matched any
	for _, item := range items {
		if matchesCatalogResolverItem(item, plan.MatchFields, value) {
			if matched != nil {
				return "", fmt.Errorf("multiple %s matches for %s", planLabel(plan), value)
			}
			matched = item
		}
	}
	if matched == nil {
		return "", fmt.Errorf("unknown %s: %s", planLabel(plan), value)
	}
	result := valueString(valueAtPath(matched, plan.ResultPath))
	if plan.RequiredResultPrefix != "" && !strings.HasPrefix(result, plan.RequiredResultPrefix) {
		return "", fmt.Errorf("%s must resolve to %s value, got %s", planLabel(plan), plan.RequiredResultPrefix, result)
	}
	if plan.TrimResultPrefix != "" {
		result = strings.TrimPrefix(result, plan.TrimResultPrefix)
	}
	if result == "" {
		return "", fmt.Errorf("resolver %s returned empty %s", planLabel(plan), plan.ResultPath)
	}
	return result, nil
}

func catalogResolverPath(
	ctx context.Context,
	client *client,
	routes []route,
	route route,
	pathParams map[string]commandBinding,
	resolved map[string]string,
) (string, error) {
	path := pathParamPattern.ReplaceAllStringFunc(route.Path, func(match string) string {
		name := match[1 : len(match)-1]
		binding := pathParams[name]
		if binding.Source == "" {
			return "\x00missing:" + name
		}
		value, err := catalogBindingString(ctx, client, routes, command{}, binding, resolved, map[string]bool{})
		if err != nil {
			return "\x00error:" + err.Error()
		}
		if value == "" {
			return "\x00missing:" + name
		}
		return url.PathEscape(value)
	})
	if strings.Contains(path, "\x00error:") {
		return "", fmt.Errorf("%s", strings.TrimPrefix(path[strings.Index(path, "\x00error:"):], "\x00error:"))
	}
	if strings.Contains(path, "\x00missing:") {
		name := strings.TrimPrefix(path[strings.Index(path, "\x00missing:"):], "\x00missing:")
		return "", fmt.Errorf("resolver %s requires path binding %s", route.ID, name)
	}
	return path, nil
}

func matchesCatalogResolverItem(item any, fields []string, value string) bool {
	for _, field := range fields {
		itemValue := valueString(valueAtPath(item, field))
		if strings.EqualFold(itemValue, value) {
			return true
		}
	}
	return false
}

func valueAtPath(value any, path string) any {
	if path == "" {
		return value
	}
	current := value
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[part]
	}
	return current
}

func valueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	case float64:
		// JSON numbers decode to float64; render in plain decimal so large
		// integers don't come out in scientific notation (e.g. 1234567, not
		// "1.234567e+06").
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

func planLabel(plan commandResolverPlan) string {
	if plan.Label != "" {
		return plan.Label
	}
	return plan.OperationID
}
