// Package portal implements the navi's portal control plane (S000407):
// transport-agnostic observation and control of a navi's thought process,
// authorized fail-closed.
//
// The observation and control contracts now live on the public `portal`
// package (NAVI-352 and its control follow-up). This internal package is
// the compatibility surface the daemon and its tests use: aliases onto the
// public types plus a `NewHub` shim for the public `Control` service, so
// there is one implementation rather than an internal copy.
package portal

import (
	pubportal "github.com/flintwillow-software/opennavi/portal"
)

// ErrPortalUnauthorized is the internal alias of the public sentinel: a
// refused attach/control action, never defaulted open.
var ErrPortalUnauthorized = pubportal.ErrUnauthorized

// Aliases onto the public contract types.
type (
	PortalCategory     = pubportal.Category
	PortalEvent        = pubportal.Event
	PortalMailbox      = pubportal.Mailbox
	PortalSubscription = pubportal.Subscription
	PortalObserver     = pubportal.Observer
	PortalAction       = pubportal.Action
	PortalAuthorizer   = pubportal.Authorizer
	Hub                = pubportal.Control
)

// Category and action constants, aliased for internal callers.
const (
	PortalQueue = pubportal.CategoryQueue
	PortalAck   = pubportal.CategoryAck
	PortalAgent = pubportal.CategoryAgent

	PortalActAttach  = pubportal.ActionAttach
	PortalActSteer   = pubportal.ActionSteer
	PortalActPause   = pubportal.ActionPause
	PortalActResume  = pubportal.ActionResume
	PortalActCancel  = pubportal.ActionCancel
	PortalActEnqueue = pubportal.ActionEnqueue
	PortalActBump    = pubportal.ActionBump
	PortalActRetry   = pubportal.ActionRetry
)

// NewPortalMailbox builds a bounded mailbox (public implementation).
func NewPortalMailbox(capacity int) *PortalMailbox { return pubportal.NewMailbox(capacity) }

// NewHub builds the shared control + observation service (the public
// Control). auth is the authorizer (nil fails closed).
func NewHub(auth PortalAuthorizer) *Hub { return pubportal.NewControl(auth) }
