package craken

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func runWorkspace(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	switch cmd.Action {
	case "list":
		return printClientJSON(ctx, client, stdout, "GET", "/api/workspaces", nil)
	case "create":
		name, err := cmd.required("name")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", "/api/workspaces", map[string]any{"name": name})
	case "get", "snapshot", "activity", "delete", "invite", "accept", "subs", "tail":
	default:
		return fmt.Errorf("unknown workspace action: %s", cmd.Action)
	}

	if cmd.Action == "accept" {
		token, err := cmd.required("invitation-token", "token")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", "/api/workspace-invitations/"+url.PathEscape(token)+"/accept", map[string]any{})
	}
	workspace, err := cmd.required("workspace")
	if err != nil {
		return err
	}
	workspaceID, err := resolveWorkspaceID(ctx, client, workspace)
	if err != nil {
		return err
	}
	switch cmd.Action {
	case "get":
		return printClientJSON(ctx, client, stdout, "GET", workspacePath(workspaceID), nil)
	case "snapshot":
		return printClientJSON(ctx, client, stdout, "GET", workspacePath(workspaceID)+"/snapshot", nil)
	case "activity":
		params := url.Values{}
		if value := cmd.string("anchor-json", ""); value != "" {
			params.Set("anchor", value)
		}
		if value := cmd.string("before-sequence", ""); value != "" {
			params.Set("beforeSequence", value)
		}
		if value := cmd.string("limit", ""); value != "" {
			params.Set("limit", value)
		}
		for _, surface := range stringListOption(cmd, "surfaces") {
			params.Add("surfaces", surface)
		}
		path := workspacePath(workspaceID) + "/activity"
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return printClientJSON(ctx, client, stdout, "GET", path, nil)
	case "delete":
		body := map[string]any(nil)
		if name := cmd.string("name", ""); name != "" {
			body = map[string]any{"name": name}
		}
		return printClientJSON(ctx, client, stdout, "DELETE", workspacePath(workspaceID), body)
	case "invite":
		email, err := cmd.required("email")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/invitations", map[string]any{"email": email})
	case "subs", "tail":
		return tailWorkspace(ctx, client, workspaceID, cmd, stdout)
	}
	return nil
}

func runChannel(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	workspaceID, err := workspaceIDFromCommand(ctx, client, cmd)
	if err != nil {
		return err
	}
	switch cmd.Action {
	case "list":
		return printClientJSON(ctx, client, stdout, "GET", workspacePath(workspaceID)+"/channels", nil)
	case "create":
		name, err := cmd.required("name", "channel")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/channels", map[string]any{"name": name, "purpose": cmd.string("purpose", "")})
	case "update", "delete", "join", "leave", "add-member", "messages", "send":
	default:
		return fmt.Errorf("unknown channel action: %s", cmd.Action)
	}
	channelID, err := channelIDFromCommand(ctx, client, workspaceID, cmd)
	if err != nil {
		return err
	}
	switch cmd.Action {
	case "update":
		return printClientJSON(ctx, client, stdout, "PATCH", workspacePath(workspaceID)+"/channels/"+url.PathEscape(channelID), compact(map[string]any{"name": cmd.string("name", ""), "purpose": cmd.string("purpose", "")}))
	case "delete":
		return printClientJSON(ctx, client, stdout, "DELETE", workspacePath(workspaceID)+"/channels/"+url.PathEscape(channelID), nil)
	case "join":
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/channels/"+url.PathEscape(channelID)+"/members/me", map[string]any{})
	case "leave":
		return printClientJSON(ctx, client, stdout, "DELETE", workspacePath(workspaceID)+"/channels/"+url.PathEscape(channelID)+"/members/me", nil)
	case "add-member":
		participant, err := cmd.required("participant", "target")
		if err != nil {
			return err
		}
		participantID, err := resolveParticipantID(ctx, client, workspaceID, participant)
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/channels/"+url.PathEscape(channelID)+"/members", map[string]any{"participantId": participantID})
	case "messages":
		return printClientJSON(ctx, client, stdout, "GET", workspacePath(workspaceID)+"/channels/"+url.PathEscape(channelID)+"/messages"+messagePageQuery(cmd), nil)
	case "send":
		body, err := messageBody(cmd)
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/channels/"+url.PathEscape(channelID)+"/messages", localSenderBody(cmd, body))
	}
	return nil
}

func runDM(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	workspaceID, err := workspaceIDFromCommand(ctx, client, cmd)
	if err != nil {
		return err
	}
	target, err := cmd.required("target", "participant")
	if err != nil {
		return err
	}
	participantID, err := resolveParticipantID(ctx, client, workspaceID, target)
	if err != nil {
		return err
	}
	path := workspacePath(workspaceID) + "/direct-messages/" + url.PathEscape(participantID) + "/messages"
	switch cmd.Action {
	case "send":
		body, err := messageBody(cmd)
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", path, localSenderBody(cmd, body))
	case "messages", "list":
		return printClientJSON(ctx, client, stdout, "GET", path+messagePageQuery(cmd), nil)
	default:
		return fmt.Errorf("unknown dm action: %s", cmd.Action)
	}
}

func runFile(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	workspaceID, err := workspaceIDFromCommand(ctx, client, cmd)
	if err != nil {
		return err
	}
	switch cmd.Action {
	case "list":
		return printClientJSON(ctx, client, stdout, "GET", workspacePath(workspaceID)+"/files", nil)
	case "upload":
		path, err := cmd.required("path", "file-path")
		if err != nil {
			return err
		}
		name := cmd.string("name", filepath.Base(path))
		parsed, err := client.multipart(ctx, "POST", workspacePath(workspaceID)+"/files", map[string]string{
			"scope":      cmd.string("scope", "workspace"),
			"folderPath": cmd.string("folder", ""),
		}, "file", path, name, cmd.string("type", "application/octet-stream"))
		if err != nil {
			return err
		}
		return printJSON(stdout, parsed)
	case "get", "download":
		fileID, err := cmd.required("file")
		if err != nil {
			return err
		}
		route := "content"
		if cmd.Action == "download" {
			route = "download"
		}
		response, err := client.raw(ctx, "GET", workspacePath(workspaceID)+"/files/"+url.PathEscape(fileID)+"/"+route, requestSpec{})
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		bytes, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("GET file failed with %d: %s", response.StatusCode, string(bytes))
		}
		if output := cmd.string("output", ""); output != "" {
			return os.WriteFile(filepath.Clean(output), bytes, 0o600)
		}
		_, err = stdout.Write(bytes)
		return err
	case "update-content":
		fileID, err := cmd.required("file")
		if err != nil {
			return err
		}
		content, err := readTextOption(cmd, "content", "content-file")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "PUT", workspacePath(workspaceID)+"/files/"+url.PathEscape(fileID)+"/content", map[string]any{"content": content})
	case "patch", "move", "rename":
		fileID, err := cmd.required("file")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "PATCH", workspacePath(workspaceID)+"/files/"+url.PathEscape(fileID), compact(map[string]any{"folderPath": cmd.string("folder", ""), "name": cmd.string("name", ""), "scope": cmd.string("scope", "")}))
	case "delete":
		fileID, err := cmd.required("file")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "DELETE", workspacePath(workspaceID)+"/files/"+url.PathEscape(fileID), nil)
	default:
		return fmt.Errorf("unknown file action: %s", cmd.Action)
	}
}

func runFolder(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	workspaceID, err := workspaceIDFromCommand(ctx, client, cmd)
	if err != nil {
		return err
	}
	switch cmd.Action {
	case "create":
		name, err := cmd.required("name")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/folders", map[string]any{"name": name, "parentPath": cmd.string("parent", ""), "scope": cmd.string("scope", "workspace")})
	case "update", "move", "rename":
		folderID, err := cmd.required("folder")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "PATCH", workspacePath(workspaceID)+"/folders/"+url.PathEscape(folderID), compact(map[string]any{"name": cmd.string("name", ""), "parentPath": cmd.string("parent", ""), "scope": cmd.string("scope", "")}))
	case "delete":
		folderID, err := cmd.required("folder")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "DELETE", workspacePath(workspaceID)+"/folders/"+url.PathEscape(folderID), nil)
	default:
		return fmt.Errorf("unknown folder action: %s", cmd.Action)
	}
}

func printClientJSON(ctx context.Context, client *client, stdout io.Writer, method string, path string, body any) error {
	parsed, err := client.json(ctx, method, path, body)
	if err != nil {
		return err
	}
	return printJSON(stdout, parsed)
}

func workspaceIDFromCommand(ctx context.Context, client *client, cmd command) (string, error) {
	workspace, err := cmd.required("workspace")
	if err != nil {
		return "", err
	}
	return resolveWorkspaceID(ctx, client, workspace)
}

func channelIDFromCommand(ctx context.Context, client *client, workspaceID string, cmd command) (string, error) {
	channel, err := cmd.required("channel")
	if err != nil {
		return "", err
	}
	return resolveChannelID(ctx, client, workspaceID, channel)
}

func messageBody(cmd command) (string, error) {
	if cmd.string("body", "") != "" && cmd.string("body-file", "") != "" {
		return "", fmt.Errorf("use either --body or --body-file, not both")
	}
	if file := cmd.string("body-file", ""); file != "" {
		bytes, err := os.ReadFile(filepath.Clean(file))
		return string(bytes), err
	}
	if body := cmd.string("body", ""); body != "" {
		return body, nil
	}
	if len(cmd.Positionals) > 0 {
		return strings.Join(cmd.Positionals, " "), nil
	}
	return "", fmt.Errorf("expected message body through --body, --body-file, or trailing positional text")
}

func localSenderBody(cmd command, body string) map[string]any {
	return compact(map[string]any{
		"body":        body,
		"senderEmail": firstNonEmpty(cmd.string("sender", ""), cmd.string("sender-email", "")),
		"senderName":  cmd.string("sender-name", ""),
	})
}

func messagePageQuery(cmd command) string {
	params := url.Values{}
	for _, key := range []string{"position", "before", "after", "around"} {
		if value := cmd.string(key, ""); value != "" {
			params.Set(key, value)
		}
	}
	if len(params) == 0 {
		return ""
	}
	return "?" + params.Encode()
}

func methodUpper(value string) string {
	return strings.ToUpper(value)
}

var _ = http.MethodGet
