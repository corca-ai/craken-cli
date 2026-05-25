package craken

import (
	"fmt"
	"io"
	"strings"
)

type commandOutputKind string

const (
	outputMessages     commandOutputKind = "messages"
	outputWikiRecent   commandOutputKind = "wiki-recent"
	outputWikiVersion  commandOutputKind = "wiki-version"
	outputWikiVersions commandOutputKind = "wiki-versions"
)

func printCommandOutput(stdout io.Writer, value any, cmd command, kind commandOutputKind) error {
	fields := cmd.string("fields", "")
	if boolOption(cmd, "compact") && fields != "" {
		return fmt.Errorf("use either --compact or --fields, not both")
	}
	if fields != "" {
		projected, err := projectFields(value, fields)
		if err != nil {
			return err
		}
		return printJSON(stdout, projected)
	}
	if !boolOption(cmd, "compact") {
		return printJSON(stdout, value)
	}

	switch kind {
	case outputMessages:
		return printCompactMessages(stdout, value)
	case outputWikiRecent:
		return printCompactWikiRows(stdout, collectionFromRoot(value, "changes"), true)
	case outputWikiVersions:
		return printCompactWikiRows(stdout, collectionFromRoot(value, "versions"), false)
	case outputWikiVersion:
		return printCompactWikiRows(stdout, []any{objectFromRoot(value, "version")}, false)
	default:
		return fmt.Errorf("--compact is not supported for this command")
	}
}

func projectFields(value any, fields string) (any, error) {
	var projected any = map[string]any{}
	for _, field := range strings.Split(fields, ",") {
		parts := fieldPathParts(field)
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty field in --fields")
		}
		partial, ok := projectFieldPath(value, parts)
		if !ok {
			continue
		}
		projected = mergeProjectedValue(projected, partial)
	}
	return projected, nil
}

func fieldPathParts(field string) []string {
	raw := strings.Split(field, ".")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func projectFieldPath(value any, parts []string) (any, bool) {
	if len(parts) == 0 {
		return value, true
	}
	switch typed := value.(type) {
	case map[string]any:
		child, ok := typed[parts[0]]
		if !ok {
			return nil, false
		}
		projected, ok := projectFieldPath(child, parts[1:])
		if !ok {
			return nil, false
		}
		return map[string]any{parts[0]: projected}, true
	case []any:
		out := make([]any, len(typed))
		anyProjected := false
		for index, item := range typed {
			projected, ok := projectFieldPath(item, parts)
			if ok {
				out[index] = projected
				anyProjected = true
			} else {
				out[index] = map[string]any{}
			}
		}
		return out, anyProjected
	default:
		return nil, false
	}
}

func mergeProjectedValue(left any, right any) any {
	leftMap, leftIsMap := left.(map[string]any)
	rightMap, rightIsMap := right.(map[string]any)
	if leftIsMap && rightIsMap {
		for key, rightValue := range rightMap {
			leftMap[key] = mergeProjectedValue(leftMap[key], rightValue)
		}
		return leftMap
	}

	leftSlice, leftIsSlice := left.([]any)
	rightSlice, rightIsSlice := right.([]any)
	if leftIsSlice && rightIsSlice {
		merged := make([]any, len(rightSlice))
		copy(merged, rightSlice)
		for index, leftValue := range leftSlice {
			if index >= len(merged) {
				merged = append(merged, leftValue)
				continue
			}
			merged[index] = mergeProjectedValue(leftValue, merged[index])
		}
		return merged
	}

	if left == nil {
		return right
	}
	return right
}

func printCompactMessages(stdout io.Writer, value any) error {
	for _, raw := range collectionFromRoot(value, "messages") {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		if _, err := fmt.Fprintf(
			stdout,
			"%s\t%s\t%s\n",
			compactCell(stringValue(message["createdAt"])),
			compactCell(senderLabel(message["sender"])),
			compactCell(stringValue(message["body"])),
		); err != nil {
			return err
		}
	}
	return nil
}

func printCompactWikiRows(stdout io.Writer, rows []any, includePage bool) error {
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		cells := []string{
			compactCell(stringValue(row["createdAt"])),
			compactCell(senderLabel(row["createdBy"])),
		}
		if includePage {
			page, _ := row["page"].(map[string]any)
			cells = append(cells, compactCell(stringValue(page["title"])))
		}
		cells = append(cells, compactCell(compactScalar(row["versionNumber"])))
		if _, err := fmt.Fprintln(stdout, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return nil
}

func collectionFromRoot(value any, key string) []any {
	root, _ := value.(map[string]any)
	items, _ := root[key].([]any)
	return items
}

func objectFromRoot(value any, key string) any {
	root, _ := value.(map[string]any)
	return root[key]
}

func senderLabel(value any) string {
	sender, _ := value.(map[string]any)
	if sender == nil {
		return ""
	}
	for _, key := range []string{"name", "email", "id"} {
		if text := stringValue(sender[key]); text != "" {
			return text
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func compactScalar(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func compactCell(value string) string {
	return strings.NewReplacer("\t", `\t`, "\r", `\r`, "\n", `\n`).Replace(value)
}
