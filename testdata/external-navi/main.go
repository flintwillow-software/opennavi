// Command external-navi is a synthetic OUT-OF-REPO navi (ADR 0002, ADR 0004):
// application code that imports the public composition root (navi) and
// component packages, and composes a third-party component module.
//
// CI builds and runs it to prove a navi authored outside this repository
// needs only the public surface — no internal/, no in-tree special casing.
// See docs/architecture/extension-model.md.
package main

import (
	"fmt"
	"os"

	component "example.com/external-component"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/skill"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "external-navi:", err)
		os.Exit(1)
	}
}

func run() error {
	n := navi.New("external-navi",
		navi.WithVersion("0.1.0"),
		navi.WithPersonality("You are external-navi, a synthetic out-of-repo composition proof."),
	)

	if err := n.Skills.Add(skill.Skill{
		Name:        "external_orientation",
		Description: "Synthetic out-of-repo skill.",
		Content:     "Orient yourself before acting.",
		Version:     "0.1.0",
	}); err != nil {
		return err
	}

	// A tool, a connector, and a portal authored in another module.
	if err := n.Tools.Add(component.PingTool()); err != nil {
		return err
	}
	if err := n.Connectors.Add(&component.Watcher{}); err != nil {
		return err
	}
	if err := n.Portals.Add(&component.Dashboard{}); err != nil {
		return err
	}

	// One model, marked default (ADR 0009): the compiled artifact must be
	// self-describing even though Run is never reached here.
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		return err
	}

	// Validate is the cross-cutting check Run performs, exposed for
	// authors who want to verify a composition without running it.
	if err := n.Validate(); err != nil {
		return err
	}

	fmt.Printf("external navi %q validated: %d skill(s), %d tool(s), %d model(s), %d connector(s), %d portal(s)\n",
		n.Name(), n.Skills.Len(), n.Tools.Len(), n.Models.Len(), n.Connectors.Len(), n.Portals.Len())
	return nil
}
