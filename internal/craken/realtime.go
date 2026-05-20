package craken

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var websocketDialer = websocket.DefaultDialer

func tailWorkspace(ctx context.Context, client *client, workspaceID string, cmd command, stdout io.Writer) error {
	endpoint, err := client.resolve(workspacePath(workspaceID) + "/realtime")
	if err != nil {
		return err
	}
	if after := cmd.string("after", ""); after != "" {
		params := endpoint.Query()
		params.Set("after", after)
		endpoint.RawQuery = params.Encode()
	}
	switch endpoint.Scheme {
	case "https":
		endpoint.Scheme = "wss"
	case "http":
		endpoint.Scheme = "ws"
	}
	limit, err := numberOption(cmd, "limit", 0)
	if err != nil {
		return err
	}
	timeoutMS, err := numberOption(cmd, "timeout-ms", 0)
	if err != nil {
		return err
	}
	dialer := *websocketDialer
	dialer.Subprotocols = bearerProtocols(client.token)
	headers := http.Header{}
	connection, _, err := dialer.DialContext(ctx, endpoint.String(), headers)
	if err != nil {
		return fmt.Errorf("websocket subscription failed for %s: %w", endpoint.String(), err)
	}
	defer func() { _ = connection.Close() }()
	client.logger.line("cli", fmt.Sprintf("subscribed workspace=%s", workspaceID))
	if timeoutMS > 0 {
		deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
		_ = connection.SetReadDeadline(deadline)
	}
	seen := 0
	for {
		_, message, err := connection.ReadMessage()
		if err != nil {
			if timeoutMS > 0 && strings.Contains(strings.ToLower(err.Error()), "timeout") {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return nil
			}
			return err
		}
		items, err := realtimeItems(message)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := printRealtimeItem(stdout, item, cmd); err != nil {
				return err
			}
			seen++
			if limit > 0 && seen >= limit {
				_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "limit"), time.Now().Add(time.Second))
				return nil
			}
		}
	}
}

func realtimeItems(message []byte) ([]map[string]any, error) {
	var parsed map[string]any
	if err := json.Unmarshal(message, &parsed); err != nil {
		return nil, err
	}
	if activities, ok := parsed["activities"].([]any); ok {
		items := make([]map[string]any, 0, len(activities))
		for _, item := range activities {
			object, _ := item.(map[string]any)
			items = append(items, object)
		}
		return items, nil
	}
	if activity, ok := parsed["activity"].(map[string]any); ok {
		return []map[string]any{activity}, nil
	}
	return []map[string]any{parsed}, nil
}

func printRealtimeItem(stdout io.Writer, item map[string]any, cmd command) error {
	if !boolOption(cmd, "pretty") {
		bytes, err := json.Marshal(item)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(bytes))
		return err
	}
	event, _ := item["event"].(map[string]any)
	line := prettyRealtimeEventLine(event)
	if _, err := fmt.Fprintln(stdout, line); err != nil {
		return err
	}
	if boolOption(cmd, "details") && event != nil {
		bytes, err := json.MarshalIndent(event, "", "\t")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, indent(string(bytes), "  "))
		return err
	}
	return nil
}

func prettyRealtimeEventLine(event map[string]any) string {
	if event == nil {
		return "workspace.activity"
	}
	eventType := itemString(event, "type")
	switch eventType {
	case "message.created", "message.updated":
		message, _ := event["message"].(map[string]any)
		sender, _ := message["sender"].(map[string]any)
		return fmt.Sprintf("%s %s %s: %s", eventType, conversationLabel(event["conversation"]), firstNonEmpty(itemString(sender, "name"), itemString(sender, "id"), "unknown"), itemString(message, "body"))
	case "wiki.page.updated":
		change, _ := event["change"].(map[string]any)
		page, _ := change["page"].(map[string]any)
		return fmt.Sprintf("wiki.page.updated [[%s]] v%v", firstNonEmpty(itemString(page, "title"), "page"), change["versionNumber"])
	case "wiki.page.deleted":
		page, _ := event["page"].(map[string]any)
		return fmt.Sprintf("wiki.page.deleted [[%s]]", firstNonEmpty(itemString(page, "title"), "page"))
	case "agent.activity.updated":
		activity, _ := event["activity"].(map[string]any)
		agent, _ := activity["agent"].(map[string]any)
		return fmt.Sprintf("agent.activity.%s %s", itemString(event, "status"), firstNonEmpty(itemString(agent, "name"), itemString(activity, "agentId"), "agent"))
	case "channel.created", "channel.updated":
		channel, _ := event["channel"].(map[string]any)
		return fmt.Sprintf("%s #%s", eventType, firstNonEmpty(itemString(channel, "name"), itemString(channel, "id"), "channel"))
	case "channel.deleted":
		return fmt.Sprintf("channel.deleted %v", event["channelId"])
	case "member.joined":
		member, _ := event["member"].(map[string]any)
		return fmt.Sprintf("member.joined %s", firstNonEmpty(itemString(member, "name"), itemString(member, "id"), "member"))
	case "agent.trace.updated":
		trace, _ := event["trace"].(map[string]any)
		agent, _ := trace["agent"].(map[string]any)
		return fmt.Sprintf("> %s [%s; wake=%s; %s]", itemString(trace, "summary"), itemString(agent, "name"), itemString(trace, "wakeId"), itemString(trace, "kind"))
	default:
		bytes, _ := json.Marshal(event)
		return fmt.Sprintf("%s %s", firstNonEmpty(eventType, "workspace.activity"), string(bytes))
	}
}

func conversationLabel(value any) string {
	conversation, _ := value.(map[string]any)
	if conversation == nil {
		return ""
	}
	if itemString(conversation, "kind") == "channel" {
		channel, _ := conversation["channel"].(map[string]any)
		return "#" + firstNonEmpty(itemString(channel, "name"), itemString(conversation, "channelId"), "channel")
	}
	return "dm"
}

func indent(value string, prefix string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

var _ = url.URL{}
