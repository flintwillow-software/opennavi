package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExportMarkdown writes a scope's documents/entries as markdown files,
// preserving the spec's human-editable story (§02 ownership): user/navi
// scopes get one file per subject (`<subject>.md`); the memories scope
// gets date-organized files (`YYYY-MM-DD.md`). Returns the paths
// written.
func (s *Store) ExportMarkdown(scope Scope, dir string) ([]string, error) {
	if !scope.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrInvalidScope, scope)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("memory: export mkdir %s: %w", dir, err)
	}

	var paths []string
	switch scope {
	case ScopeUser, ScopeNavi:
		subjects, err := s.ListSubjects(scope)
		if err != nil {
			return nil, err
		}
		for _, subj := range subjects {
			e, err := s.Get(scope, subj)
			if err != nil {
				return nil, err
			}
			path := filepath.Join(dir, e.Subject+".md")
			if err := os.WriteFile(path, []byte(renderDocument(e)), 0o644); err != nil {
				return nil, fmt.Errorf("memory: export %s: %w", path, err)
			}
			paths = append(paths, path)
		}
		return paths, nil

	case ScopeMemories:
		// Unbounded: export must never silently truncate archive history.
		all, err := s.listEntriesAll()
		if err != nil {
			return nil, err
		}
		// Group by date into one file per day.
		byDate := map[string][]Entry{}
		var dates []string
		for _, e := range all {
			date := e.UpdatedAt.UTC().Format("2006-01-02")
			if _, seen := byDate[date]; !seen {
				dates = append(dates, date)
			}
			byDate[date] = append(byDate[date], e)
		}
		for _, date := range dates {
			var sb strings.Builder
			fmt.Fprintf(&sb, "# Memories: %s\n\n", date)
			for _, e := range byDate[date] {
				sb.WriteString(renderEntry(e))
				sb.WriteString("\n")
			}
			path := filepath.Join(dir, date+".md")
			if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
				return nil, fmt.Errorf("memory: export %s: %w", path, err)
			}
			paths = append(paths, path)
		}
		return paths, nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidScope, scope)
	}
}

// renderDocument renders a user/navi document with metadata followed by
// the content.
func renderDocument(e Entry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s: %s\n\n", scopeTitle(e.Scope), e.Subject)
	fmt.Fprintf(&sb, "Updated: %s\n\n", e.UpdatedAt.UTC().Format(time.RFC3339))
	if len(e.Tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n\n", strings.Join(e.Tags, ", "))
	}
	sb.WriteString(e.Content)
	if !strings.HasSuffix(sb.String(), "\n") {
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderEntry renders one memories entry with frontmatter metadata
// (spec §04: date, source, tags, importance, expires).
func renderEntry(e Entry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "---\ndate: %s\nsource: %s\nimportance: %.2f\n",
		e.CreatedAt.UTC().Format(time.RFC3339), e.Source, e.Importance)
	if len(e.Tags) > 0 {
		fmt.Fprintf(&sb, "tags: [%s]\n", strings.Join(e.Tags, ", "))
	}
	if e.ExpiresAt != nil {
		fmt.Fprintf(&sb, "expires: %s\n", e.ExpiresAt.UTC().Format(time.RFC3339))
	} else {
		sb.WriteString("expires: null\n")
	}
	sb.WriteString("---\n\n")
	if e.Subject != "" {
		fmt.Fprintf(&sb, "# %s\n\n", e.Subject)
	}
	sb.WriteString(e.Content)
	if !strings.HasSuffix(sb.String(), "\n") {
		sb.WriteString("\n")
	}
	return sb.String()
}

func scopeTitle(scope Scope) string {
	switch scope {
	case ScopeUser:
		return "User"
	case ScopeNavi:
		return "Navi"
	default:
		return "Memory"
	}
}
