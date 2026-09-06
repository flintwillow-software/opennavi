package main

import (
	"os"
	"strings"
	"testing"
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
