package inmem

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/datastore"
)

type invalidTodoStatusError struct {
	naviID string
	index  int
	status datastore.TodoStatus
}

func (e *invalidTodoStatusError) Error() string {
	return fmt.Sprintf("inmem: todos for %s: item %d has invalid status %q", e.naviID, e.index, e.status)
}

func notFoundTodo(naviID, id string) error {
	return fmt.Errorf("inmem: todos for %s: %w (id %s)", naviID, datastore.ErrTodoNotFound, id)
}

func notFoundNote(naviID, id string) error {
	return fmt.Errorf("inmem: notes for %s: %w (id %s)", naviID, datastore.ErrNoteNotFound, id)
}

func notFoundMemory(id string) error {
	return fmt.Errorf("inmem: memory %s: %w", id, datastore.ErrMemoryNotFound)
}
