package craken

import (
	"encoding/json"
	"fmt"
	"net/http"
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
func convertBoundWire(values map[string]any, schema wireSchema, bindings map[string]commandBinding) (map[string]any, error) {
	for name, value := range values {
		field, exists := schema.Properties[name]
		binding := bindings[name]
		if !exists || binding.Type == commandBindingValueTypeJSON || binding.Type == "string" || binding.Source == commandBindingSourceText || (binding.Type == "" && schemaType(field) == "") {
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
	if err := validateRequiredInputs(cmd, selected, plan, generic); err != nil {
		return err
	}
	if err := validatePlan(plan, cmd); err != nil {
		return err
	}
	if plan.Transport == commandTransportWebSocket {
		if _, err := newStreamOutput(cmd, plan.WebSocket.Stream); err != nil {
			return err
		}
	}
	known := map[string]bool{}
	for name, value := range localOptions {
		known[name] = value
	}
	bindings := []map[string]commandBinding{plan.PathParams, plan.QueryParams, plan.BodyFields, plan.Multipart.Fields}
	positionalText := false
	for index, group := range bindings {
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
			if binding.Required && binding.Source != commandBindingSourceResolved && !(index == 2 && hasExplicitJSON(cmd)) {
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
		for _, name := range []string{"messages", "reconnect", "resume", "once", "limit", "timeout-ms", "wait-timeout-ms", "max-retries", "retry-delay-ms"} {
			known[name] = true
		}
	}
	if selected.Specification != nil {
		schemas := []wireSchema{selected.Specification.Wire.Headers}
		if plan.QueryParams == nil {
			schemas = append(schemas, selected.Specification.Wire.Query)
		}
		if plan.BodyFields == nil {
			schemas = append(schemas, selected.Specification.Wire.Body)
		}
		for _, schema := range schemas {
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
		if len(values) > 1 && name != "header" && name != "query" && !arrayOption(selected, plan, name) {
			return fmt.Errorf("repeated scalar option --%s; encode arrays as explicit JSON", name)
		}
	}
	if hasExplicitJSON(cmd) && selected.RequestBody == "none" {
		return fmt.Errorf("operation %s does not accept a JSON body", selected.ID)
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

func arrayOption(selected route, plan commandExecution, option string) bool {
	if selected.Specification == nil {
		return false
	}
	for index, schema := range []wireSchema{selected.Specification.Wire.Query, selected.Specification.Wire.Body} {
		for name, field := range schema.Properties {
			if schemaType(field) != "array" {
				continue
			}
			names := optionAliases(name)
			bindings := plan.QueryParams
			if index == 1 {
				bindings = plan.BodyFields
			}
			if binding, exists := bindings[name]; exists {
				names = append(names, bindingOptionNames(binding)...)
			}
			for _, name := range names {
				if name == option {
					return true
				}
			}
		}
	}
	return false
}
func normalizeArrayOptions(cmd command, selected route, plan commandExecution) (command, error) {
	if selected.Specification == nil {
		return cmd, nil
	}
	for index, schema := range []wireSchema{selected.Specification.Wire.Query, selected.Specification.Wire.Body} {
		for name, field := range schema.Properties {
			if schemaType(field) != "array" {
				continue
			}
			names := optionAliases(name)
			bindings := plan.QueryParams
			if index == 1 {
				bindings = plan.BodyFields
			}
			if binding, exists := bindings[name]; exists {
				names = append(names, bindingOptionNames(binding)...)
			}
			for _, option := range names {
				values := cmd.Values[option]
				if len(values) < 2 {
					continue
				}
				items := []any{}
				for _, input := range values {
					var value any = input
					if field.Items == nil || schemaType(*field.Items) != "string" {
						parsed, err := jsonValue(input)
						if err != nil {
							return cmd, fmt.Errorf("--%s requires JSON array input", option)
						}
						value = parsed
					}
					if array, ok := value.([]any); ok {
						items = append(items, array...)
					} else {
						items = append(items, value)
					}
				}
				encoded, err := json.Marshal(items)
				if err != nil {
					return cmd, err
				}
				cmd.Options[option] = string(encoded)
				delete(cmd.Flags, option)
			}
		}
	}
	return cmd, nil
}
func wireHeaders(headers http.Header, cmd command, selected route) {
	if selected.Specification == nil {
		return
	}
	for name := range selected.Specification.Wire.Headers.Properties {
		if value, ok := optionValue(cmd, name); ok {
			headers.Set(name, value)
		}
	}
}

func validateRequiredInputs(cmd command, selected route, plan commandExecution, generic bool) error {
	if generic {
		positionals := len(cmd.Positionals)
		for _, match := range pathParamPattern.FindAllStringSubmatch(selected.Path, -1) {
			if _, exists := optionValue(cmd, match[1]); !exists {
				if positionals == 0 {
					return fmt.Errorf("expected --%s", kebabCase(match[1]))
				}
				positionals--
			}
		}
	}
	for index, bindings := range []map[string]commandBinding{plan.PathParams, plan.QueryParams, plan.BodyFields, plan.Multipart.Fields} {
		for fieldName, binding := range bindings {
			boolean := binding.Source == commandBindingSourceFlag
			if selected.Specification != nil && index < 3 {
				schemas := []wireSchema{selected.Specification.Wire.Path, selected.Specification.Wire.Query, selected.Specification.Wire.Body}
				boolean = boolean || schemaType(schemas[index].Properties[fieldName]) == "boolean"
			}
			for _, name := range bindingOptionNames(binding) {
				if cmd.Flags[name] && !boolean {
					return fmt.Errorf("expected value for --%s", name)
				}
			}
			if binding.FileOption != "" && cmd.Flags[binding.FileOption] {
				return fmt.Errorf("expected value for --%s", binding.FileOption)
			}
			if binding.Source == commandBindingSourceText {
				_, _, inline := bindingOptionValue(cmd, commandBinding{Option: binding.Option})
				_, _, file := bindingOptionValue(cmd, commandBinding{Option: binding.FileOption})
				if inline && file {
					return fmt.Errorf("use either --%s or --%s, not both", binding.Option, binding.FileOption)
				}
			}
			count := 0
			for _, name := range bindingOptionNames(binding) {
				if _, exists := optionValueExact(cmd, name); exists {
					count++
				}
			}
			if count > 1 {
				return fmt.Errorf("conflicting aliases for --%s", binding.Option)
			}
		}
	}
	if plan.Transport == commandTransportMultipart && !plan.Multipart.FileOptional {
		if cmd.string(plan.Multipart.FileOption, "") == "" {
			return fmt.Errorf("expected --%s", plan.Multipart.FileOption)
		}
	}
	if selected.Specification == nil {
		return nil
	}
	for _, schema := range []wireSchema{selected.Specification.Wire.Path, selected.Specification.Wire.Query, selected.Specification.Wire.Body, selected.Specification.Wire.Headers} {
		for name, field := range schema.Properties {
			count := 0
			for _, alias := range optionAliases(name) {
				if _, exists := optionValueExact(cmd, alias); exists {
					count++
				}
				if cmd.Flags[alias] && schemaType(field) != "boolean" {
					return fmt.Errorf("expected value for --%s", alias)
				}
			}
			if count > 1 {
				return fmt.Errorf("conflicting aliases for --%s", kebabCase(name))
			}
		}
	}
	for index, schema := range []wireSchema{selected.Specification.Wire.Query, selected.Specification.Wire.Body} {
		bindings := plan.QueryParams
		if index == 1 {
			bindings = plan.BodyFields
			if hasExplicitJSON(cmd) {
				continue
			}
		}
		for _, name := range schema.Required {
			if _, bound := bindings[name]; bound {
				continue
			}
			if _, exists := optionValue(cmd, name); !exists {
				return fmt.Errorf("expected --%s", kebabCase(name))
			}
		}
	}
	return nil
}

func optionValueExact(cmd command, name string) (string, bool) {
	if value, exists := cmd.Options[name]; exists {
		return value, true
	}
	if value, exists := cmd.Flags[name]; exists && value {
		return "true", true
	}
	return "", false
}
