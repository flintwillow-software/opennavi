// Portal serve shim (S000407): the daemon serves the observation + control
// portal so a remote gateway attachment can observe the navi's thought
// process and — once IAM lands — control it.
//
// The framing now lives in the public portal/gateway component (NAVI-352
// follow-up): this file is the thin adapter from the assembled daemon
// runtime onto that component, so there is one gateway implementation
// rather than an internal copy. The real wire protocol is still deferred
// to 05-messaging.
//
// Authorization: OBSERVATION is an attachment (S000407) — the stream
// carries prompt content and tool-input snippets, so attaching to it is
// governed by the same portal authorization as control. Every request
// authorizes through the wrapped portal's Attach; with no authorizer wired
// (IAM pending) everything fails closed 403 (S040200).
package daemon

import (
	"net/http"

	"github.com/flintwillow-software/opennavi/portal/gateway"
)

// DefaultPortalMailboxCap bounds the served portal stream: a client that
// reads slower than the runtime emits loses the oldest (bounded memory;
// the cursor reports the gap).
const DefaultPortalMailboxCap = gateway.DefaultSubscriptionCap

// PortalHandler returns the gateway handler: GET /portal/events drains the
// stream after a cursor; POST /portal/control applies a control action
// (authorizer-gated, fail closed).
func PortalHandler(d *Runtime) http.Handler {
	return gateway.Handler(d.Portal, d.Observation)
}
