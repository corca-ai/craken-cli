package craken

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var websocketDialer = websocket.DefaultDialer

func runCatalogWebSocketCommand(
	ctx context.Context,
	client *client,
	routes []route,
	plan commandExecution,
	cmd command,
	path string,
	resolved map[string]string,
	consumed map[string]bool,
	stdout io.Writer,
) error {
	query, err := catalogValues(ctx, client, routes, cmd, plan.QueryParams, resolved, consumed)
	if err != nil {
		return err
	}
	if plan.QueryParams != nil {
		path = appendQuery(path, query)
	}
	endpoint, err := client.resolve(path)
	if err != nil {
		return err
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
	protocols, err := catalogWebSocketProtocols(ctx, client, routes, plan.WebSocket.Protocols, cmd, resolved, consumed)
	if err != nil {
		return err
	}
	dialer := *websocketDialer
	dialer.Subprotocols = protocols
	connection, _, err := dialer.DialContext(ctx, endpoint.String(), http.Header{})
	if err != nil {
		return fmt.Errorf("websocket subscription failed for %s: %w", endpoint.String(), err)
	}
	defer func() { _ = connection.Close() }()
	if timeoutMS > 0 {
		_ = connection.SetReadDeadline(time.Now().Add(time.Duration(timeoutMS) * time.Millisecond))
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
		if err := printWebSocketMessage(stdout, message, cmd); err != nil {
			return err
		}
		seen++
		if limit > 0 && seen >= limit {
			_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "limit"), time.Now().Add(time.Second))
			return nil
		}
	}
}

func catalogWebSocketProtocols(
	ctx context.Context,
	client *client,
	routes []route,
	plans []commandWebSocketProtocol,
	cmd command,
	resolved map[string]string,
	consumed map[string]bool,
) ([]string, error) {
	protocols := make([]string, 0, len(plans))
	for _, plan := range plans {
		switch plan.Source {
		case "literal":
			if plan.Value != "" {
				protocols = append(protocols, plan.Value)
			}
		case "json-payload":
			values, err := catalogValues(ctx, client, routes, cmd, plan.Payload, resolved, consumed)
			if err != nil {
				return nil, err
			}
			bytes, err := json.Marshal(values)
			if err != nil {
				return nil, err
			}
			protocols = append(protocols, plan.Prefix+base64.RawURLEncoding.EncodeToString(bytes))
		default:
			return nil, fmt.Errorf("unsupported websocket protocol source: %s", plan.Source)
		}
	}
	return protocols, nil
}

func printWebSocketMessage(stdout io.Writer, message []byte, cmd command) error {
	if !boolOption(cmd, "pretty") {
		_, err := fmt.Fprintln(stdout, string(message))
		return err
	}
	var parsed any
	if err := json.Unmarshal(message, &parsed); err != nil {
		return err
	}
	return printJSON(stdout, parsed)
}
