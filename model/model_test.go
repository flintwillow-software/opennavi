package model

import "testing"

func TestRegistryRegisterFind(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Provider{Name: "ollama", Model: "qwen3.8", Endpoint: "http://localhost:11434"}); err != nil {
		t.Fatal(err)
	}
	p, ok := r.Find("ollama")
	if !ok || p.Name != "ollama" || p.Model != "qwen3.8" {
		t.Fatalf("find failed: %+v", p)
	}
	if _, ok := r.Find("missing"); ok {
		t.Error("unknown provider found")
	}
	if err := r.Register(Provider{Name: "", Endpoint: "x"}); err == nil {
		t.Error("nameless provider accepted")
	}
	if err := r.Register(Provider{Name: "ollama", Model: "new"}); err != nil {
		t.Fatalf("re-register should replace: %v", err)
	}
	p, _ = r.Find("ollama")
	if p.Model != "new" {
		t.Errorf("replacement not applied")
	}
}

func TestModelDescriptor(t *testing.T) {
	m := Model{Name: "fast", ProviderName: "ollama", Description: "cheap fast model"}
	if m.Name != "fast" || m.ProviderName != "ollama" {
		t.Fatalf("model descriptor wrong: %+v", m)
	}
}
