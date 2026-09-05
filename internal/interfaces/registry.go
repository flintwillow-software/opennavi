package interfaces

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Interface is one delivery mode compiled into (or selected for) a
// navi binary. Run is long-running: it blocks until ctx is done or a
// fatal error occurs. Stop requests a graceful shutdown and MUST be
// idempotent and safe to call after Run has returned — RunAll calls
// Stop on every interface (including ones that already exited) before
// waiting for them.
type Interface interface {
	Name() string
	Run(ctx context.Context) error
	Stop() error
}

// Constructor builds an Interface from the generic config block.
// Adapters that need richer configuration should carry it in their own
// option struct and adapt here.
type Constructor func(cfg InterfaceConfig) (Interface, error)

// Registry holds named interface constructors and selects the ones
// enabled in an InterfaceSet.
type Registry struct {
	mu     sync.RWMutex
	ctors  map[string]Constructor
	orderC []string // registration order (stable Select output)
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{ctors: make(map[string]Constructor)}
}

// Register adds a constructor under the given name. Duplicate names
// replace the previous constructor.
func (r *Registry) Register(name string, ctor Constructor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.ctors[name]; !exists {
		r.orderC = append(r.orderC, name)
	}
	r.ctors[name] = ctor
}

// Names returns the registered interface names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.orderC...)
}

// Select instantiates every interface enabled in the set. It errors if
// an enabled interface has no registered constructor — a navi binary
// missing a compiled-in interface is a build/packaging problem, not a
// runtime condition to skip silently.
func (r *Registry) Select(set InterfaceSet) ([]Interface, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Interface
	for _, name := range r.orderC {
		if !set.Enabled(name) {
			continue
		}
		iface, err := r.ctors[name](set.ConfigFor(name))
		if err != nil {
			return nil, fmt.Errorf("interfaces: construct %s: %w", name, err)
		}
		if iface == nil {
			return nil, fmt.Errorf("interfaces: constructor for %s returned nil", name)
		}
		if iface.Name() != name {
			return nil, fmt.Errorf("interfaces: constructor %s produced interface named %q", name, iface.Name())
		}
		out = append(out, iface)
	}

	// Enabled but not registered: report every missing name, sorted for
	// deterministic errors.
	var missing []string
	for _, name := range allKnownNames(set) {
		if set.Enabled(name) {
			if _, ok := r.ctors[name]; !ok {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("interfaces: enabled but not registered: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// RunAll runs all interfaces concurrently. It blocks until ctx is done
// or one interface returns a fatal error; the remaining interfaces are
// stopped and awaited.
func RunAll(ctx context.Context, ifaces []Interface) error {
	if len(ifaces) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(ifaces))
	var wg sync.WaitGroup
	for _, iface := range ifaces {
		wg.Add(1)
		iface := iface
		go func() {
			defer wg.Done()
			if err := iface.Run(ctx); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("%s: %w", iface.Name(), err)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(errCh)
	}()

	var runErr error
	select {
	case err := <-errCh:
		runErr = err
		cancel() // first fatal error shuts the rest down
	case <-ctx.Done():
	}

	// Stop BEFORE waiting: the contract allows shutdown via Stop
	// rather than ctx (Stop-driven adapters like HTTP servers ignore
	// ctx and would deadlock a wg.Wait that precedes Stop). Stop is
	// idempotent and safe after Run returned, so already-exited
	// interfaces tolerate it.
	for _, iface := range ifaces {
		_ = iface.Stop()
	}
	wg.Wait()

	if runErr != nil {
		return runErr
	}
	return ctx.Err()
}

// allKnownNames lists every interface name the set can enable.
func allKnownNames(set InterfaceSet) []string {
	names := []string{"cli", "webui", "gateway", "cron"}
	for name := range set.Extra {
		names = append(names, name)
	}
	return names
}
