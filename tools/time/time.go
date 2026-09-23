// Package time implements the time_read tool (S130500 §03): the
// wall-clock affordance every production agent needs. Stateless —
// no store handle, no per-navi persistence; register with the plain
// registry.Register through RegisterTools.
//
// time_read returns the current time in the requested IANA timezone
// (default: UTC — the navi's configured zone surfaces when the
// runtime zone-config lands) as ISO 8601 with UTC offset, plus
// epoch seconds when requested.
package time

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/tools/registry"
)

// DefaultZone is the navi's configured zone default (S130500 §03):
// UTC until the runtime zone-config surface lands.
const DefaultZone = "UTC"

// Definition returns the complete ToolDefinition for time_read.
func Definition() registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"timezone": { "type": "string", "minLength": 1 },
			"epoch": { "type": "boolean" }
		},
		"additionalProperties": false
	}`))
	return registry.ToolDefinition{
		Name:          "time_read",
		Description:   "Returns the current time in the requested IANA timezone (default: the navi's configured zone, UTC) as ISO 8601 with UTC offset, plus Unix epoch seconds when requested.",
		InputSchema:   schema,
		OutputFormat:  "text",
		Access:        registry.AccessReadOnly,
		Timeout:       5 * time.Second,
		Recovery:      "none",
		Mode:          registry.ToolModeParallel,
		Tags:          []string{"time", "clock", "stateless"},
		PromptSnippet: "time_read — current time (ISO 8601 with offset, optional epoch seconds, IANA timezone)",
		SourceInfo:    registry.SourceInfo{Origin: registry.OriginSDK},
	}
}

// RegisterTools registers time_read on reg (plain registry.Register —
// the tool is stateless, S130500 §03/§07).
func RegisterTools(reg *registry.Registry) error {
	if reg == nil {
		return errors.New("time: registry is required")
	}
	if err := reg.Register(Definition(), handleRead); err != nil {
		return fmt.Errorf("time: register time_read: %w", err)
	}
	return nil
}

// handleRead implements time_read (S130500 §03): current time in the
// requested IANA zone as ISO 8601 with offset, epoch seconds when
// asked for.
func handleRead(ctx context.Context, args map[string]any) (string, error) {
	zone := DefaultZone
	if raw, ok := args["timezone"]; ok {
		s, ok := raw.(string)
		if !ok || s == "" {
			return "", errors.New("time_read: timezone must be a non-empty string")
		}
		zone = s
	}
	wantEpoch := true
	if raw, ok := args["epoch"]; ok {
		b, ok := raw.(bool)
		if !ok {
			return "", errors.New("time_read: epoch must be a boolean")
		}
		wantEpoch = b
	}

	loc, err := time.LoadLocation(zone)
	if err != nil {
		// Identify the invalid zone so the LLM can self-correct.
		return "", fmt.Errorf("time_read: invalid timezone %q: %w", zone, err)
	}

	now := time.Now().In(loc)
	out := fmt.Sprintf("%s (UTC offset %s, %s)", now.Format(time.RFC3339), now.Format("-07:00"), loc.String())
	if wantEpoch {
		out = fmt.Sprintf("%s\nepoch: %d", out, now.Unix())
	}
	return out, nil
}
