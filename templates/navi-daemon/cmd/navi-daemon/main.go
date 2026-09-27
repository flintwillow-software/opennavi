// Command navi-daemon is a starter daemon-style navi: a real navi module
// that assembles through navi.New, wakes on cron schedules, and serves the
// HTTP portal gateway.
//
// Authoring model (S000200 §04): the Go definition below is the static,
// compiled-in half. Deployment settings (provider endpoints, credential
// references, cron overrides) come from the config file named by
// NAVI_CONFIG, if set.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/flintwillow-software/opennavi/connector/cron"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/tool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "navi-daemon:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := []navi.Option{
		navi.WithVersion("0.1.0"),
		navi.WithPersonality("You are navi-daemon, an always-on operator's assistant. You are proactive but " +
			"quiet: you act on schedules and queued work, surface only what needs attention, and log the rest."),
		// A daemon talks to the world through its schedules and gateway;
		// deny interactive shell by default.
		navi.WithPermissions(navi.PermissionPolicy{DenyTools: []string{"shell_exec"}}),
	}
	if cfg := os.Getenv("NAVI_CONFIG"); cfg != "" {
		opts = append(opts, navi.WithRuntimeConfig(cfg))
	}
	n := navi.New("navi-daemon", opts...)

	if err := n.Tools.Add(queueStatusTool()); err != nil {
		return err
	}

	// One model, marked default (ADR 0009).
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		return err
	}

	// Cron: schedules are a thought factory (ADR 0010).
	if err := n.Connectors.Add(cron.New(cron.Config{Schedules: []cron.Schedule{
		{Name: "health", Expr: "*/30 * * * *", Prompt: "Report the navi's runtime health concisely."},
	}})); err != nil {
		return err
	}

	// Gateway: the HTTP observation + control portal. Control is
	// authorizer-gated and fails closed until IAM lands, so this starter
	// binds no control portal (observation only).
	if err := n.Portals.Add(gateway.New(":8080", nil)); err != nil {
		return err
	}

	return n.Run(ctx)
}

// queueStatusTool declares queue_status as a public contract tool.
func queueStatusTool() tool.Tool {
	schema, _ := tool.ParseSchema(json.RawMessage(`{"type": "object"}`))
	return tool.Tool{
		Def: tool.Def{
			Name:          "queue_status",
			Description:   "Reports the navi's own runtime status: uptime, process memory usage, and goroutine count. Use for self-diagnostics and in scheduled health reports.",
			InputSchema:   schema,
			OutputFormat:  "text",
			Access:        tool.AccessReadOnly,
			Timeout:       5 * time.Second,
			Recovery:      "retry_once",
			Mode:          tool.ToolModeSequential,
			Tags:          []string{"daemon", "health", "status"},
			Version:       "1.0.0",
			PromptSnippet: "Report daemon uptime and runtime health",
			SourceInfo:    tool.SourceInfo{Origin: tool.OriginAuthor, Path: "cmd/navi-daemon/main.go"},
		},
		Handler: queueStatus,
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
