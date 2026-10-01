package craken

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// The server describes stream envelopes and message fields. Product event names
// and routing remain in the catalog, just as they do for HTTP commands.
type commandStreamPlan struct {
	SinglePath       string            `json:"singlePath"`
	BatchPath        string            `json:"batchPath"`
	CursorPath       string            `json:"cursorPath"`
	CheckpointPath   string            `json:"checkpointPath"`
	RequiredPath     string            `json:"requiredPath"`
	OutputFields     map[string]string `json:"outputFields"`
	MessageQuery     map[string]string `json:"messageQuery"`
	ResumeQuery      string            `json:"resumeQuery"`
	DefaultMessages  bool              `json:"defaultMessages"`
	DefaultReconnect bool              `json:"defaultReconnect"`
	DefaultResume    bool              `json:"defaultResume"`
	Bootstrap        struct {
		OperationID string `json:"operationId"`
		ResultPath  string `json:"resultPath"`
	} `json:"bootstrap"`
}

type streamOutput struct {
	cmd         command
	plan        *commandStreamPlan
	messages    bool
	limit, seen int
	cursor      int64
	save        func(int64) error
	wait        time.Duration
	waitUntil   time.Time
}

func newStreamOutput(cmd command, plan *commandStreamPlan) (*streamOutput, error) {
	messages := plan != nil && plan.DefaultMessages
	messages = boolOptionDefault(cmd, "messages", messages)
	if cmd.string("format", "") == "raw" {
		messages = false
	}
	if messages && (plan == nil || plan.RequiredPath == "" || len(plan.OutputFields) == 0) {
		return nil, fmt.Errorf("server catalog does not advertise message stream output")
	}
	if boolOption(cmd, "compact") {
		return nil, fmt.Errorf("--compact is not supported for streams; use --messages or --fields")
	}
	format := cmd.string("format", "")
	if format != "" && format != "ndjson" && format != "raw" {
		return nil, fmt.Errorf("stream --format must be ndjson or raw")
	}
	if boolOption(cmd, "pretty") && (messages || format == "ndjson") {
		return nil, fmt.Errorf("--pretty cannot be combined with message/NDJSON output; use --format raw --pretty")
	}
	if fields := cmd.string("fields", ""); fields != "" {
		if _, err := projectFields(map[string]any{}, fields); err != nil {
			return nil, err
		}
	}
	limit, err := numberOption(cmd, "limit", 0)
	if err != nil {
		return nil, err
	}
	wait, err := numberOption(cmd, "wait-timeout-ms", 0)
	if err != nil {
		return nil, err
	}
	if limit < 0 || wait < 0 {
		return nil, fmt.Errorf("stream limits and timeouts must be nonnegative")
	}
	if wait > 0 && !messages {
		return nil, fmt.Errorf("--wait-timeout-ms requires message output")
	}
	return &streamOutput{cmd: cmd, plan: plan, messages: messages, limit: limit, wait: time.Duration(wait) * time.Millisecond}, nil
}

func boolOptionDefault(cmd command, name string, fallback bool) bool {
	if _, ok := cmd.Options[name]; ok {
		return boolOption(cmd, name)
	}
	if _, ok := cmd.Flags[name]; ok {
		return boolOption(cmd, name)
	}
	return fallback
}

func (s *streamOutput) emitFrame(out io.Writer, frame []byte) (bool, error) {
	if !s.messages && s.plan == nil {
		return s.emitRaw(out, frame)
	}
	var envelope any
	if err := json.Unmarshal(frame, &envelope); err != nil {
		return false, err
	}
	records := outputRows(envelope, s.plan.BatchPath)
	if record := valueAtPath(envelope, s.plan.SinglePath); record != nil {
		records = []any{record}
	}
	if !s.messages {
		done, err := s.emitRaw(out, frame)
		if err != nil {
			return false, err
		}
		for _, record := range records {
			if err := s.advance(valueAtPath(record, s.plan.CursorPath)); err != nil {
				return false, err
			}
		}
		if err := s.advance(valueAtPath(envelope, s.plan.CheckpointPath)); err != nil {
			return false, err
		}
		return done, nil
	}
	for _, record := range records {
		if valueAtPath(record, s.plan.RequiredPath) != nil {
			projected := map[string]any{}
			for destination, source := range s.plan.OutputFields {
				if value := valueAtPath(record, source); value != nil {
					setStreamField(projected, fieldPathParts(destination), value)
				}
			}
			var output any = projected
			if fields := s.cmd.string("fields", ""); fields != "" {
				var err error
				output, err = projectFields(output, fields)
				if err != nil {
					return false, err
				}
			}
			if err := json.NewEncoder(out).Encode(output); err != nil {
				return false, err
			}
			s.seen++
			s.waitUntil = time.Now().Add(s.wait)
		}
		if err := s.advance(valueAtPath(record, s.plan.CursorPath)); err != nil {
			return false, err
		}
		// A partial batch must not acknowledge its unconsumed tail.
		if s.limit > 0 && s.seen >= s.limit {
			return true, nil
		}
	}
	return false, s.advance(valueAtPath(envelope, s.plan.CheckpointPath))
}

func (s *streamOutput) emitRaw(out io.Writer, frame []byte) (bool, error) {
	if err := printWebSocketMessage(out, frame, s.cmd); err != nil {
		return false, err
	}
	s.seen++
	return s.limit > 0 && s.seen >= s.limit, nil
}

func (s *streamOutput) advance(value any) error {
	number, ok := value.(float64)
	if !ok || number <= float64(s.cursor) {
		return nil
	}
	if number != float64(int64(number)) || number > 9007199254740991 {
		return fmt.Errorf("invalid stream cursor")
	}
	cursor := int64(number)
	if s.save != nil {
		if err := s.save(cursor); err != nil {
			return err
		}
	}
	s.cursor = cursor
	return nil
}

func setStreamField(root map[string]any, parts []string, value any) {
	if len(parts) == 0 {
		return
	}
	for _, part := range parts[:len(parts)-1] {
		next, ok := root[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			root[part] = next
		}
		root = next
	}
	root[parts[len(parts)-1]] = value
}
