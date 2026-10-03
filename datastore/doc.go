// Package datastore is the public persistence contract (ADR 0011): RAM is
// the source of truth at runtime, and the datastore is the
// eventual-consistency persistence layer beneath it. Nothing on a run
// path reads through it.
//
// A navi declares its candidate backends at build time (navi.Datastores)
// and selects the active one at runtime. Consumers never depend on "the
// datastore": they take a per-aggregate view — thought/transcript, tool
// state, or memory — and cannot tell which backend serves it. Handles are
// views, not backends (ADR 0011 decision 4): the same *Datastore handle
// resolves to whichever compiled candidate is activated, so retargeting
// storage is config, not an ABI change.
//
// The package is stdlib-only apart from the public thought package, so an
// out-of-repo component can depend on the contract without pulling
// runtime internals (ADR 0004). Backends live in their own packages —
// datastore/inmem (the non-durable reference backend), datastore/fs, and
// datastore/sqlite — and are ordinary components.
package datastore
