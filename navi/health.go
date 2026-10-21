package navi

// Connector health (NAVI-329): the start-time state of the navi's doors.
//
// The one-assembly design makes a connector's failed Start non-fatal —
// a headless run without a free metrics or portal port still processes
// work — so the failure would otherwise be visible only in the startup
// logs. serve() records each door's outcome here and hands the view to a
// HealthRecorder, which the host persists and `navi health` reports.

// ConnectorState is one door's start-time state.
type ConnectorState string

const (
	// ConnectorOK: the component started and is serving.
	ConnectorOK ConnectorState = "ok"
	// ConnectorDisabled: the door is not configured. The composition root
	// supplies these entries (a disabled door declares no component, so
	// serve() cannot observe it).
	ConnectorDisabled ConnectorState = "disabled"
	// ConnectorFailed: Start returned an error; the navi is serving
	// without this door.
	ConnectorFailed ConnectorState = "failed"
)

// ConnectorHealth is one door's start-time health: a stable name
// ("scheduler", "metrics", "portal", or the component's own Name), the
// state, the listen address when it has one, and the failure reason.
type ConnectorHealth struct {
	Name   string         `json:"name"`
	State  ConnectorState `json:"state"`
	Addr   string         `json:"addr,omitempty"`
	Reason string         `json:"reason,omitempty"`
}

// HealthRecorder receives the navi's connector-health view once, after
// every component's Start has been attempted and before the run blocks.
// A host uses it to persist the view and warn about degraded doors; an
// error there is the host's concern, so the recorder returns nothing.
type HealthRecorder func([]ConnectorHealth)

// WithHealthRecorder registers a recorder for the start-time connector
// health (NAVI-329). Absent, the view is still recorded and available
// through Health().
func WithHealthRecorder(r HealthRecorder) Option {
	return func(n *Navi) { n.healthRecorder = r }
}

// Health returns the start-time connector-health view recorded by the
// last serve(). It is empty before Run has started the doors.
func (n *Navi) Health() []ConnectorHealth {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ConnectorHealth(nil), n.health...)
}

// healthReporter is the optional capability a component implements to
// report a stable name for the health view. Components already implement
// Name() for diagnostics; Addr() is the portal.Addressed shape (and the
// metrics connector mirrors it), matched structurally so this package
// needs no import of either.
type healthAddr interface{ Addr() string }

// describeHealth returns the component's name and listen address.
func describeHealth(v any) (string, string) {
	name := describe(v)
	if a, ok := v.(healthAddr); ok {
		return name, a.Addr()
	}
	return name, ""
}
