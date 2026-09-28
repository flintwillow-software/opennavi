package time

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/tool/registry"
)

func newToolRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	if err := reg.SetActive([]string{"time_read"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	return reg
}

func dispatch(t *testing.T, reg *registry.Registry, args string) (string, error) {
	t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	return reg.Dispatch(context.Background(), "time_read", raw)
}

var rfc3339Re = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?([+-]\d{2}:\d{2}|Z)$`)

func TestDefinitionComplete(t *testing.T) {
	def := Definition()
	if def.Name != "time_read" {
		t.Fatalf("name = %q", def.Name)
	}
	if def.Access != registry.AccessReadOnly || def.Mode != registry.ToolModeParallel {
		t.Fatalf("access=%v mode=%v, want read-only parallel", def.Access, def.Mode)
	}
	if def.OutputFormat != "text" || def.Timeout != 5*time.Second || def.Recovery == "" || def.PromptSnippet == "" {
		t.Fatalf("incomplete metadata: %+v", def)
	}
	if def.SourceInfo.Origin != registry.OriginSDK {
		t.Fatalf("origin = %v, want sdk", def.SourceInfo.Origin)
	}
	if def.Namespace() != "time" {
		t.Fatalf("namespace = %q", def.Namespace())
	}
	if _, err := registry.ParseSchema(def.InputSchema.Raw()); err != nil {
		t.Fatalf("schema invalid: %v", err)
	}
}

func TestTimeReadCurrentTime(t *testing.T) {
	// No arguments: current time in the default zone (UTC) as ISO 8601
	// with UTC offset AND epoch seconds ("Reading current time").
	reg := newToolRegistry(t)
	before := time.Now().UTC()
	out, err := dispatch(t, reg, "")
	if err != nil {
		t.Fatalf("time_read: %v", err)
	}
	after := time.Now().UTC()

	fields := strings.Fields(out)
	if len(fields) < 3 {
		t.Fatalf("unexpected output shape: %q", out)
	}
	stamp := fields[0]
	if !rfc3339Re.MatchString(stamp) {
		t.Fatalf("not ISO 8601 with UTC offset: %q", stamp)
	}
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parse %q: %v", stamp, err)
	}
	if parsed.Before(before.Add(-time.Second)) || parsed.After(after.Add(time.Second)) {
		t.Fatalf("timestamp out of bounds: %v not in [%v, %v]", parsed, before, after)
	}
	if !strings.Contains(out, "+00:00") {
		t.Fatalf("UTC offset missing: %q", out)
	}
	// Epoch seconds are part of the default output per scenario 1.
	parts := strings.Split(out, "epoch: ")
	if len(parts) != 2 {
		t.Fatalf("epoch seconds missing from default output: %q", out)
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		t.Fatalf("epoch not an integer: %q", out)
	}
	if epoch < before.Unix()-1 || epoch > after.Unix()+1 {
		t.Fatalf("epoch out of bounds: %d not in [%d, %d]", epoch, before.Unix(), after.Unix())
	}
}

func TestTimeReadEpochToggle(t *testing.T) {
	reg := newToolRegistry(t)
	// epoch: false suppresses the epoch line.
	out, err := dispatch(t, reg, `{"epoch":false}`)
	if err != nil {
		t.Fatalf("time_read(epoch=false): %v", err)
	}
	if strings.Contains(out, "epoch:") {
		t.Fatalf("epoch line present despite epoch=false: %q", out)
	}
	// epoch: true includes it.
	out, err = dispatch(t, reg, `{"epoch":true}`)
	if err != nil {
		t.Fatalf("time_read(epoch=true): %v", err)
	}
	if !strings.Contains(out, "epoch:") {
		t.Fatalf("epoch line missing with epoch=true: %q", out)
	}
}

func TestTimeReadTimezoneConversion(t *testing.T) {
	reg := newToolRegistry(t)

	// A fixed-offset zone: conversion must shift the wall clock
	// exactly +05:30 from UTC, same instant.
	out, err := dispatch(t, reg, `{"timezone":"Asia/Kolkata"}`)
	if err != nil {
		t.Fatalf("time_read(Asia/Kolkata): %v", err)
	}
	stamp := strings.Fields(out)[0]
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parse %q: %v", stamp, err)
	}
	if _, offset := parsed.Zone(); offset != 5*3600+30*60 {
		t.Fatalf("offset = %d s, want 19800 (+05:30): %q", offset, out)
	}
	utc, err := dispatch(t, reg, `{"timezone":"UTC"}`)
	if err != nil {
		t.Fatalf("time_read(UTC): %v", err)
	}
	utcParsed, err := time.Parse(time.RFC3339, strings.Fields(utc)[0])
	if err != nil {
		t.Fatalf("parse UTC %q: %v", utc, err)
	}
	if diff := parsed.Sub(utcParsed); diff > 30*time.Second || diff < -30*time.Second {
		t.Fatalf("zones disagree on the instant: %v vs %v", parsed, utcParsed)
	}
	// Zone name echoed in the output.
	if !strings.Contains(out, "Asia/Kolkata") {
		t.Fatalf("zone name missing: %q", out)
	}
}

func TestTimeReadInvalidTimezone(t *testing.T) {
	reg := newToolRegistry(t)
	_, err := dispatch(t, reg, `{"timezone":"Mars/Olympus_Mons"}`)
	if err == nil {
		t.Fatal("expected error for invalid timezone")
	}
	if !strings.Contains(err.Error(), "Mars/Olympus_Mons") {
		t.Fatalf("error must identify the invalid zone: %v", err)
	}
	// Empty / non-string timezone values are rejected by schema or
	// handler.
	if _, err := dispatch(t, reg, `{"timezone":""}`); err == nil {
		t.Fatal("expected error for empty timezone")
	}
	if _, err := dispatch(t, reg, `{"timezone":42}`); err == nil {
		t.Fatal("expected error for non-string timezone")
	}
	if _, err := dispatch(t, reg, `{"epoch":"yes"}`); err == nil {
		t.Fatal("expected error for non-boolean epoch")
	}
	// Unknown properties rejected by the schema.
	if _, err := dispatch(t, reg, `{"zone":"UTC"}`); err == nil {
		t.Fatal("expected schema rejection for unknown property")
	}
}

func TestRegisterToolsValidation(t *testing.T) {
	if err := RegisterTools(nil); err == nil {
		t.Fatal("RegisterTools(nil) should fail")
	}
	// Duplicate registration is rejected by the registry.
	reg := registry.New()
	if err := RegisterTools(reg); err != nil {
		t.Fatalf("first RegisterTools: %v", err)
	}
	if err := RegisterTools(reg); !errors.Is(err, registry.ErrDuplicateTool) {
		t.Fatalf("duplicate err = %v, want ErrDuplicateTool", err)
	}
}

func TestTimeReadMonotonic(t *testing.T) {
	// Two successive calls never go backwards.
	reg := newToolRegistry(t)
	out1, err := dispatch(t, reg, `{"epoch":true}`)
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	out2, err := dispatch(t, reg, `{"epoch":true}`)
	if err != nil {
		t.Fatalf("call 2: %v", err)
	}
	e1, _ := strconv.ParseInt(strings.TrimSpace(strings.Split(out1, "epoch: ")[1]), 10, 64)
	e2, _ := strconv.ParseInt(strings.TrimSpace(strings.Split(out2, "epoch: ")[1]), 10, 64)
	if e2 < e1 {
		t.Fatalf("time went backwards: %d -> %d", e1, e2)
	}
}
