package navi

import (
	"errors"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/memory"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the live path of the typed-object surface (ADR 0017 decision
// 6): the observation fanout and the surface's own mutations are translated
// into object.Delta and published on a bounded, cursor-ed window Watch
// reads. A portal therefore keeps its objects current without knowing queue
// or agent event kinds — a queue transition is a thought `update`, streaming
// agent content is an `append` to the thought's transcript detail, and a
// mutation the surface performed is an `update`/`delete`.
//
// The window itself is object.DeltaWindow, shared with the gateway's stream
// binding so the in-process and remote paths cannot drift (ADR 0019).

// timeNow is the delta timestamp source (UTC).
func timeNow() time.Time { return time.Now().UTC() }

// ── producers ───────────────────────────────────────────────────────────

// push publishes one delta on the surface's live window.
func (s *objectSurface) push(d object.Delta) {
	if s == nil || s.deltas == nil {
		return
	}
	s.deltas.Push(d)
}

// observeQueue translates a queue lifecycle event into a thought delta. A
// removal is a delete; every other transition is an update carrying the
// object's current exposed fields. A persist failure is not an object change
// (RAM is authoritative and unchanged), so it produces no delta.
func (s *objectSurface) observeQueue(ev thought.Event) {
	if ev.Kind == thought.EventDeleted {
		s.push(object.Delta{Type: TypeThought, ID: ev.ThoughtID, Op: object.OpDelete, At: ev.At})
		return
	}
	if ev.Kind == thought.EventPersistFailed {
		return
	}
	s.emitObject(TypeThought, ev.ThoughtID, ev.At)
}

// observeAgent translates a running agent's event into an append on the
// thought's transcript detail: a content delta is an assistant fragment, a
// tool call its input, and a tool result its output. Everything else
// (lifecycle transitions, errors) is not transcript content.
func (s *objectSurface) observeAgent(thoughtID string, ev agent.AgentEvent) {
	fields := map[string]any{"thought_id": thoughtID, "time": timeNow()}
	switch ev.Type {
	case agent.AgentEventContentDelta:
		if ev.Message == nil {
			return
		}
		fields["role"] = "assistant"
		fields["message"] = ev.Message.Content
	case agent.AgentEventToolCall:
		if ev.Tool == nil {
			return
		}
		fields["role"] = "assistant"
		fields["tool_name"] = ev.Tool.Name
		fields["tool_call_id"] = ev.Tool.ToolCallID
		if len(ev.Tool.Input) > 0 {
			fields["tool_input"] = string(ev.Tool.Input)
		}
	case agent.AgentEventToolResult:
		if ev.Tool == nil {
			return
		}
		fields["role"] = "tool"
		fields["tool_name"] = ev.Tool.Name
		fields["tool_call_id"] = ev.Tool.ToolCallID
		fields["message"] = ev.Tool.Result
	default:
		return
	}
	s.push(object.Delta{
		Type: TypeThought, ID: thoughtID, Op: object.OpAppend,
		Detail: DetailTranscript, Fields: fields,
	})
}

// emitMemory publishes a memory mutation reported by the short-term memory
// service after it has been persisted, so a portal's read sees the record the
// delta names.
func (s *objectSurface) emitMemory(rec datastore.MemoryRecord, op object.Op) {
	reg, ok := s.types.Get(TypeMemory)
	if !ok {
		return
	}
	d := object.Delta{Type: TypeMemory, ID: rec.ID, Op: op}
	if op != object.OpDelete {
		d.Fields = project(reg.Type, memoryObject(rec)).Fields
	}
	s.push(d)
}

// emitObject publishes the object's current state as an update, or a delete
// when it no longer exists. It is the post-action and queue-transition
// translation: the surface re-reads through the type's provider, so the delta
// always carries the same projection a List/Get would.
func (s *objectSurface) emitObject(typeName, id string, at time.Time) {
	reg, ok := s.types.Get(typeName)
	if !ok {
		return
	}
	obj, err := reg.Provider.Get(id)
	if err != nil {
		if notFound(err) {
			s.push(object.Delta{Type: typeName, ID: id, Op: object.OpDelete, At: at})
		}
		return
	}
	s.push(object.Delta{
		Type: typeName, ID: id, Op: object.OpUpdate,
		Fields: project(reg.Type, obj).Fields, At: at,
	})
}

// memoryChanged translates a persisted short-term-memory mutation into a
// memory object delta. It is the memory service's OnChange observer.
func (n *Navi) memoryChanged(rec datastore.MemoryRecord, change memory.Change) {
	if n.objects == nil {
		return
	}
	op := object.OpUpdate
	switch change {
	case memory.ChangeCreated:
		op = object.OpCreate
	case memory.ChangeRemoved:
		op = object.OpDelete
	}
	n.objects.emitMemory(rec, op)
}

// notFound reports whether a provider read failed because the object is gone
// rather than because the read itself failed. The built-in providers predate
// a single sentinel, so each one's not-found error is recognised.
func notFound(err error) bool {
	return errors.Is(err, object.ErrNotFound) ||
		errors.Is(err, thought.ErrThoughtNotFound) ||
		errors.Is(err, datastore.ErrTodoNotFound) ||
		errors.Is(err, datastore.ErrNoteNotFound)
}
