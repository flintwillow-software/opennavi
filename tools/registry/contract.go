package registry

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/tool"
)

// ToDef converts a runtime ToolDefinition into the public contract Def.
// The two are field-for-field identical by design (see the tool package
// note); this is the mechanical bridge the catalog port uses to expose
// registry-declared tools as collection values. prepareArguments is a
// registration concern and is deliberately absent from the contract.
func ToDef(d ToolDefinition) (tool.Def, error) {
	schema, err := tool.ParseSchema(d.InputSchema.Raw())
	if err != nil {
		return tool.Def{}, fmt.Errorf("registry: convert %q schema: %w", d.Name, err)
	}
	return tool.Def{
		Name:             d.Name,
		Description:      d.Description,
		InputSchema:      schema,
		OutputFormat:     d.OutputFormat,
		Access:           tool.ToolAccess(d.Access),
		Timeout:          d.Timeout,
		Recovery:         d.Recovery,
		Mode:             tool.ToolMode(d.Mode),
		Tags:             append([]string(nil), d.Tags...),
		Version:          d.Version,
		PromptSnippet:    d.PromptSnippet,
		PromptGuidelines: append([]string(nil), d.PromptGuidelines...),
		SourceInfo: tool.SourceInfo{
			Origin: tool.ToolOrigin(d.SourceInfo.Origin),
			Path:   d.SourceInfo.Path,
		},
	}, nil
}
