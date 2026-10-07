package craken

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

func printCommandVariants(stdout io.Writer, command cliCommand) error {
	for _, variant := range command.Execution.Variants {
		condition := "default"
		if variant.When.Option != "" {
			condition = "--" + variant.When.Option + " supplied"
		}
		plan := mergeExecution(command.Execution, variant)
		if _, err := fmt.Fprintf(stdout, "\nVariant (%s):\n  Operation: %s\n", condition, plan.OperationID); err != nil {
			return err
		}
		for _, option := range executionBindingHelpOptions(plan) {
			if _, err := fmt.Fprintf(stdout, "  %s\n", option); err != nil {
				return err
			}
		}
	}
	return nil
}

func executionBindingHelpOptions(plan commandExecution) []string {
	unique := map[string]bool{}
	for _, bindings := range []map[string]commandBinding{plan.PathParams, plan.QueryParams, plan.BodyFields, plan.Multipart.Fields} {
		for _, binding := range bindings {
			if option := bindingHelpOption(binding); option != "" {
				unique[option] = true
			}
		}
	}
	for name, value := range map[string]string{plan.Multipart.FileOption: "PATH", plan.Multipart.FileNameOption: "NAME", plan.Multipart.ContentTypeOption: "MIME"} {
		if name != "" {
			option := "--" + name + " " + value
			if name == plan.Multipart.FileOption && plan.Multipart.FileOptional {
				option += "  optional"
			}
			unique[option] = true
		}
	}
	options := make([]string, 0, len(unique))
	for option := range unique {
		options = append(options, option)
	}
	sort.Strings(options)
	return options
}

func bindingHelpOption(binding commandBinding) string {
	if binding.Option == "" {
		return ""
	}
	details := []string{}
	if binding.Required {
		details = append(details, "required")
	}
	if binding.Default != nil {
		details = append(details, fmt.Sprintf("default: %v", binding.Default))
	}
	if binding.Resolver != nil {
		details = append(details, "accepts a "+binding.Resolver.Label+" name or id")
	}
	option := "--" + binding.Option
	switch binding.Source {
	case commandBindingSourceText:
		option += " TEXT"
		if binding.FileOption != "" {
			option += " | --" + binding.FileOption + " PATH"
			details = append(details, "inline text or file, not both; use - for stdin")
		}
		if binding.Positionals == commandBindingPositionalsJoin {
			details = append(details, "also accepts trailing text")
		}
	case commandBindingSourceOption, "":
		value := "VALUE"
		switch binding.Type {
		case commandBindingValueTypeInteger:
			value = "N"
		case commandBindingValueTypeJSON:
			value = "JSON"
		}
		option += " " + value
	case commandBindingSourceFlag:
	default:
		return ""
	}
	if len(binding.Aliases) > 0 {
		details = append(details, "aliases: --"+strings.Join(binding.Aliases, ", --"))
	}
	if len(details) > 0 {
		option += "  " + strings.Join(details, "; ")
	}
	return option
}

func localCommandHelpOptions(command cliCommand) []string {
	options := []string{}
	options = append(options, executionBindingHelpOptions(command.Execution)...)
	if command.Execution.Output != nil {
		options = append(options, "--fields LIST             Print JSON projected to comma-separated dotted fields.")
		if command.Execution.Output.Mode == commandOutputModeTable {
			options = append(options, "--compact                 Print the catalog table columns as tab-separated text.")
		}
	}
	if command.Execution.Poll != nil {
		options = append(
			options,
			fmt.Sprintf("--%s N              Poll interval in seconds.", command.Execution.Poll.IntervalOption),
			fmt.Sprintf("--%s N             Maximum poll attempts.", command.Execution.Poll.MaxPollsOption),
		)
	}
	if command.Execution.Transport == commandTransportDownload {
		options = append(options, "--output PATH             Write downloaded bytes to a file instead of stdout.")
	}
	if command.Execution.Transport == commandTransportWebSocket {
		options = append(
			options,
			"--limit N                 Stop after N frames (raw) or emitted records (--messages).",
			"--timeout-ms MS          Transport idle window; any received frame resets it.",
			"--pretty                 Pretty-print JSON WebSocket messages.",
			"--fields LIST            Project dotted JSON fields (on records in message mode).",
			"--format raw|ndjson      Raw frames or one JSON value per line; raw allows --pretty.",
		)
		if command.Execution.WebSocket.Stream != nil {
			stream := command.Execution.WebSocket.Stream
			options = append(options,
				fmt.Sprintf("--messages               Emit message NDJSON (default %t); --format raw gets full events.", stream.DefaultMessages),
				"--wait-timeout-ms MS      Relevant-message wait; unrelated frames do not reset it.",
				"--once                   Emit one matching message and exit (message mode only).",
				fmt.Sprintf("--resume[=false]         Persist scoped scan position (default %t); first start reads current head.", stream.DefaultResume),
				fmt.Sprintf("--reconnect[=false]      Retry transient disconnects (default %t).", stream.DefaultReconnect),
				"--max-retries N          Consecutive retry budget, 0..100 (default 5).",
				"--retry-delay-ms MS      Initial backoff, 1..5000 (default 250); doubles with jitter, capped at 5s.",
				"Resume advances after successful stdout output, not completed work; deduplicate stable event/message ids.",
				"One active consumer per resume scope. Explicit --after overrides saved state; --resume=false disables persistence.",
				"Exit: 0 for limit/once or inactivity, 1 for auth/failure/exhausted retries, 130 for SIGINT, 143 for SIGTERM.",
			)
		}
	}
	return options
}
