package defaults

import "testing"

func TestDefaultsAnchored(t *testing.T) {
	// Guard against silent drift: these are the baked-in baseline of
	// the merge chain (config.DefaultRuntimeConfig mirrors them).
	if DefaultSessionDBPath != "./navi.db" {
		t.Fatalf("SessionDBPath = %q", DefaultSessionDBPath)
	}
	if DefaultPrimaryProvider != "ollama" {
		t.Fatalf("PrimaryProvider = %q", DefaultPrimaryProvider)
	}
	if DefaultEmbedModel != "nomic-embed-text" {
		t.Fatalf("EmbedModel = %q", DefaultEmbedModel)
	}
}
