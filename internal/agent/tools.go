package agent

import (
	"fmt"
)

// Default tools registered with every new agent.

func exampleSearch(args map[string]any) (string, error) {
	q, _ := args["query"].(string)
	return fmt.Sprintf("search result for: %s", q), nil
}

func exampleCalc(args map[string]any) (string, error) {
	a, _ := args["a"].(float64)
	b, _ := args["b"].(float64)
	return fmt.Sprintf("%.2f", a+b), nil
}

// RegisterDemoTools adds example tools to the given executor.
func RegisterDemoTools(ex *ToolExecutor) {
	ex.RegisterTool("example_search", exampleSearch)
	ex.RegisterTool("example_calc", exampleCalc)
}
