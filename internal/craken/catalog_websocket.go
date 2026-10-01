package craken

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	stderr io.Writer,
) error {
	output, err := newStreamOutput(cmd, plan.WebSocket.Stream)
	if err != nil {
		return err
	}
	query, err := catalogValues(ctx, client, routes, cmd, plan.QueryParams, resolved, consumed, nil)
	if err != nil {
		return err
	}
	if output.messages {
		for name, value := range output.plan.MessageQuery {
			if _, ok := query[name]; !ok {
				query[name] = value
			}
		}
	}
	reconnect, err := prepareStreamResume(ctx, client, routes, plan, cmd, path, query, resolved, output)
	if err != nil {
		return err
	}
	if len(query) > 0 {
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
	return runStreamConnections(ctx, endpoint, &dialer, output, timeoutMS, reconnect, cmd, stdout, stderr)
}

// wsConn is the minimal websocket connection surface used by the read loop. It
// is satisfied by *websocket.Conn and faked in tests.
type wsConn interface {
	ReadMessage() (int, []byte, error)
	SetReadDeadline(t time.Time) error
	WriteControl(messageType int, data []byte, deadline time.Time) error
}

func streamWebSocketMessages(conn wsConn, cmd command, limit int, timeoutMS int, stdout io.Writer) error {
	output, err := newStreamOutput(cmd, nil)
	if err != nil {
		return err
	}
	output.limit = limit
	err = readStream(conn, output, timeoutMS, stdout)
	if errors.Is(err, errStreamComplete) || errors.Is(err, errStreamInactive) || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return nil
	}
	return err
}

func readStream(conn wsConn, output *streamOutput, timeoutMS int, stdout io.Writer) error {
	if output.wait > 0 && output.waitUntil.IsZero() {
		output.waitUntil = time.Now().Add(output.wait)
	}
	armDeadline := func() {
		var deadline time.Time
		if timeoutMS > 0 {
			deadline = time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
		}
		if output.wait > 0 && (deadline.IsZero() || output.waitUntil.Before(deadline)) {
			deadline = output.waitUntil
		}
		if !deadline.IsZero() {
			_ = conn.SetReadDeadline(deadline)
		}
	}
	armDeadline()
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if (timeoutMS > 0 || output.wait > 0) && streamTimedOut(err) {
				return errStreamInactive
			}
			return err
		}
		done, err := output.emitFrame(stdout, message)
		if err != nil {
			return &streamDataError{err: err}
		}
		if done {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "limit"), time.Now().Add(time.Second))
			return errStreamComplete
		}
		// Re-arm the idle timeout after each received message so --timeout-ms is a
		// per-message idle window rather than a fixed total deadline from connect.
		armDeadline()
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
			values, err := catalogValues(ctx, client, routes, cmd, plan.Payload, resolved, consumed, nil)
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
	fields := cmd.string("fields", "")
	if !boolOption(cmd, "pretty") && fields == "" && cmd.string("format", "") != "ndjson" {
		_, err := fmt.Fprintln(stdout, string(message))
		return err
	}
	var parsed any
	if err := json.Unmarshal(message, &parsed); err != nil {
		return err
	}
	if fields != "" {
		var err error
		parsed, err = projectFields(parsed, fields)
		if err != nil {
			return err
		}
	}
	if boolOption(cmd, "pretty") {
		return printJSON(stdout, parsed)
	}
	return json.NewEncoder(stdout).Encode(parsed)
}
