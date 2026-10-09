package portal

import (
	"time"

	"github.com/flintwillow-software/opennavi/object"
)

// This file is the unified portal stream (ADR 0019 decisions 2 and 3): one
// stream per portal, carrying the bounded opening snapshot, observation
// events, object deltas, and control results. The frames do not invent a new
// dialect — every frame is a Surface call or result, or an observation event
// or object delta — so the in-process binding and the remote binding deliver
// the same contract to the same model.
//
// The first remote binding is SSE downstream plus POST upstream
// (ADR 0019 decision 4). A socket binding is added only when a named
// consumer needs low-latency upstream; the frames below are binding-neutral.

// StreamVersion identifies the frame dialect. A binding carries it out of
// band (the gateway sets the X-Navi-Stream-Version response header) so a
// client refuses a stream it does not understand rather than misreading it
// (ADR 0019 decision 9). Bump it only for an incompatible frame change.
const StreamVersion = 1

// FrameKind discriminates a stream frame.
type FrameKind string

const (
	// FrameCall is an upstream Surface call (or a creation request).
	FrameCall FrameKind = "call"
	// FrameResult is the downstream answer to a FrameCall.
	FrameResult FrameKind = "result"
	// FrameEvent is an observation event (portal.Event).
	FrameEvent FrameKind = "event"
	// FrameDelta is a live object delta (object.Delta).
	FrameDelta FrameKind = "delta"
)

// Frame is one item on the portal stream. Exactly one of the payload fields
// is set, selected by Kind.
type Frame struct {
	Kind   FrameKind     `json:"kind"`
	Call   *Call         `json:"call,omitempty"`
	Result *Result       `json:"result,omitempty"`
	Event  *Event        `json:"event,omitempty"`
	Delta  *object.Delta `json:"delta,omitempty"`
	// Cursor is the server's delta cursor after this frame, so a reconnecting
	// client resumes exactly where it stopped (ADR 0019 decision 6).
	Cursor int `json:"cursor,omitempty"`
}

// Call methods. They are the object.Surface verbs plus creation: an object
// that does not exist yet has no id to Act on, so creation is its own verb
// (ADR 0019 decision 7).
const (
	MethodTypes   = "types"
	MethodList    = "list"
	MethodGet     = "get"
	MethodDetail  = "detail"
	MethodAct     = "act"
	MethodWatch   = "watch"
	MethodEnqueue = "enqueue"
)

// Call is one Surface call on the stream: the upstream half. It is a plain
// data record so any binding (POST today, a socket frame later) can carry it.
type Call struct {
	Method string `json:"method"`

	// Type/ID/Detail/Action address the object (List/Get/Detail/Act).
	Type   string `json:"type,omitempty"`
	ID     string `json:"id,omitempty"`
	Detail string `json:"detail,omitempty"`
	Action string `json:"action,omitempty"`

	// Query is the bounded-read selection for List/Detail.
	Query object.Query `json:"query,omitempty"`

	// After is the Watch cursor.
	After int `json:"after,omitempty"`

	// Args are the declared action's arguments.
	Args map[string]any `json:"args,omitempty"`

	// Enqueue fields: a thought that does not exist yet.
	Prompt     string     `json:"prompt,omitempty"`
	Priority   int        `json:"priority,omitempty"`
	DeferUntil *time.Time `json:"deferUntil,omitempty"`
}

// Result is the downstream half: the answer to a Call, or a stream control
// frame. The zero value is a successful empty result. Error is a string
// because a frame must survive a binding that cannot carry Go error values;
// a caller that needs the sentinel matches on the message's prefix.
type Result struct {
	// Types is the descriptor set (MethodTypes, or the opening snapshot).
	Types []object.Type `json:"types,omitempty"`
	// Type names the type an opening-snapshot page belongs to.
	Type string `json:"type,omitempty"`
	// Objects is a List/Detail page, or the opening snapshot's page.
	Objects []object.Object `json:"objects,omitempty"`
	// Object is a Get result.
	Object *object.Object `json:"object,omitempty"`
	// Next is the bounded-read cursor ("" = last page).
	Next string `json:"next,omitempty"`
	// Deltas is a Watch result.
	Deltas []object.Delta `json:"deltas,omitempty"`
	// Cursor is the next Watch cursor.
	Cursor int `json:"cursor,omitempty"`
	// Lost reports a slow-consumer gap (the bounded window dropped deltas).
	Lost bool `json:"lost,omitempty"`
	// Snapshot marks the opening-snapshot page frames, so a client can tell
	// them from a call result.
	Snapshot bool `json:"snapshot,omitempty"`
	// SnapshotDone marks the end of the opening snapshot, so a client knows
	// the live stream has begun.
	SnapshotDone bool `json:"snapshotDone,omitempty"`
	// OK is the success flag for a mutation (Act/Enqueue).
	OK bool `json:"ok,omitempty"`
	// Error is the failure message ("" = success).
	Error string `json:"error,omitempty"`
}

// Stream is one portal's unified stream (ADR 0019 decision 2): the object
// surface, the observation feed, and creation, delivered as one thing. An
// in-process portal and a remote portal are two bindings of it, so the model
// above them is identical.
type Stream interface {
	// Objects is the typed-object surface: descriptors, bounded reads,
	// declared actions, and live deltas.
	Objects() object.Surface
	// Feed is the observation feed the stream delivers (pushed, not polled).
	Feed() Subscription
	// Enqueue creates a thought. Creation is not an action: a thought that
	// does not exist yet has no id to Act on.
	Enqueue(prompt string, prio int, deferUntil time.Time) error
	// Close ends the stream and releases its feed.
	Close() error
}
