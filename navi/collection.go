package navi

import (
	"errors"
	"fmt"
	"sync"
)

// Sentinel errors the composition root reports. Callers test with
// errors.Is; the wrapping message names the collection and the item.
var (
	// ErrFrozen is returned by Add once the navi is serving. Collections
	// are mutable during declaration and read-only after Run (ADR 0002).
	ErrFrozen = errors.New("collections are frozen after Run")
	// ErrDuplicate is returned when a collection already holds an item
	// with the same name.
	ErrDuplicate = errors.New("duplicate name")
	// ErrInvalid is returned when an item fails shape validation.
	ErrInvalid = errors.New("invalid item")
)

// namer is an OPTIONAL capability. Components that can name themselves
// get precise duplicate detection and readable diagnostics; those that
// cannot are described by their type. Nothing is required to implement
// it — connector.Connector and portal.Portal deliberately do not, so
// an author's component is never forced to invent a name.
type namer interface{ Name() string }

// describe returns the best available label for a component.
func describe(v any) string {
	if n, ok := v.(namer); ok {
		if name := n.Name(); name != "" {
			return name
		}
	}
	return fmt.Sprintf("%T", v)
}

// Collection is one typed collection of navi components: the unit a navi
// author appends to during declaration. It is safe for concurrent use,
// though the intended pattern is single-goroutine declaration followed
// by Run.
//
// Ordering is insertion order — Run resolves execution order from it, so
// declaration order is meaningful and must not be sorted away.
type Collection[T any] struct {
	kind     string         // "tool", "portal", ... for diagnostics
	name     func(T) string // identity; nil = anonymous
	validate func(T) error  // shape validation; nil = none

	mu     sync.RWMutex
	items  []T
	index  map[string]int
	frozen bool
}

func newCollection[T any](kind string, name func(T) string, validate func(T) error) *Collection[T] {
	return &Collection[T]{
		kind:     kind,
		name:     name,
		validate: validate,
		index:    map[string]int{},
	}
}

// Add appends a component, doing eager shape validation. It returns an
// error rather than panicking: a bad collection is an authoring mistake
// that should surface at the call site, not take down a process.
//
// Add is the declaration-phase write. After Run it returns ErrFrozen.
func (c *Collection[T]) Add(v T) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return fmt.Errorf("%s: %w", c.kind, ErrFrozen)
	}
	if c.validate != nil {
		if err := c.validate(v); err != nil {
			return fmt.Errorf("%s: %w", c.kind, err)
		}
	}
	name := c.NameOf(v)
	if name != "" {
		if _, dup := c.index[name]; dup {
			return fmt.Errorf("%s %q: %w", c.kind, name, ErrDuplicate)
		}
		c.index[name] = len(c.items)
	}
	c.items = append(c.items, v)
	return nil
}

// Len returns how many components the collection holds.
func (c *Collection[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// All returns a snapshot copy of the collection in declaration order.
// The slice is the caller's; the components themselves are as they were
// added.
func (c *Collection[T]) All() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]T(nil), c.items...)
}

// Get returns the component registered under name.
func (c *Collection[T]) Get(name string) (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	i, ok := c.index[name]
	if !ok {
		var zero T
		return zero, false
	}
	return c.items[i], true
}

// Names returns the names of the components that have one, in
// declaration order.
func (c *Collection[T]) Names() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.items))
	for _, v := range c.items {
		if n := c.NameOf(v); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// NameOf reports a component's name, or "" when it is anonymous.
func (c *Collection[T]) NameOf(v T) string {
	if c.name == nil {
		return ""
	}
	return c.name(v)
}

// revalidate applies shape validation to everything already added. Run
// uses it so an ignored Add error cannot slip a malformed component
// into a serving navi.
func (c *Collection[T]) revalidate() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.validate == nil {
		return nil
	}
	for _, v := range c.items {
		if err := c.validate(v); err != nil {
			return fmt.Errorf("%s %s: %w", c.kind, describe(v), err)
		}
	}
	return nil
}

// freeze makes the collection read-only for the life of the navi.
func (c *Collection[T]) freeze() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frozen = true
}

// duplicates reports the first name that occurs more than once. Add
// rejects duplicates eagerly; Run re-checks so an ignored Add error
// cannot reach a serving navi.
func (c *Collection[T]) duplicates() (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[string]struct{}, len(c.items))
	for _, v := range c.items {
		name := c.NameOf(v)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			return name, true
		}
		seen[name] = struct{}{}
	}
	return "", false
}

// frozenNow reports whether the collection has been frozen.
func (c *Collection[T]) frozenNow() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.frozen
}
