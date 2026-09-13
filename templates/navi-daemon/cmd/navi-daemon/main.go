// Command navi-daemon is the starter daemon-style navi: an always-on
// background agent (the OpenClaw/Hermes pattern) that consumes its
// thought queue, wakes on cron schedules, and serves the gateway as
// its delivery mode (interfaces per S000400 §01).
//
// Authoring model (S000200 §04): the Go definition below is the
// static, compiled-in half. The deployment half — provider endpoints,
// cron schedules, discovery sources — is runtime config under
// .config/navi/navi-daemon/ owned by the operator.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/flintwillow-software/opennavi/sdk"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

func main() {
	reg := registry.New()

	// One author tool registered through the tool registry (S130400):
	// registration IS the declaration.
	must(reg.Register(registry.ToolDefinition{
		Name:          "queue_status",
		Description:   "Reports the navi's own runtime status: uptime, process memory usage, and goroutine count. Use for self-diagnostics and in scheduled health reports.",
		InputSchema:   mustSchema(json.RawMessage(`{"type": "object"}`)),
		OutputFormat:  "text",
		Access:        registry.AccessReadOnly,
		Timeout:       5 * time.Second,
		Recovery:      "retry_once",
		Mode:          registry.ToolModeSequential,
		Tags:          []string{"daemon", "health", "status"},
		Version:       "1.0.0",
		PromptSnippet: "Report daemon uptime and runtime health",
		SourceInfo:    registry.SourceInfo{Origin: registry.OriginAuthor, Path: "cmd/navi-daemon/main.go"},
	}, queueStatus))

	// Activate the author tool. Catalog builtins join the active set
	// once the tool catalog ships (epic NAVI-59); they register through
	// the same registry.
	must(reg.SetActive([]string{"queue_status"}))

	def := sdk.NaviDef{
		Name:    "navi-daemon",
		Version: "0.1.0",
		Personality: "You are navi-daemon, an always-on operator's assistant. You are proactive but " +
			"quiet: you act on schedules and queued work, surface only what needs attention, and log the rest.",
		Interfaces: sdk.InterfaceSet{
			Gateway: sdk.InterfaceConfig{Enabled: true, HTTPPort: 8080},
			Cron:    sdk.InterfaceConfig{Enabled: true},
		},
		Permissions: sdk.PermissionPolicy{
			// A daemon talks to the world through its schedules and gateway;
			// deny interactive shell by default.
			Deny: []string{"shell_exec"},
		},
	}

	// sdk.Build validates the definition and compiles the binary
	// (S000300). It exits after producing the binary.
	if err := sdk.Build(def); err != nil {
		fmt.Fprintln(os.Stderr, "navi-daemon:", err)
		os.Exit(1)
	}
}

// queueStatus reports basic process runtime health.
func queueStatus(ctx context.Context, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return fmt.Sprintf("uptime=%s goroutines=%d heap=%dMiB",
		time.Since(start).Round(time.Second), runtime.NumGoroutine(), ms.HeapAlloc/(1<<20)), nil
}

var start = time.Now()

func mustSchema(raw json.RawMessage) registry.Schema {
	s, err := registry.ParseSchema(raw)
	if err != nil {
		panic(fmt.Sprintf("navi-daemon: tool schema: %v", err))
	}
	return s
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "navi-daemon:", err)
		os.Exit(1)
	}
}
