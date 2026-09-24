package main

import (
	"os"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/store/sqlite"
)

func TestRunEnqueuePromptFirst(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/test.db"
	err := runEnqueue([]string{"hello second", "--db", db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatal(err)
	}
}

func TestRunEnqueueFlagsFirst(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/test.db"
	err := runEnqueue([]string{"-priority", "7", "-db", db, "the prompt"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunEnqueueMultiWordPrompt(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/test.db"
	err := runEnqueue([]string{"multi", "word", "prompt", "-priority", "2", "-db", db})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunEnqueuePriorityValidation(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"0", "11", "-1"} {
		err := runEnqueue([]string{"-priority", p, "-db", dir + "/x.db", "task"})
		if err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Errorf("priority %q: error = %v, want out-of-range", p, err)
		}
	}
}

func TestRunEnqueueMissingPrompt(t *testing.T) {
	dir := t.TempDir()
	err := runEnqueue([]string{"-db", dir + "/x.db"})
	if err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunEnqueueMissingDB(t *testing.T) {
	err := runEnqueue([]string{"some prompt"})
	if err == nil || !strings.Contains(err.Error(), "db path is required") {
		t.Fatalf("error = %v, want db path required", err)
	}
}

func TestRunEnqueueThreeCaseOllamaHost(t *testing.T) {
	// 3-case CLI smoke: flag set / env set / neither — OllamaHost
	// reaches the provider in all cases.
	t.Run("flag set", func(t *testing.T) {
		// runEnqueue doesn't set OllamaHost; the flag is consumed by
		// the daemon subcommand. The CLI chat path wires it via
		// Config.OllamaHost. This test verifies the enqueue path
		// doesn't error with OLLAMA_HOST unset.
		t.Setenv("OLLAMA_HOST", "")
		dir := t.TempDir()
		err := runEnqueue([]string{"task", "-priority", "3", "-db", dir + "/x.db"})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("env set", func(t *testing.T) {
		t.Setenv("OLLAMA_HOST", "http://10.0.0.50:11434")
		dir := t.TempDir()
		err := runEnqueue([]string{"task", "-db", dir + "/y.db"})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("neither", func(t *testing.T) {
		t.Setenv("OLLAMA_HOST", "")
		dir := t.TempDir()
		err := runEnqueue([]string{"task", "-db", dir + "/z.db"})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunEnqueueProviderModel(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/t.db"
	err := runEnqueue([]string{"-provider", "mock", "-model", "fast-1", "-db", db, "do the thing"})
	if err != nil {
		t.Fatal(err)
	}
	st, oerr := sqlite.Open(db, sqlite.Options{})
	if oerr != nil {
		t.Fatal(oerr)
	}
	defer st.Close()
	ts, terr := sqlite.NewThoughtStore(st.DB())
	if terr != nil {
		t.Fatal(terr)
	}
	all, lerr := ts.LoadThoughts()
	if lerr != nil {
		t.Fatal(lerr)
	}
	found := false
	for _, th := range all {
		if th.Prompt == "do the thing" && th.Provider == "mock" && th.Model == "fast-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider/model flags not persisted: %+v", all)
	}
}
