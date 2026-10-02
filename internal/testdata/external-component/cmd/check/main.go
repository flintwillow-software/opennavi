// Command check exercises the components this module publishes, so CI
// runs the same declaration-time code paths a navi would. It is not part
// of the module's public surface; the component package is.
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/portal"

	component "example.com/external-component"
)

func main() {
	t := component.PingTool()
	out, err := t.Handler(context.Background(), map[string]any{})
	if err != nil {
		panic(err)
	}
	fmt.Printf("external tool %s -> %s\n", t.Def.Name, out)

	// The interface assertions prove the module satisfies the interfaces
	// structurally, without importing navi.
	var c connector.Connector = &component.Watcher{}
	var p portal.Portal = &component.Dashboard{}

	// The datastore view is part of the component's public surface: the
	// factory is asserted to type-check against the contract, which is what
	// keeps the external-consumer guard honest about datastore being a
	// contract package.
	var _ = component.TodoCount

	// The portal must fail closed for an identity that never attached.
	if err := p.Steer("anon", "t1", "hi"); !errors.Is(err, portal.ErrUnauthorized) {
		panic(fmt.Sprintf("dashboard must fail closed, got %v", err))
	}
	fmt.Printf("external component satisfies connector.Connector (%T) and portal.Portal (%T); fail-closed ok\n", c, p)
}
