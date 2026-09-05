// Package interfaces implements the interface set abstraction (spec
// 00-navi-core/04-interface-set.md): interfaces are delivery modes
// (CLI, TUI, Gateway, ...) that a navi binary can enable in any
// combination. This package defines the contract and selection
// machinery; concrete adapters register themselves by name.
//
// Build-time pruning (compiling only selected interfaces into the
// binary) lands with the builder/compiler (#140); until then selection
// is runtime, over constructors registered at init time. The
// Interface/registry seam is designed so the builder can drive it
// without changes.
package interfaces

// InterfaceConfig is the generic per-interface configuration block
// (spec §01). Concrete adapters extend it via their own option structs;
// the spec's per-interface fields (ports, tokens, themes) live there.
type InterfaceConfig struct {
	Enabled bool
	Port    int
	Addr    string
}

// InterfaceSet mirrors the navi definition's interface selection (spec
// §00/§02): any combination of delivery modes can be enabled.
type InterfaceSet struct {
	CLI     bool            // interactive terminal chat (REPL for now, TUI later)
	WebUI   InterfaceConfig // HTTP + WebSocket chat
	Gateway InterfaceConfig // webhook daemon
	Cron    InterfaceConfig // scheduled tasks
	// Platform adapters (Telegram/Discord/Slack) are follow-ups on the
	// Gateway contract; they register with the same registry.
	Extra map[string]bool
}

// Enabled reports whether the named interface is enabled in the set.
func (s InterfaceSet) Enabled(name string) bool {
	switch name {
	case "cli":
		return s.CLI
	case "webui":
		return s.WebUI.Enabled
	case "gateway":
		return s.Gateway.Enabled
	case "cron":
		return s.Cron.Enabled
	default:
		return s.Extra[name]
	}
}

// ConfigFor returns the generic config block for the named interface
// (empty for interfaces without per-interface config).
func (s InterfaceSet) ConfigFor(name string) InterfaceConfig {
	switch name {
	case "webui":
		return s.WebUI
	case "gateway":
		return s.Gateway
	case "cron":
		return s.Cron
	default:
		return InterfaceConfig{}
	}
}
