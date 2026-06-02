// ready-import replays a rudi-export JSONL file into a ready project campfire.
//
// Usage:
//
//	ready-import [--input file.jsonl] [--project-dir .] [--cf-home ~/.campfire] [--dry-run]
//
// Each rudi item is replayed as campfire messages:
//   - work:create — creates the item in the ready project
//   - work:update — appends the rudi audit history (with original actor/timestamp)
//   - work:status — closes the item if terminal (done/cancelled/failed)
//   - work:block — wires dependencies (second pass after all items created)
//
// NOTE: original timestamps from rudi history are stored in the message payload
// (import_history field), not as campfire message timestamps. cf 0.14 SendRequest
// has no Timestamp field — see rd item rudi-trl for the upstream feature request.
//
// Idempotent: re-running on an already-imported project skips existing items.
// State is tracked in <project-dir>/.ready/migration-state.json.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/ready/pkg/jsonl"
	"github.com/campfire-net/ready/pkg/state"
)

// rudiItem is a work item exported from rudi by rudi-export.
type rudiItem struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Type        string         `json:"type"`
	Level       string         `json:"level,omitempty"`
	Priority    string         `json:"priority"`
	Status      string         `json:"status"`
	Context     string         `json:"context,omitempty"`
	Project     string         `json:"project,omitempty"`
	CreatedBy   string         `json:"created_by,omitempty"`
	Responsible string         `json:"responsible,omitempty"`
	CreatedAt   string         `json:"created_at,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
	ETA         string         `json:"eta,omitempty"`
	ParentID    string         `json:"parent_id,omitempty"`
	Blocks      []string       `json:"blocks,omitempty"`
	BlockedBy   []string       `json:"blocked_by,omitempty"`
	History     []rudiHistory  `json:"history,omitempty"`
}

// rudiHistory is a single audit trail entry from rudi.
type rudiHistory struct {
	Timestamp  string `json:"timestamp"`
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
	ChangedBy  string `json:"changed_by"`
	Note       string `json:"note,omitempty"`
}

// createPayload mirrors the work:create convention message payload.
type createPayload struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Context  string `json:"context,omitempty"`
	Type     string `json:"type"`
	Level    string `json:"level,omitempty"`
	Project  string `json:"project,omitempty"`
	For      string `json:"for"`
	By       string `json:"by,omitempty"`
	Priority string `json:"priority"`
	ParentID string `json:"parent_id,omitempty"`
	ETA      string `json:"eta,omitempty"`
}

// updatePayload mirrors the work:update convention message payload, with
// an extra import_history field for replaying rudi audit history.
type updatePayload struct {
	Target        string              `json:"target"`
	ImportHistory []state.HistoryEntry `json:"import_history,omitempty"`
}

// statusPayload mirrors the work:status convention message payload.
type statusPayload struct {
	Target string `json:"target"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
}

// blockPayload mirrors the work:block convention message payload.
type blockPayload struct {
	BlockerID  string `json:"blocker_id"`
	BlockedID  string `json:"blocked_id"`
	BlockerMsg string `json:"blocker_msg,omitempty"`
	BlockedMsg string `json:"blocked_msg,omitempty"`
}

// migrationState tracks which rudi IDs have been imported, mapping rudi ID →
// ready campfire message ID (msg_id of the work:create message).
type migrationState struct {
	ImportedItems map[string]string `json:"imported_items"` // rudiID → create msgID
}

func main() {
	var (
		inputPath  = flag.String("input", "", "JSONL input file (default: stdin)")
		projectDir = flag.String("project-dir", ".", "ready project directory (must have .campfire/root)")
		cfHome     = flag.String("cf-home", "", "campfire home directory (default: ~/.campfire or CF_HOME)")
		dryRun     = flag.Bool("dry-run", false, "print what would happen without writing")
	)
	flag.Parse()

	// Resolve cfHome.
	cfHomeDir := *cfHome
	if cfHomeDir == "" {
		cfHomeDir = os.Getenv("CF_HOME")
	}
	if cfHomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatalf("cannot determine home directory: %v", err)
		}
		cfHomeDir = filepath.Join(home, ".campfire")
	}

	// Resolve project directory (absolute).
	projDir, err := filepath.Abs(*projectDir)
	if err != nil {
		fatalf("resolving project dir: %v", err)
	}

	// Read campfire ID from .campfire/root.
	campfireID, err := readCampfireRoot(projDir)
	if err != nil {
		fatalf("reading project campfire: %v", err)
	}

	// Open input.
	var r io.Reader = os.Stdin
	if *inputPath != "" {
		f, err := os.Open(*inputPath)
		if err != nil {
			fatalf("opening input: %v", err)
		}
		defer f.Close()
		r = f
	}

	// Load migration state.
	statePath := filepath.Join(projDir, ".ready", "migration-state.json")
	ms, err := loadMigrationState(statePath)
	if err != nil {
		fatalf("loading migration state: %v", err)
	}

	// Parse all items first so we can do two passes.
	var items []rudiItem
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item rudiItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping malformed line: %v\nline: %s\n", err, line)
			continue
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		fatalf("reading input: %v", err)
	}

	if *dryRun {
		fmt.Printf("dry-run: would import %d items into campfire %s\n", len(items), campfireID[:8])
		for _, item := range items {
			if _, ok := ms.ImportedItems[item.ID]; ok {
				fmt.Printf("  SKIP %s (already imported)\n", item.ID)
			} else {
				fmt.Printf("  IMPORT %s — %s\n", item.ID, item.Title)
			}
		}
		return
	}

	// Initialize campfire client.
	client, _, err := protocol.Init(cfHomeDir)
	if err != nil {
		fatalf("initializing campfire client: %v", err)
	}
	defer client.Close()

	// Open mutations.jsonl writer — rd list reads this file first.
	jw := jsonl.NewWriter(filepath.Join(projDir, ".ready", "mutations.jsonl"))

	// Build a map of item ID → rudi item for dep wiring.
	itemByID := make(map[string]rudiItem, len(items))
	for _, it := range items {
		itemByID[it.ID] = it
	}

	// Pass 1: create items and replay history.
	created := 0
	skipped := 0
	for _, item := range items {
		if _, ok := ms.ImportedItems[item.ID]; ok {
			skipped++
			continue
		}

		msgID, err := importItem(client, campfireID, item, jw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error importing %s: %v\n", item.ID, err)
			continue
		}

		ms.ImportedItems[item.ID] = msgID
		created++

		// Save state after each item for crash recovery.
		if err := saveMigrationState(statePath, ms); err != nil {
			fmt.Fprintf(os.Stderr, "warning: saving migration state: %v\n", err)
		}
	}

	// Pass 2: wire dependencies.
	// Only wire deps between items we just created (skip if blocker not imported).
	blocked := 0
	for _, item := range items {
		createMsgID, ok := ms.ImportedItems[item.ID]
		if !ok {
			continue
		}
		for _, blockerID := range item.BlockedBy {
			blockerMsgID, ok := ms.ImportedItems[blockerID]
			if !ok {
				fmt.Fprintf(os.Stderr, "warning: dep %s → %s: blocker not imported, skipping\n", item.ID, blockerID)
				continue
			}
			if err := wireBlock(client, campfireID, blockerID, blockerMsgID, item.ID, createMsgID, jw); err != nil {
				fmt.Fprintf(os.Stderr, "error wiring dep %s → %s: %v\n", blockerID, item.ID, err)
				continue
			}
			blocked++
		}
	}

	fmt.Fprintf(os.Stderr, "import complete: %d created, %d skipped, %d deps wired\n", created, skipped, blocked)
}

// importItem creates a single rudi item in the ready project campfire and
// replays its history. Returns the campfire message ID of the work:create message.
func importItem(client *protocol.Client, campfireID string, item rudiItem, jw *jsonl.Writer) (string, error) {
	// Build create payload.
	forParty := item.Responsible
	if forParty == "" {
		forParty = item.CreatedBy
	}
	if forParty == "" {
		forParty = client.PublicKeyHex()
	}

	eta := item.ETA
	if eta == "" && item.CreatedAt != "" {
		// Default ETA from priority if not set.
		eta = etaFromPriority(item.Priority, item.CreatedAt)
	}

	cp := createPayload{
		ID:       item.ID,
		Title:    item.Title,
		Context:  item.Context,
		Type:     normalizeType(item.Type),
		Level:    item.Level,
		Project:  item.Project,
		For:      forParty,
		Priority: strings.ToLower(item.Priority),
		ParentID: item.ParentID,
		ETA:      eta,
	}

	payload, err := json.Marshal(cp)
	if err != nil {
		return "", fmt.Errorf("encoding create payload: %w", err)
	}

	tags := []string{
		"work:create",
		"work:type:" + cp.Type,
		"work:priority:" + cp.Priority,
	}
	if cp.Project != "" {
		tags = append(tags, "work:project:"+cp.Project)
	}
	if cp.Level != "" {
		tags = append(tags, "work:level:"+cp.Level)
	}

	createMsg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    payload,
		Tags:       tags,
	})
	if err != nil {
		return "", fmt.Errorf("sending work:create: %w", err)
	}
	appendMutation(jw, createMsg.ID, campfireID, client.PublicKeyHex(), payload, tags, nil)

	// Replay history as a single work:update with import_history.
	// Skip the first entry if it's just "created" (state already reflects that).
	importHistory := buildImportHistory(item.History)
	if len(importHistory) > 0 {
		up := updatePayload{
			Target:        createMsg.ID,
			ImportHistory: importHistory,
		}
		upPayload, err := json.Marshal(up)
		if err != nil {
			return createMsg.ID, fmt.Errorf("encoding update payload: %w", err)
		}
		upTags := []string{"work:update"}
		upAnts := []string{createMsg.ID}
		upMsg, err := client.Send(protocol.SendRequest{
			CampfireID:  campfireID,
			Payload:     upPayload,
			Tags:        upTags,
			Antecedents: upAnts,
		})
		if err != nil {
			return createMsg.ID, fmt.Errorf("sending import history: %w", err)
		}
		appendMutation(jw, upMsg.ID, campfireID, client.PublicKeyHex(), upPayload, upTags, upAnts)
	}

	// Close the item if it's in a terminal status.
	finalStatus := strings.ToLower(item.Status)
	switch finalStatus {
	case "done", "cancelled", "failed":
		sp := statusPayload{
			Target: createMsg.ID,
			To:     finalStatus,
		}
		// Find the closing reason from history.
		for i := len(item.History) - 1; i >= 0; i-- {
			h := item.History[i]
			if strings.ToLower(h.ToStatus) == finalStatus && h.Note != "" {
				sp.Reason = h.Note
				break
			}
		}
		spPayload, err := json.Marshal(sp)
		if err != nil {
			return createMsg.ID, fmt.Errorf("encoding status payload: %w", err)
		}
		spTags := []string{"work:status", "work:status:" + finalStatus}
		spAnts := []string{createMsg.ID}
		spMsg, err := client.Send(protocol.SendRequest{
			CampfireID:  campfireID,
			Payload:     spPayload,
			Tags:        spTags,
			Antecedents: spAnts,
		})
		if err != nil {
			return createMsg.ID, fmt.Errorf("sending work:status %s: %w", finalStatus, err)
		}
		appendMutation(jw, spMsg.ID, campfireID, client.PublicKeyHex(), spPayload, spTags, spAnts)
	}

	return createMsg.ID, nil
}

// wireBlock sends a work:block message to record a dependency.
func wireBlock(client *protocol.Client, campfireID, blockerID, blockerMsgID, blockedID, blockedMsgID string, jw *jsonl.Writer) error {
	bp := blockPayload{
		BlockerID:  blockerID,
		BlockedID:  blockedID,
		BlockerMsg: blockerMsgID,
		BlockedMsg: blockedMsgID,
	}
	payload, err := json.Marshal(bp)
	if err != nil {
		return fmt.Errorf("encoding block payload: %w", err)
	}
	tags := []string{"work:block"}
	ants := []string{blockerMsgID, blockedMsgID}
	msg, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     payload,
		Tags:        tags,
		Antecedents: ants,
	})
	if err != nil {
		return err
	}
	appendMutation(jw, msg.ID, campfireID, client.PublicKeyHex(), payload, tags, ants)
	return nil
}

// buildImportHistory converts rudi history entries to state.HistoryEntry for
// the import_history field. All entries are included to preserve the full audit
// trail from rudi.
func buildImportHistory(hist []rudiHistory) []state.HistoryEntry {
	result := make([]state.HistoryEntry, 0, len(hist))
	for _, h := range hist {
		result = append(result, state.HistoryEntry{
			Timestamp:  h.Timestamp,
			FromStatus: h.FromStatus,
			ToStatus:   h.ToStatus,
			ChangedBy:  h.ChangedBy,
			Note:       h.Note,
		})
	}
	return result
}

// normalizeType maps rudi item types to ready convention types.
// Rudi and ready share the same type vocabulary; unknown types default to "task".
func normalizeType(t string) string {
	switch strings.ToLower(t) {
	case "task", "decision", "review", "reminder", "deadline", "prep", "message", "directive":
		return strings.ToLower(t)
	default:
		return "task"
	}
}

// etaFromPriority derives a default ETA offset from priority.
// Base time is the item's created_at timestamp.
func etaFromPriority(priority, createdAt string) string {
	t := time.Now()
	if createdAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			t = parsed
		}
	}
	switch strings.ToLower(priority) {
	case "p0":
		return t.UTC().Format(time.RFC3339)
	case "p1":
		return t.Add(4 * time.Hour).UTC().Format(time.RFC3339)
	case "p2":
		return t.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	default: // p3
		return t.Add(72 * time.Hour).UTC().Format(time.RFC3339)
	}
}

// readCampfireRoot reads the campfire ID from <dir>/.campfire/root.
func readCampfireRoot(dir string) (string, error) {
	rootPath := filepath.Join(dir, ".campfire", "root")
	data, err := os.ReadFile(rootPath)
	if err != nil {
		return "", fmt.Errorf("reading .campfire/root (run `rd init` first): %w", err)
	}
	id := strings.TrimSpace(string(data))
	if len(id) != 64 {
		return "", fmt.Errorf(".campfire/root contains invalid campfire ID (want 64 chars, got %d)", len(id))
	}
	return id, nil
}

// loadMigrationState reads migration state from disk.
func loadMigrationState(path string) (*migrationState, error) {
	ms := &migrationState{ImportedItems: make(map[string]string)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ms, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading migration state: %w", err)
	}
	if err := json.Unmarshal(data, ms); err != nil {
		return nil, fmt.Errorf("parsing migration state: %w", err)
	}
	if ms.ImportedItems == nil {
		ms.ImportedItems = make(map[string]string)
	}
	return ms, nil
}

// saveMigrationState writes migration state to disk.
func saveMigrationState(path string, ms *migrationState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ms, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// appendMutation writes a MutationRecord to mutations.jsonl so that rd list
// and other rd commands can read the imported items without needing the campfire store.
func appendMutation(jw *jsonl.Writer, msgID, campfireID, sender string, payload []byte, tags, antecedents []string) {
	op := ""
	for _, t := range tags {
		if strings.HasPrefix(t, "work:") && strings.Count(t, ":") == 1 {
			op = t
			break
		}
	}
	rec := jsonl.MutationRecord{
		MsgID:       msgID,
		CampfireID:  campfireID,
		Timestamp:   time.Now().UnixNano(),
		Operation:   op,
		Payload:     json.RawMessage(payload),
		Tags:        tags,
		Sender:      sender,
		Antecedents: antecedents,
	}
	if err := jw.Append(rec); err != nil {
		fmt.Fprintf(os.Stderr, "warning: writing mutation record: %v\n", err)
	}
}

// generateUUID returns a random UUID v4 (used for internal correlation only).
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
