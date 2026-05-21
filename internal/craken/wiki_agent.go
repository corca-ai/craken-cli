package craken

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

func runWiki(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	workspaceID, err := workspaceIDFromCommand(ctx, client, cmd)
	if err != nil {
		return err
	}
	switch cmd.Action {
	case "list":
		return printClientJSON(ctx, client, stdout, "GET", workspacePath(workspaceID)+"/wiki/pages", nil)
	case "deleted":
		return printClientJSON(ctx, client, stdout, "GET", workspacePath(workspaceID)+"/wiki/deleted-pages", nil)
	case "recent":
		path := workspacePath(workspaceID) + "/wiki/recent-changes"
		if limit := cmd.string("limit", ""); limit != "" {
			path += "?limit=" + url.QueryEscape(limit)
		}
		return printClientJSON(ctx, client, stdout, "GET", path, nil)
	case "get":
		title, err := cmd.required("title", "page")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "GET", wikiPagePath(workspaceID, title), nil)
	case "save":
		content, err := readTextOption(cmd, "content", "content-file")
		if err != nil {
			return err
		}
		baseVersionNumber, err := positiveNumberOption(cmd, "base-version")
		if err != nil {
			return err
		}
		if existing := cmd.string("existing-title", ""); existing != "" {
			return printClientJSON(
				ctx,
				client,
				stdout,
				"PATCH",
				wikiPagePath(workspaceID, existing),
				wikiSaveBody(content, cmd.string("title", existing), baseVersionNumber),
			)
		}
		title, err := cmd.required("title")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/wiki/pages", wikiSaveBody(content, title, baseVersionNumber))
	case "delete":
		title, err := cmd.required("title", "page")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "DELETE", wikiPagePath(workspaceID, title), nil)
	case "restore":
		title, err := cmd.required("title", "page")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", wikiPagePath(workspaceID, title)+"/restore", map[string]any{})
	case "versions":
		title, err := cmd.required("title", "page")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "GET", wikiPagePath(workspaceID, title)+"/versions", nil)
	case "version":
		title, err := cmd.required("title", "page")
		if err != nil {
			return err
		}
		version, err := cmd.required("version")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "GET", wikiPagePath(workspaceID, title)+"/versions/"+url.PathEscape(version), nil)
	case "diff":
		title, err := cmd.required("title", "page")
		if err != nil {
			return err
		}
		params := url.Values{}
		if value := cmd.string("from", ""); value != "" {
			params.Set("from", value)
		}
		if value := cmd.string("to", ""); value != "" {
			params.Set("to", value)
		}
		return printClientJSON(ctx, client, stdout, "GET", wikiPagePath(workspaceID, title)+"/diff?"+params.Encode(), nil)
	default:
		return fmt.Errorf("unknown wiki action: %s", cmd.Action)
	}
}

func wikiSaveBody(content string, title string, baseVersionNumber *int) map[string]any {
	body := map[string]any{"content": content, "title": title}
	if baseVersionNumber != nil {
		body["baseVersionNumber"] = *baseVersionNumber
	}
	return body
}

func runAgent(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	if cmd.Action == "plan-check" {
		previous, err := jsonOption(cmd, "previous-json", "previous-file", false)
		if err != nil {
			return err
		}
		next, err := jsonOption(cmd, "next-json", "next-file", true)
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", "/api/admin/agent-job-plan/progression", map[string]any{"previousPlan": previous, "nextPlan": next})
	}
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
		purpose, err := cmd.required("purpose")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/agents", map[string]any{"name": name, "purpose": purpose})
	case "jobs":
		params := url.Values{}
		if value := cmd.string("limit", ""); value != "" {
			params.Set("limit", value)
		}
		if value := cmd.string("offset", ""); value != "" {
			params.Set("offset", value)
		}
		path := workspacePath(workspaceID) + "/agent-jobs"
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return printClientJSON(ctx, client, stdout, "GET", path, nil)
	case "job":
		wake, err := cmd.required("wake", "job")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "GET", workspacePath(workspaceID)+"/agent-jobs/"+url.PathEscape(wake), nil)
	case "watch":
		return watchAgentJob(ctx, client, workspaceID, cmd, stdout)
	case "interrupt", "stop":
		wake, err := cmd.required("wake")
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", workspacePath(workspaceID)+"/agent-wakes/"+url.PathEscape(wake)+"/interrupt", compact(map[string]any{"reason": cmd.string("reason", "")}))
	default:
		return fmt.Errorf("unknown agent action: %s", cmd.Action)
	}
}

func watchAgentJob(ctx context.Context, client *client, workspaceID string, cmd command, stdout io.Writer) error {
	jobID, err := cmd.required("wake", "job")
	if err != nil {
		return err
	}
	interval, err := numberOption(cmd, "interval", 2)
	if err != nil {
		return err
	}
	maxPolls, err := numberOption(cmd, "max-polls", 60)
	if err != nil {
		return err
	}
	var last string
	for index := 0; index < maxPolls; index++ {
		parsed, err := client.json(ctx, "GET", workspacePath(workspaceID)+"/agent-jobs/"+url.PathEscape(jobID), nil)
		if err != nil {
			return err
		}
		snapshot := agentJobWatchSnapshot(parsed)
		signature := fmt.Sprintf("%v", compact(map[string]any{"status": snapshot["status"], "plan": snapshot["plan"], "error": snapshot["error"]}))
		if boolOption(cmd, "verbose") || signature != last {
			if err := printJSON(stdout, snapshot); err != nil {
				return err
			}
		}
		last = signature
		if terminalStatus(snapshot["status"]) || index == maxPolls-1 {
			return nil
		}
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func agentJobWatchSnapshot(parsed any) map[string]any {
	root, _ := parsed.(map[string]any)
	job, _ := root["job"].(map[string]any)
	plan, _ := job["plan"].(map[string]any)
	var planSnapshot any
	if plan != nil {
		itemsRaw, _ := plan["items"].([]any)
		items := make([]any, 0, len(itemsRaw))
		completed := 0
		for _, itemRaw := range itemsRaw {
			item, _ := itemRaw.(map[string]any)
			if itemString(item, "status") == "done" {
				completed++
			}
			items = append(items, compact(map[string]any{"evidence": item["evidence"], "id": item["id"], "status": item["status"], "title": item["title"]}))
		}
		planSnapshot = map[string]any{"completedItems": completed, "items": items, "nextAction": plan["nextAction"], "status": plan["status"], "totalItems": len(items)}
	}
	return map[string]any{
		"error":         job["error"],
		"executionMode": job["executionMode"],
		"id":            job["id"],
		"observedAt":    time.Now().UTC().Format(time.RFC3339Nano),
		"plan":          planSnapshot,
		"sliceCount":    job["sliceCount"],
		"status":        job["status"],
	}
}

func terminalStatus(value any) bool {
	status, _ := value.(string)
	return status == "cancelled" || status == "completed" || status == "failed"
}

func runDream(ctx context.Context, client *client, cmd command, stdout io.Writer) error {
	workspaceID, err := workspaceIDFromCommand(ctx, client, cmd)
	if err != nil {
		return err
	}
	agentName, err := cmd.required("agent")
	if err != nil {
		return err
	}
	participantID, err := resolveParticipantID(ctx, client, workspaceID, agentName)
	if err != nil {
		return err
	}
	if len(participantID) < len("agent:") || participantID[:len("agent:")] != "agent:" {
		return fmt.Errorf("dream agent must resolve to an agent participant, got %s", participantID)
	}
	agentID := participantID[len("agent:"):]
	switch cmd.Action {
	case "issue-smoke":
		return printClientJSON(ctx, client, stdout, "POST", "/api/admin/dream/issue-smoke", map[string]any{"agentId": agentID, "agentName": agentName, "reason": cmd.string("reason", "manual Dream GitHub issue reporter smoke test"), "workspaceId": workspaceID})
	case "run":
		limit, err := numberOption(command{Options: map[string]string{"activity-limit": firstNonEmpty(cmd.string("activity-limit", ""), cmd.string("limit", ""))}}, "activity-limit", 50)
		if err != nil {
			return err
		}
		return printClientJSON(ctx, client, stdout, "POST", "/api/admin/dream/run", map[string]any{"activityLimit": limit, "agentId": agentID, "apply": boolOption(cmd, "apply"), "reason": cmd.string("reason", "manual CLI dream run"), "workspaceId": workspaceID})
	default:
		return fmt.Errorf("unknown dream action: %s", cmd.Action)
	}
}
