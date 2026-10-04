package fs

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
)

// The transcript is the body of thought.md, rendered as a readable
// conversation with one HTML comment per entry carrying the fields that would
// otherwise be lost:
//
//	<!-- navi-entry id=<uuid> time=<rfc3339nano> role=<role> [name=<tool>] [call=<b64>] [input=<b64>] -->
//	the message text
//
// Exactly one separator newline follows each message, so the round-trip is
// lossless without length prefixes. Marker id values are UUIDs, so a message
// that happens to contain the marker prefix cannot be mistaken for the start of
// the next entry.
const entryPrefix = "<!-- navi-entry "
const entryClose = " -->\n"

// renderTranscript renders entries as the readable body of thought.md.
func renderTranscript(entries []datastore.Entry) []byte {
	var b bytes.Buffer
	for _, e := range entries {
		b.WriteString(entryPrefix)
		fmt.Fprintf(&b, "id=%s time=%s role=%s", e.ID, formatTime(e.Time), e.Role)
		if e.ToolName != "" {
			b.WriteString(" name=")
			b.WriteString(e.ToolName)
		}
		if e.ToolCallID != "" {
			b.WriteString(" call=")
			b.WriteString(base64.StdEncoding.EncodeToString([]byte(e.ToolCallID)))
		}
		if len(e.ToolInput) > 0 {
			b.WriteString(" input=")
			b.WriteString(base64.StdEncoding.EncodeToString(e.ToolInput))
		}
		b.WriteString(entryClose)
		b.WriteString(e.Message)
		b.WriteString("\n")
	}
	return b.Bytes()
}

// parseTranscript reads a rendered body back into entries.
func parseTranscript(body []byte) ([]datastore.Entry, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return []datastore.Entry{}, nil
	}
	rest := string(body)
	out := []datastore.Entry{}
	for {
		start := strings.Index(rest, entryPrefix)
		if start < 0 {
			if strings.TrimSpace(rest) != "" {
				return nil, fmt.Errorf("fs: transcript: content outside any entry marker")
			}
			return out, nil
		}
		if strings.TrimSpace(rest[:start]) != "" {
			return nil, fmt.Errorf("fs: transcript: content outside any entry marker")
		}
		closeIdx := strings.Index(rest[start:], entryClose)
		if closeIdx < 0 {
			return nil, fmt.Errorf("fs: transcript: unterminated entry marker")
		}
		attrs := rest[start+len(entryPrefix) : start+closeIdx]
		rest = rest[start+closeIdx+len(entryClose):]

		// The message runs to the next marker, minus the separator newline we
		// wrote after it.
		var message string
		if next := strings.Index(rest, entryPrefix); next >= 0 {
			message, rest = rest[:next], rest[next:]
		} else {
			message, rest = rest, ""
		}
		message = strings.TrimSuffix(message, "\n")

		e, err := parseEntry(attrs, message)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
}

// parseEntry decodes one marker's attributes plus its message.
func parseEntry(attrs, message string) (datastore.Entry, error) {
	var e datastore.Entry
	e.Message = message
	for _, field := range strings.Fields(attrs) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return e, fmt.Errorf("fs: transcript: malformed entry attribute %q", field)
		}
		switch key {
		case "id":
			e.ID = value
		case "role":
			e.Role = datastore.Role(value)
		case "name":
			e.ToolName = value
		case "time":
			t, err := parseTime(value)
			if err != nil {
				return e, err
			}
			e.Time = t
		case "call":
			decoded, err := decodeBase64("call", value)
			if err != nil {
				return e, err
			}
			e.ToolCallID = string(decoded)
		case "input":
			decoded, err := decodeBase64("input", value)
			if err != nil {
				return e, err
			}
			e.ToolInput = decoded
		default:
			return e, fmt.Errorf("fs: transcript: unknown entry attribute %q", key)
		}
	}
	if e.ID == "" {
		return e, fmt.Errorf("fs: transcript: entry marker without id")
	}
	return e, nil
}

func decodeBase64(field, value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("fs: transcript: entry %s is not valid base64: %w", field, err)
	}
	return decoded, nil
}

// latestEntryTime is the transcript's update time, stored in frontmatter so
// TranscriptInfo never has to read the body.
func latestEntryTime(entries []datastore.Entry) time.Time {
	var latest time.Time
	for _, e := range entries {
		if e.Time.After(latest) {
			latest = e.Time
		}
	}
	return latest
}
