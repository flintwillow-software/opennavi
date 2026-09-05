package interfaces

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeInterface struct {
	name       string
	runErr     error
	blockUntil chan struct{}
	ran        bool
	stopped    bool
}

func (f *fakeInterface) Name() string { return f.name }
func (f *fakeInterface) Run(ctx context.Context) error {
	f.ran = true
	if f.blockUntil != nil {
		<-ctx.Done()
	}
	return f.runErr
}
func (f *fakeInterface) Stop() error { f.stopped = true; return nil }

func TestRegistrySelectEnabledOnly(t *testing.T) {
	r := NewRegistry()
	r.Register("cli", func(InterfaceConfig) (Interface, error) {
		return &fakeInterface{name: "cli"}, nil
	})
	r.Register("gateway", func(InterfaceConfig) (Interface, error) {
		return &fakeInterface{name: "gateway"}, nil
	})

	set := InterfaceSet{CLI: true}
	ifaces, err := r.Select(set)
	if err != nil {
		t.Fatal(err)
	}
	if len(ifaces) != 1 || ifaces[0].Name() != "cli" {
		t.Fatalf("selected = %v, want [cli]", names(ifaces))
	}
}

func TestRegistryMissingConstructor(t *testing.T) {
	r := NewRegistry()
	r.Register("cli", func(InterfaceConfig) (Interface, error) {
		return &fakeInterface{name: "cli"}, nil
	})
	// WebUI enabled but no constructor registered.
	_, err := r.Select(InterfaceSet{WebUI: InterfaceConfig{Enabled: true}})
	if err == nil {
		t.Fatal("missing constructor should error")
	}
}

func TestRegistryConstructorError(t *testing.T) {
	r := NewRegistry()
	r.Register("cli", func(InterfaceConfig) (Interface, error) {
		return nil, errors.New("bad config")
	})
	if _, err := r.Select(InterfaceSet{CLI: true}); err == nil {
		t.Fatal("constructor error should propagate")
	}
}

func TestRunAllShutdownOnError(t *testing.T) {
	blocker := &fakeInterface{name: "blocker", blockUntil: make(chan struct{})}
	failer := &fakeInterface{name: "failer", runErr: errors.New("fatal")}
	ifaces := []Interface{blocker, failer}

	err := RunAll(context.Background(), ifaces)
	if err == nil || err.Error() != "failer: fatal" {
		t.Fatalf("RunAll error = %v", err)
	}
	// The surviving interface was asked to stop.
	if !blocker.stopped {
		t.Fatal("blocker not stopped after failer's fatal error")
	}
}

func TestRunAllCtxCancel(t *testing.T) {
	blocker := &fakeInterface{name: "blocker", blockUntil: make(chan struct{})}
	ifaces := []Interface{blocker}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunAll(ctx, ifaces) }()
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("RunAll error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAll should return after ctx cancel")
	}
}

func TestInterfaceSetEnabled(t *testing.T) {
	set := InterfaceSet{
		CLI:   true,
		WebUI: InterfaceConfig{Enabled: true},
		Extra: map[string]bool{"telegram": true},
	}
	if !set.Enabled("cli") || !set.Enabled("webui") || !set.Enabled("telegram") {
		t.Fatal("enabled interfaces reported disabled")
	}
	if set.Enabled("gateway") || set.Enabled("cron") || set.Enabled("discord") {
		t.Fatal("disabled interfaces reported enabled")
	}
}

func names(ifaces []Interface) []string {
	out := make([]string, len(ifaces))
	for i, f := range ifaces {
		out[i] = f.Name()
	}
	return out
}
