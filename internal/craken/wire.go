package craken

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type wireField struct {
	Type  any         `json:"type"`
	AnyOf []wireField `json:"anyOf"`
	OneOf []wireField `json:"oneOf"`
	Items *wireField  `json:"items"`
}
type wireSchema struct {
	Properties map[string]wireField `json:"properties"`
	Required   []string             `json:"required"`
}
type operationSpecification struct {
	Wire struct {
		Path          wireSchema        `json:"path"`
		Query         wireSchema        `json:"query"`
		Body          wireSchema        `json:"body"`
		Headers       wireSchema        `json:"headers"`
		QueryEncoding map[string]string `json:"queryEncoding"`
	} `json:"wire"`
}

func schemaType(field wireField) string { value, _ := field.Type.(string); return value }

func wireValue(value string, field wireField) (any, error) {
	switch schemaType(field) {
	case "string":
		return value, nil
	case "boolean":
		if value != "true" && value != "false" {
			return nil, fmt.Errorf("expected true or false")
		}
		return value == "true", nil
	case "integer":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != value {
			return nil, fmt.Errorf("expected canonical integer")
		}
		return n, nil
	case "number":
		parsed, err := jsonValue(value)
		if err != nil {
			return nil, fmt.Errorf("expected JSON number")
		}
		if _, ok := parsed.(float64); !ok {
			return nil, fmt.Errorf("expected JSON number")
		}
		return parsed, nil
	default:
		parsed, err := jsonValue(value)
		if err != nil {
			return nil, fmt.Errorf("complex or ambiguous input requires explicit JSON: %w", err)
		}
		return parsed, nil
	}
}
func wireOptions(cmd command, schema wireSchema) (map[string]any, error) {
	out := map[string]any{}
	for name, field := range schema.Properties {
		value, ok := optionValue(cmd, name)
		if !ok || localOptions[name] || localOptions[kebabCase(name)] {
			continue
		}
		parsed, err := wireValue(value, field)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", kebabCase(name), err)
		}
		out[name] = parsed
	}
	return out, nil
}
func convertBoundWire(values map[string]any, schema wireSchema) (map[string]any, error) {
	for name, value := range values {
		field, exists := schema.Properties[name]
		if !exists {
			continue
		}
		if text, ok := value.(string); ok {
			converted, err := wireValue(text, field)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			values[name] = converted
		}
	}
	return values, nil
}
func appendWireQuery(path string, values map[string]any, selected route) (string, error) {
	encodings := map[string]string{}
	if selected.Specification != nil {
		encodings = selected.Specification.Wire.QueryEncoding
	}
	params := url.Values{}
	for name, value := range values {
		encoding := encodings[name]
		switch encoding {
		case "", "json":
			params.Add(name, queryParamString(value))
		case "comma", "repeat":
			items, ok := value.([]any)
			if !ok {
				return "", fmt.Errorf("query %s requires an array for %s encoding", name, encoding)
			}
			texts := []string{}
			for _, item := range items {
				text, ok := item.(string)
				if !ok {
					return "", fmt.Errorf("query %s requires string items", name)
				}
				texts = append(texts, text)
			}
			if encoding == "comma" {
				params.Add(name, strings.Join(texts, ","))
			} else {
				for _, text := range texts {
					params.Add(name, text)
				}
			}
		default:
			return "", fmt.Errorf("unsupported query encoding: %s", encoding)
		}
	}
	return appendQueryValues(path, params), nil
}
func appendQueryValues(path string, params url.Values) string {
	if len(params) == 0 {
		return path
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + params.Encode()
}
func rawQuery(cmd command) url.Values {
	out := url.Values{}
	for _, input := range cmd.Values["query"] {
		name, value, ok := strings.Cut(input, "=")
		if ok {
			out.Add(name, value)
		}
	}
	return out
}

func validateCatalogInputs(cmd command, selected route, plan commandExecution, generic bool) error {
	if err := validatePlan(plan, cmd); err != nil {
		return err
	}
	known := map[string]bool{}
	for name, value := range localOptions {
		known[name] = value
	}
	bindings := []map[string]commandBinding{plan.PathParams, plan.QueryParams, plan.BodyFields, plan.Multipart.Fields}
	positionalText := false
	for _, group := range bindings {
		for _, binding := range group {
			for _, name := range bindingOptionNames(binding) {
				known[name] = true
			}
			if binding.FileOption != "" {
				known[binding.FileOption] = true
			}
			if binding.Positionals == commandBindingPositionalsJoin {
				positionalText = true
			}
			if binding.Required && binding.Source != commandBindingSourceResolved && !hasExplicitJSON(cmd) {
				_, _, exists := bindingOptionValue(cmd, binding)
				_, hasFile := cmd.Options[binding.FileOption]
				if !exists && !hasFile && binding.Default == nil && !(binding.Positionals == commandBindingPositionalsJoin && len(cmd.Positionals) > 0) {
					return fmt.Errorf("expected --%s", binding.Option)
				}
			}
		}
	}
	for _, name := range []string{plan.Multipart.FileOption, plan.Multipart.FileNameOption, plan.Multipart.ContentTypeOption} {
		if name != "" {
			known[name] = true
		}
	}
	if plan.Poll != nil {
		known[plan.Poll.IntervalOption] = true
		known[plan.Poll.MaxPollsOption] = true
	}
	if plan.Transport == commandTransportWebSocket {
		for _, name := range []string{"messages", "reconnect", "resume", "checkpoint", "limit", "event-types", "sender-kind", "conversation-kind", "channel-id", "channel-ids", "mentions-me", "include-dm", "after", "reconnect-delay", "max-reconnects"} {
			known[name] = true
		}
	}
	if selected.Specification != nil {
		for _, schema := range []wireSchema{selected.Specification.Wire.Query, selected.Specification.Wire.Body, selected.Specification.Wire.Headers} {
			for name := range schema.Properties {
				known[name] = true
				known[kebabCase(name)] = true
			}
		}
	}
	if generic {
		for _, match := range pathParamPattern.FindAllStringSubmatch(selected.Path, -1) {
			known[match[1]] = true
			known[kebabCase(match[1])] = true
		}
	}
	strict := selected.Specification != nil || plan.QueryParams != nil || plan.BodyFields != nil || plan.Transport == commandTransportMultipart
	if strict {
		for name := range cmd.Options {
			if !known[name] {
				return fmt.Errorf("unknown option --%s", name)
			}
		}
		for name := range cmd.Flags {
			if !known[name] {
				return fmt.Errorf("unknown option --%s", name)
			}
		}
	}
	if !generic && !positionalText && len(cmd.Positionals) > 0 {
		return fmt.Errorf("unexpected positional argument: %s", cmd.Positionals[0])
	}
	for name, values := range cmd.Values {
		if len(values) > 1 && name != "header" && name != "query" {
			return fmt.Errorf("repeated scalar option --%s; encode arrays as explicit JSON", name)
		}
	}
	if hasExplicitJSON(cmd) {
		for _, binding := range plan.BodyFields {
			for _, name := range append(bindingOptionNames(binding), binding.FileOption) {
				if _, ok := optionValue(cmd, name); ok {
					return fmt.Errorf("--json conflicts with --%s", name)
				}
			}
		}
		if selected.Specification != nil {
			for name := range selected.Specification.Wire.Body.Properties {
				if _, ok := optionValue(cmd, name); ok && !localOptions[kebabCase(name)] {
					return fmt.Errorf("--json conflicts with --%s", kebabCase(name))
				}
			}
		}
	}
	return nil
}
