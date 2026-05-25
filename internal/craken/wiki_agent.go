package craken

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

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
		parsed, err := client.json(ctx, workspacePath(workspaceID)+"/agent-jobs/"+url.PathEscape(jobID))
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
