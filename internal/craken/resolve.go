package craken

import (
	"context"
	"fmt"
	"strings"
)

func resolveWorkspaceID(ctx context.Context, client *client, value string) (string, error) {
	if looksLikeID(value) {
		return value, nil
	}
	parsed, err := client.json(ctx, "GET", "/api/workspaces", nil)
	if err != nil {
		return "", err
	}
	root, _ := parsed.(map[string]any)
	matches := filterObjects(root["workspaces"], func(item map[string]any) bool {
		return itemString(item, "id") == value || itemString(item, "name") == value
	})
	match, err := uniqueMatch(matches, value, "workspace")
	if err != nil {
		return "", err
	}
	return itemString(match, "id"), nil
}

func resolveChannelID(ctx context.Context, client *client, workspaceID string, value string) (string, error) {
	if looksLikeID(value) {
		return value, nil
	}
	parsed, err := client.json(ctx, "GET", workspacePath(workspaceID), nil)
	if err != nil {
		return "", err
	}
	detail, _ := parsed.(map[string]any)
	matches := filterObjects(detail["channels"], func(item map[string]any) bool {
		return itemString(item, "id") == value || itemString(item, "name") == value
	})
	match, err := uniqueMatch(matches, value, "channel")
	if err != nil {
		return "", err
	}
	return itemString(match, "id"), nil
}

func resolveParticipantID(ctx context.Context, client *client, workspaceID string, value string) (string, error) {
	if strings.HasPrefix(value, "user:") || strings.HasPrefix(value, "agent:") {
		return value, nil
	}
	lower := strings.ToLower(value)
	parsed, err := client.json(ctx, "GET", workspacePath(workspaceID), nil)
	if err != nil {
		return "", err
	}
	detail, _ := parsed.(map[string]any)
	matches := filterObjects(detail["members"], func(item map[string]any) bool {
		if itemString(item, "id") == value {
			return true
		}
		if itemString(item, "kind") == "user" {
			return strings.ToLower(itemString(item, "email")) == lower || strings.ToLower(itemString(item, "name")) == lower
		}
		return strings.ToLower(itemString(item, "name")) == lower
	})
	match, err := uniqueMatch(matches, value, "participant")
	if err != nil {
		return "", err
	}
	return itemString(match, "id"), nil
}

func filterObjects(value any, include func(map[string]any) bool) []map[string]any {
	items, _ := value.([]any)
	var matches []map[string]any
	for _, item := range items {
		object, ok := item.(map[string]any)
		if ok && include(object) {
			matches = append(matches, object)
		}
	}
	return matches
}

func uniqueMatch(matches []map[string]any, value string, label string) (map[string]any, error) {
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("could not resolve %s: %s", label, value)
	}
	return nil, fmt.Errorf("ambiguous %s: %s", label, value)
}

func itemString(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}
