package navi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
)

// modelListTool builds the built-in model_list discovery tool (ADR 0009):
// it exposes the navi's declared provider/model set — credential
// REFERENCES only, never values (ADR 0008) — so the agent can reason
// about which provider/model a thought should select.
func (n *Navi) modelListTool() (tool.Tool, error) {
	schema, err := tool.ParseSchema([]byte(
		`{"type":"object","properties":{},"additionalProperties":false}`))
	if err != nil {
		return tool.Tool{}, fmt.Errorf("navi: model_list schema: %w", err)
	}
	def := tool.Def{
		Name:          "model_list",
		Description:   "Lists the navi's declared provider/model set with the default marked, so a thought can choose a per-thought provider/model override. Credential references are named, never their values.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        tool.AccessReadOnly,
		Timeout:       5 * time.Second,
		Recovery:      "none",
		Mode:          tool.ToolModeParallel,
		Tags:          []string{"model", "provider", "discovery"},
		PromptSnippet: "model_list — list available providers/models and which is the default",
		SourceInfo:    tool.SourceInfo{Origin: tool.OriginBuiltin},
	}

	// The explicit WithDefaultModel wins over the descriptor marker, same
	// precedence buildTimeDefault uses.
	defaultName := n.defaultProvider
	if defaultName == "" {
		for _, p := range n.Models.All() {
			if p.Default {
				defaultName = p.Name
				break
			}
		}
	}

	handler := func(ctx context.Context, args map[string]any) (string, error) {
		type entry struct {
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			Default    bool   `json:"default"`
			Local      bool   `json:"local,omitempty"`
			Credential string `json:"credential_ref,omitempty"`
		}
		out := struct {
			Default      string  `json:"default,omitempty"`
			DefaultModel string  `json:"default_model,omitempty"`
			Models       []entry `json:"models"`
		}{Models: []entry{}}
		for _, p := range n.Models.All() {
			isDefault := p.Default || (defaultName != "" && strings.EqualFold(p.Name, defaultName))
			out.Models = append(out.Models, entry{
				Provider:   p.Name,
				Model:      p.Model,
				Default:    isDefault,
				Local:      p.Mode == "local",
				Credential: string(p.Credential),
			})
			if isDefault {
				out.Default, out.DefaultModel = p.Name, p.Model
			}
		}
		blob, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("model_list: encode: %w", err)
		}
		return string(blob), nil
	}

	return tool.Tool{Def: def, Handler: handler}, nil
}
