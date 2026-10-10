package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/flintwillow-software/opennavi/tool"
)

// This file is the egress seam (ADR 0014, ADR 0020): a connector's outbound
// half, expressed as ordinary tool.Tool values the author adds explicitly.
//
// It lives in the contract connector package and references only `tool` and
// the standard library. That is deliberate: `connector` is a contract package
// (ADR 0004), so its dependency closure must stay implementation-free — a
// signature mentioning tool/registry would put an implementation package in
// the closure every out-of-repo connector author inherits, and the
// external-module CI guard fails the build for exactly that.

// ErrNotStarted reports an egress call made while its connector is not
// serving: the tool was bound at authoring time, but the client arrives at
// Start (ADR 0014). It is a clean error, never a nil dereference.
var ErrNotStarted = errors.New("connector: not started")

// Live is a connector's lifecycle-gated handle to its service client. The
// connector stores the client in Start, clears it in Stop, and an egress
// handler reads it through Get.
//
// It is the reason SendTool can build the gate into the handler rather than
// trusting every connector author to remember it: ADR 0014's consequence
// ("tools must tolerate an unstarted connector") becomes a property of the
// mechanism.
//
// A Live must not be copied: it holds a lock. Hold it by pointer
// (NewLive, or new(Live[C])).
type Live[C any] struct {
	mu      sync.RWMutex
	client  C
	serving bool
}

// NewLive builds an empty, not-yet-serving handle.
func NewLive[C any]() *Live[C] { return &Live[C]{} }

// Set publishes the live client and marks the connector serving. Start calls
// it once the client is usable; calling it again replaces the client.
func (l *Live[C]) Set(client C) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.client = client
	l.serving = true
}

// Clear drops the client and marks the connector stopped, so a call after
// Stop fails the same way a call before Start does.
func (l *Live[C]) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	var zero C
	l.client = zero
	l.serving = false
}

// Get returns the live client, or ErrNotStarted while the connector is not
// serving.
func (l *Live[C]) Get() (C, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !l.serving {
		var zero C
		return zero, ErrNotStarted
	}
	return l.client, nil
}

// Serving reports whether the connector currently has a live client.
func (l *Live[C]) Serving() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.serving
}

// SendTool builds one egress tool: an ordinary tool.Tool whose handler is
// handed the connector's live client at call time. The author adds the result
// to the navi's tool collection explicitly — nothing auto-registers it, so an
// egress tool is absent from a binary whose author did not add it (ADR 0006).
//
// The descriptor must declare what the tool does rather than inherit a
// default: a namespaced name (the namespace is the connector's identity) and a
// real access level are required, and SendTool stamps the provenance, so
// "what can this navi reach, and through which component?" is answerable from
// the catalog (ADR 0020 decision 5).
func SendTool[C any](def tool.Def, live *Live[C], call func(ctx context.Context, client C, args map[string]any) (string, error)) (tool.Tool, error) {
	if live == nil {
		return tool.Tool{}, fmt.Errorf("connector: egress tool %q: nil live handle", def.Name)
	}
	if call == nil {
		return tool.Tool{}, fmt.Errorf("connector: egress tool %q: nil handler", def.Name)
	}
	if err := validateEgress(def); err != nil {
		return tool.Tool{}, err
	}
	def.SourceInfo.Origin = tool.OriginAuthor
	if def.SourceInfo.Path == "" {
		def.SourceInfo.Path = def.Namespace()
	}
	return tool.Tool{
		Def: def,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			client, err := live.Get()
			if err != nil {
				return "", fmt.Errorf("connector: egress tool %q: %w", def.Name, err)
			}
			return call(ctx, client, args)
		},
	}, nil
}

// validateEgress enforces the shape an egress tool must declare. Each rule
// exists so a capability cannot be registered in a shape that hides what it
// does: an unnamed or un-namespaced tool is unattributable, an "any" access
// level is not a level, and a tool with no argument schema cannot be called
// correctly by a model.
//
// The recovery strategy is required here as well as by the registry, so an
// egress tool that is not registrable fails at composition time rather than
// when the navi assembles.
func validateEgress(def tool.Def) error {
	name := strings.TrimSpace(def.Name)
	if name == "" {
		return errors.New("connector: egress tool needs a name")
	}
	ns := def.Namespace()
	if ns == name || ns == "" {
		return fmt.Errorf("connector: egress tool %q must be namespaced namespace_action (e.g. github_issue_comment)", name)
	}
	if def.Access == tool.AccessAny {
		return fmt.Errorf("connector: egress tool %q must declare an access level", name)
	}
	if len(def.InputSchema.Raw()) == 0 {
		return fmt.Errorf("connector: egress tool %q must declare an input schema", name)
	}
	if def.Recovery == "" {
		return fmt.Errorf("connector: egress tool %q must declare a recovery strategy (the registry requires one)", name)
	}
	return nil
}
