package craken

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

func runCatalogPollCommand(
	ctx context.Context,
	client *client,
	routes []route,
	route route,
	plan commandExecution,
	cmd command,
	path string,
	resolved map[string]string,
	consumed map[string]bool,
	stdout io.Writer,
	stdin io.Reader,
) error {
	interval, err := numberOption(cmd, plan.Poll.IntervalOption, plan.Poll.DefaultIntervalSeconds)
	if err != nil {
		return err
	}
	maxPolls, err := numberOption(cmd, plan.Poll.MaxPollsOption, plan.Poll.DefaultMaxPolls)
	if err != nil {
		return err
	}
	var last string
	for index := 0; index < maxPolls; index++ {
		requestPath, spec, err := catalogHTTPRequest(ctx, client, routes, route, commandExecution{
			BodyFields:  plan.BodyFields,
			OperationID: plan.OperationID,
			Output:      plan.Output,
			PathParams:  plan.PathParams,
			QueryParams: plan.QueryParams,
			Transport:   plan.Transport,
		}, cmd, path, resolved, consumed, stdin)
		if err != nil {
			return err
		}
		response, err := client.raw(ctx, route.Method, requestPath, spec)
		if err != nil {
			return err
		}
		payload, err := readPayload(response, route.Method, requestPath)
		if err != nil {
			return err
		}
		signatureBytes, _ := json.Marshal(payload.Parsed)
		signature := string(signatureBytes)
		if boolOption(cmd, "verbose") || signature != last {
			if err := printCatalogCommandPayload(stdout, payload, cmd, plan.Output); err != nil {
				return err
			}
		}
		last = signature
		if catalogPollTerminal(plan.Poll, payload.Parsed) || index == maxPolls-1 {
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

func catalogPollTerminal(plan *commandPollPlan, payload any) bool {
	status := valueString(valueAtPath(payload, plan.StatusPath))
	for _, terminal := range plan.TerminalValues {
		if status == terminal {
			return true
		}
	}
	return false
}
