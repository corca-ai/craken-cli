package craken

import (
	"fmt"
	"io"
	"strings"
)

func printCommandOutput(stdout io.Writer, value any, cmd command, plan commandOutputPlan) error {
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
	if plan.Mode != commandOutputModeTable {
		return fmt.Errorf("--compact is not supported for this command")
	}
	return printCompactTable(stdout, value, plan)
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

func printCompactTable(stdout io.Writer, value any, plan commandOutputPlan) error {
	for _, row := range outputRows(value, plan.RowsPath) {
		cells := make([]string, 0, len(plan.Columns))
		for _, column := range plan.Columns {
			cells = append(cells, compactCell(firstColumnValue(row, column.Paths)))
		}
		if _, err := fmt.Fprintln(stdout, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return nil
}

func outputRows(value any, rowsPath string) []any {
	rows := valueAtPath(value, rowsPath)
	if items, ok := rows.([]any); ok {
		return items
	}
	if rows == nil {
		return nil
	}
	return []any{rows}
}

func firstColumnValue(row any, paths []string) string {
	for _, path := range paths {
		if text := compactScalar(valueAtPath(row, path)); text != "" {
			return text
		}
	}
	return ""
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
