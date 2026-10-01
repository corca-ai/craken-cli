package craken

import (
	"context"
	"fmt"
	"strconv"
)

func prepareStreamResume(ctx context.Context, c *client, routes []route, plan commandExecution, cmd command, path string, query map[string]any, resolved map[string]string, output *streamOutput) (bool, error) {
	descriptor := output.plan
	defaults := descriptor != nil && descriptor.DefaultReconnect
	reconnect := boolOptionDefault(cmd, "reconnect", defaults)
	resume := boolOptionDefault(cmd, "resume", descriptor != nil && descriptor.DefaultResume)
	if boolOption(cmd, "once") {
		if !output.messages {
			return false, fmt.Errorf("--once requires message output")
		}
		if output.limit > 1 {
			return false, fmt.Errorf("--once cannot be combined with --limit greater than 1")
		}
		output.limit = 1
	}
	if !resume && !reconnect {
		return false, nil
	}
	if descriptor == nil || descriptor.ResumeQuery == "" {
		return false, fmt.Errorf("server catalog does not advertise stream replay")
	}
	var saved int64
	var found bool
	if resume {
		checkpoint, scope, err := streamCheckpointPath(c, cmd, path, query, resolved, output)
		if err != nil {
			return false, err
		}
		saved, found, err = readStreamCheckpoint(checkpoint, scope)
		if err != nil {
			return false, err
		}
		output.save = func(cursor int64) error { return writeStreamCheckpoint(checkpoint, scope, cursor) }
	}
	if explicit, ok := query[descriptor.ResumeQuery]; ok {
		cursor, err := strconv.ParseInt(fmt.Sprint(explicit), 10, 64)
		if err != nil || cursor < 0 || cursor > 9007199254740991 {
			return false, fmt.Errorf("invalid explicit stream cursor")
		}
		output.cursor = cursor
	} else if found {
		output.cursor = saved
	} else {
		cursor, err := streamBootstrapCursor(ctx, c, routes, plan, resolved, descriptor)
		if err != nil {
			return false, err
		}
		output.cursor = cursor
	}
	if output.save != nil {
		if err := output.save(output.cursor); err != nil {
			return false, err
		}
	}
	query[descriptor.ResumeQuery] = output.cursor
	return reconnect, nil
}

func streamBootstrapCursor(ctx context.Context, c *client, routes []route, plan commandExecution, resolved map[string]string, descriptor *commandStreamPlan) (int64, error) {
	operation := routeByID(routes, descriptor.Bootstrap.OperationID)
	if operation == nil || operation.Method != "GET" {
		return 0, fmt.Errorf("server catalog does not advertise a readable stream bootstrap")
	}
	bindings := map[string]commandBinding{}
	for name, binding := range plan.PathParams {
		bindings[name] = binding
	}
	for name := range resolved {
		bindings[name] = commandBinding{Source: commandBindingSourceResolved, Name: name, Required: true}
	}
	path, err := catalogResolverPath(ctx, c, routes, *operation, bindings, resolved)
	if err != nil {
		return 0, err
	}
	value, err := c.json(ctx, path)
	if err != nil {
		return 0, err
	}
	cursor, ok := valueAtPath(value, descriptor.Bootstrap.ResultPath).(float64)
	if !ok || cursor < 0 || cursor > 9007199254740991 || cursor != float64(int64(cursor)) {
		return 0, fmt.Errorf("server bootstrap returned an invalid stream cursor")
	}
	return int64(cursor), nil
}
