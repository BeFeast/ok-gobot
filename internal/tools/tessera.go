package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"ok-gobot/internal/tessera"
)

// TesseraTool shares the deterministic coordinator. Authority is never accepted
// as a parameter, and one immutable invocation per tool/update survives replay.
type TesseraTool struct {
	Coordinator *tessera.Coordinator
	Op          string
}

func (t *TesseraTool) Name() string { return "tessera_" + t.Op }
func (t *TesseraTool) Description() string {
	return "Tessera " + t.Op + " using this trusted Telegram turn. Replies save unverified knowledge; seen never completes a goal. Queued/combined/background turns are unsupported; use the explicit Telegram commands."
}
func (t *TesseraTool) GetSchema() map[string]interface{} {
	props := map[string]interface{}{}
	required := []string{}
	add := func(name string) {
		props[name] = map[string]interface{}{"type": "string"}
		required = append(required, name)
	}
	switch t.Op {
	case "inbox_list", "attention_list":
		props["cursor"] = map[string]interface{}{"type": "string", "description": "Exact next_cursor from the previous page, omitted for the first page."}
	case "inbox_capture":
		add("text")
	case "inbox_get":
		add("capture_id")
	case "attention_get":
		add("goal_id")
		add("attention_id")
		add("revision")
	case "attention_reply", "attention_ack":
		add("goal_id")
		add("attention_id")
		add("expected_revision")
		add("stage_id")
		props["stage_id"] = map[string]interface{}{"type": "string", "description": "Exact selected stage ID, or the literal null for a stage-less item."}
		if t.Op == "attention_reply" {
			add("text")
		}
	}
	return map[string]interface{}{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func (t *TesseraTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) != 1 {
		return "", errors.New("Tessera requires one JSON argument object")
	}
	var p map[string]string
	if err := json.Unmarshal([]byte(args[0]), &p); err != nil {
		return "", err
	}
	return t.ExecuteJSON(ctx, p)
}
func (t *TesseraTool) ExecuteJSON(ctx context.Context, p map[string]string) (string, error) {
	turn, ok := tessera.TrustedTurn(ctx)
	if !ok || t.Coordinator == nil {
		return "", errors.New("Tessera requires an immutable, single Telegram turn")
	}
	schema := t.GetSchema()
	props := schema["properties"].(map[string]interface{})
	for k := range p {
		if _, ok := props[k]; !ok {
			return "", fmt.Errorf("unsupported Tessera parameter: %s", k)
		}
	}
	for _, k := range schema["required"].([]string) {
		if _, ok := p[k]; !ok {
			return "", fmt.Errorf("missing Tessera parameter: %s", k)
		}
	}
	command := map[string]any{"op": t.Op}
	for k, v := range p {
		command[k] = v
	}
	if p["stage_id"] == "null" {
		command["stage_id"] = nil
	}
	var r json.RawMessage
	var err error
	switch t.Op {
	case "inbox_capture", "attention_reply", "attention_ack":
		r, err = t.Coordinator.Mutate(ctx, turn.Telegram, "tool-"+t.Op, command)
	default:
		r, err = t.Coordinator.Read(ctx, turn.Telegram, command)
	}
	return string(r), err
}
