package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/3dl-dev/ready/pkg/state"
)

// logEntryKind classifies a unified-timeline entry by its origin.
const (
	logKindStatus   = "status"   // a status-transition entry from item.History
	logKindProgress = "progress" // a progress note appended to item.Context by `rd progress`
)

// logEntry is one line in the unified timeline built by `rd log <id>`.
// It merges two previously-split sources — status transitions (item.History) and
// progress notes (timestamped blocks in item.Context) — into a single record.
type logEntry struct {
	// Timestamp is the raw timestamp string as stored (RFC3339 for status entries,
	// "2006-01-02T15:04Z" for progress notes).
	Timestamp string `json:"timestamp"`
	// Kind is logKindStatus or logKindProgress.
	Kind string `json:"kind"`
	// From/To are the status transition (status entries only).
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// By is the actor that made the change (status entries only).
	By string `json:"by,omitempty"`
	// Note is the human-readable note (transition reason, or progress text).
	Note string `json:"note,omitempty"`

	// at is the parsed sort key. Unexported so it never appears in --json output.
	at time.Time
}

// progressNotePattern matches the leading "[timestamp] " prefix that `rd progress`
// writes when it appends a note to the context field. The timestamp layout is
// "2006-01-02T15:04Z" (minute precision, always UTC) — see progressCmd in aliases.go.
var progressNotePattern = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}Z)\]\s?`)

// progressNoteLayout is the time layout `rd progress` uses for its note prefix.
const progressNoteLayout = "2006-01-02T15:04Z"

// parseProgressNotes extracts timestamped progress notes from an item's context.
//
// `rd progress` appends notes as "\n\n[<ts>] <text>" blocks. The very first block
// (the item's original description) carries no timestamp prefix and is NOT a
// progress note, so it is skipped. A block that lacks a timestamp prefix but
// follows a timestamped block is treated as a continuation of that note (progress
// text may itself contain blank lines).
func parseProgressNotes(context string) []logEntry {
	if strings.TrimSpace(context) == "" {
		return nil
	}
	blocks := strings.Split(context, "\n\n")
	var entries []logEntry
	for _, block := range blocks {
		m := progressNotePattern.FindStringSubmatch(block)
		if m == nil {
			// Not a timestamped block. If it follows a progress note, append it as a
			// continuation; otherwise it is leading description text — ignore.
			if len(entries) > 0 {
				last := &entries[len(entries)-1]
				if last.Note != "" {
					last.Note += "\n\n" + block
				} else {
					last.Note = block
				}
			}
			continue
		}
		ts := m[1]
		text := strings.TrimSpace(block[len(m[0]):])
		at, _ := time.Parse(progressNoteLayout, ts)
		entries = append(entries, logEntry{
			Timestamp: ts,
			Kind:      logKindProgress,
			Note:      text,
			at:        at,
		})
	}
	return entries
}

// buildTimeline merges an item's status history and progress notes into one
// chronologically-ordered slice. Status entries and progress notes that share a
// timestamp keep a stable order (status first, then progress) so output is
// deterministic. Entries with unparseable timestamps sort to the front but retain
// their relative insertion order.
func buildTimeline(item *state.Item) []logEntry {
	var entries []logEntry
	for _, h := range item.History {
		at, _ := time.Parse(time.RFC3339, h.Timestamp)
		entries = append(entries, logEntry{
			Timestamp: h.Timestamp,
			Kind:      logKindStatus,
			From:      h.FromStatus,
			To:        h.ToStatus,
			By:        h.ChangedBy,
			Note:      h.Note,
			at:        at,
		})
	}
	entries = append(entries, parseProgressNotes(item.Context)...)

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].at.Before(entries[j].at)
	})
	return entries
}

// renderTimeline is the shared entry point for `rd log <id>`. It resolves the
// item, builds the unified timeline, and writes it as JSON or a human table.
func renderTimeline(itemID string) error {
	item, err := itemByID(itemID)
	if err != nil {
		return err
	}
	entries := buildTimeline(item)

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if entries == nil {
			entries = []logEntry{}
		}
		return enc.Encode(entries)
	}

	fmt.Printf("Timeline for %s — %s\n\n", item.ID, item.Title)
	if len(entries) == 0 {
		fmt.Println("  (no history or progress notes)")
		return nil
	}
	for _, e := range entries {
		switch e.Kind {
		case logKindStatus:
			note := ""
			if e.Note != "" {
				note = " — " + e.Note
			}
			by := ""
			if e.By != "" {
				by = " by " + e.By
			}
			fmt.Printf("  [%s] status: %s → %s%s%s\n", e.Timestamp, e.From, e.To, by, note)
		case logKindProgress:
			fmt.Printf("  [%s] progress: %s\n", e.Timestamp, e.Note)
		}
	}
	return nil
}
