package metrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
)

// Connector serves the thought-queue metrics exposition as a navi
// component (NAVI-357): Start wires the collector to the navi's thought
// system and serves /metrics on the configured address; Stop shuts the
// listener down with the run.
//
// A bind failure is reported as a Start error and the composition root
// treats a connector's failed start as non-fatal: the metrics door is
// observability, not work intake, so the queue keeps processing without
// it. The one hard requirement is a thought system; a navi that hands
// the connector no queue is an authoring mistake.
type Connector struct {
	addr      string
	collector *Collector

	ln  net.Listener
	srv *http.Server
}

// New builds a metrics connector serving on addr (e.g. ":9090").
func New(addr string) *Connector {
	return &Connector{addr: addr, collector: NewCollector()}
}

// Name identifies the connector in diagnostics and the health view.
func (c *Connector) Name() string { return "metrics" }

// Addr reports the configured listen address.
func (c *Connector) Addr() string { return c.addr }

// BoundAddr reports the address actually bound after Start, or "" when
// the listener is not serving. Useful when the configured address used
// port 0.
func (c *Connector) BoundAddr() string {
	if c.ln == nil {
		return ""
	}
	return c.ln.Addr().String()
}

// Collector exposes the underlying collector (tests and embedding).
func (c *Connector) Collector() *Collector { return c.collector }

// Start wires the collector to the thought system and serves /metrics.
// A bind failure returns an error with the connector released (the
// collector's OnEvent subscription cannot be removed, so nothing is
// subscribed until the listener is up).
func (c *Connector) Start(ctx context.Context, deps connector.Deps) error {
	if deps.ThoughtSystem == nil {
		return fmt.Errorf("metrics: thought system is required")
	}
	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		// Return the listener's own error ("listen tcp :9090: bind:
		// address already in use"): the health view already names the
		// connector, so wrapping it again only repeats "listen".
		return err
	}
	c.collector.WireQueue(deps.ThoughtSystem)
	c.ln = ln
	mux := http.NewServeMux()
	mux.Handle("/metrics", c.collector.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	c.srv = srv
	go func() {
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			if deps.Log != nil {
				deps.Log.Error("metrics: serve error", "addr", c.addr, "err", serr)
			}
		}
	}()
	return nil
}

// Stop shuts the HTTP server down and closes the listener.
func (c *Connector) Stop() error {
	if c.srv == nil {
		return nil
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.srv.Shutdown(shutCtx)
	c.srv = nil
	c.ln = nil
	return err
}

// Compile-time check.
var _ connector.Connector = (*Connector)(nil)
