package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/ready/pkg/state"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// seedRec is one on-disk mutation record for the campfire/JSONL backend — the
// exact shape pkg/state's DeriveFromJSONLWithCampfire replays. It is the
// campfire-backed SOURCE the top-level `rd migrate` re-emits and `rd migrate
// --parity` verifies against, in an isolated clone with NO real board.
type seedRec struct {
	MsgID       string          `json:"msg_id"`
	CampfireID  string          `json:"campfire_id"`
	Timestamp   int64           `json:"timestamp"`
	Operation   string          `json:"operation"`
	Payload     json.RawMessage `json:"payload"`
	Tags        []string        `json:"tags"`
	Sender      string          `json:"sender"`
	Antecedents []string        `json:"antecedents,omitempty"`
}

// seedCampfireBoard writes a MULTI-FIELD, MULTI-HISTORY campfire item set into
// <dir>/.ready/mutations.jsonl — the campfire path setupNostrCmdTest exposes as
// the default (campfire-backed) read surface. Unlike writeCreateMutations (which
// only emits work:create → status=inbox, history len 1), this seeds a full
// lifecycle: create → status → claim (assignee) → block (deps) → close (reason),
// so the resulting board carries varied title/status/priority/type/deps/assignee
// and history depth > 1. That is exactly the surface a real board presents to
// `rd migrate`, so parity is a real item-by-item FIELD comparison here, not a
// count over trivial single-entry items.
func seedCampfireBoard(t *testing.T, dir string) {
	t.Helper()
	cf := strings.Repeat("ab", 32)
	base := int64(1700000000000000000)
	var recs []seedRec
	nextTS := func(i int) int64 { return base + int64(i)*1_000_000_000 }
	payload := func(v map[string]any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		return json.RawMessage(b)
	}
	add := func(r seedRec) { recs = append(recs, r) }

	// --- ready-mig01: task, p1, active, claimed (assignee), 3 history entries ---
	add(seedRec{MsgID: "msg-mig01-create", CampfireID: cf, Timestamp: nextTS(0), Operation: "work:create", Sender: "human",
		Payload: payload(map[string]any{"id": "ready-mig01", "title": "First migrated item", "type": "task", "for": "alice@test.dev", "priority": "p1", "context": "primary work item"}),
		Tags:    []string{"work:create"}})
	add(seedRec{MsgID: "msg-mig01-status", CampfireID: cf, Timestamp: nextTS(1), Operation: "work:status", Sender: "human",
		Payload: payload(map[string]any{"target": "msg-mig01-create", "to": "active"}),
		Tags:    []string{"work:status"}, Antecedents: []string{"msg-mig01-create"}})
	add(seedRec{MsgID: "msg-mig01-claim", CampfireID: cf, Timestamp: nextTS(2), Operation: "work:claim", Sender: "agent-x@test.dev",
		Payload: payload(map[string]any{"target": "msg-mig01-create"}),
		Tags:    []string{"work:claim"}, Antecedents: []string{"msg-mig01-create"}})

	// --- ready-mig02: bug, p0, blocked by ready-mig01 (deps) ---
	add(seedRec{MsgID: "msg-mig02-create", CampfireID: cf, Timestamp: nextTS(3), Operation: "work:create", Sender: "human",
		Payload: payload(map[string]any{"id": "ready-mig02", "title": "Second migrated item", "type": "bug", "for": "bob@test.dev", "priority": "p0"}),
		Tags:    []string{"work:create"}})
	add(seedRec{MsgID: "msg-mig02-block", CampfireID: cf, Timestamp: nextTS(4), Operation: "work:block", Sender: "human",
		Payload: payload(map[string]any{"blocker_id": "ready-mig01", "blocked_id": "ready-mig02", "blocker_msg": "msg-mig01-create", "blocked_msg": "msg-mig02-create"}),
		Tags:    []string{"work:block"}, Antecedents: []string{"msg-mig02-create"}})

	// --- ready-mig03: task, p2, closed done (terminal, close reason in history) ---
	add(seedRec{MsgID: "msg-mig03-create", CampfireID: cf, Timestamp: nextTS(5), Operation: "work:create", Sender: "human",
		Payload: payload(map[string]any{"id": "ready-mig03", "title": "Third migrated item", "type": "task", "for": "carol@test.dev", "priority": "p2"}),
		Tags:    []string{"work:create"}})
	add(seedRec{MsgID: "msg-mig03-status", CampfireID: cf, Timestamp: nextTS(6), Operation: "work:status", Sender: "human",
		Payload: payload(map[string]any{"target": "msg-mig03-create", "to": "active"}),
		Tags:    []string{"work:status"}, Antecedents: []string{"msg-mig03-create"}})
	add(seedRec{MsgID: "msg-mig03-close", CampfireID: cf, Timestamp: nextTS(7), Operation: "work:close", Sender: "human",
		Payload: payload(map[string]any{"target": "msg-mig03-create", "resolution": "done", "reason": "shipped and verified"}),
		Tags:    []string{"work:close", "work:resolution:done"}, Antecedents: []string{"msg-mig03-create"}})

	var buf strings.Builder
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal seed record: %v", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	path := filepath.Join(dir, ".ready", "mutations.jsonl")
	if err := os.WriteFile(path, []byte(buf.String()), 0600); err != nil {
		t.Fatalf("write mutations.jsonl: %v", err)
	}
}

// resetTopMigrateFlags returns the top-level `rd migrate` command flags to their
// declared defaults between sub-runs (cobra flags are process-global mutable
// state shared across table cases in the same binary).
func resetTopMigrateFlags(t *testing.T) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(migrateCmd.Flags().Set("parity", "false"))
	must(migrateCmd.Flags().Set("local-only", "false"))
	must(migrateCmd.Flags().Set("limit", "0"))
	resetStringSliceFlag(t, migrateCmd, "only")
	must(migrateCmd.Flags().Set("all", "true"))
	must(migrateCmd.Flags().Set("verbose", "false"))
	must(migrateCmd.Flags().Set("sample", "false"))
}

// runTopMigrate runs `rd migrate` (migration mode) with --local-only and returns
// the decoded JSON result plus the RunE error.
func runTopMigrate(t *testing.T) (map[string]any, error) {
	t.Helper()
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()
	resetTopMigrateFlags(t)
	if err := migrateCmd.Flags().Set("local-only", "true"); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdoutPipe(t, func() {
		runErr = migrateCmd.RunE(migrateCmd, nil)
	})
	resetTopMigrateFlags(t)
	if runErr != nil {
		return nil, runErr
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("unmarshal migrate output: %v\noutput:\n%s", err, out)
	}
	return res, nil
}

// runTopParity runs `rd migrate --parity` and returns the decoded ParityReport
// plus the RunE error (a non-nil error IS the CLI's exit-code-1 contract).
func runTopParity(t *testing.T) (rdSync.ParityReport, error) {
	t.Helper()
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()
	resetTopMigrateFlags(t)
	if err := migrateCmd.Flags().Set("parity", "true"); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdoutPipe(t, func() {
		runErr = migrateCmd.RunE(migrateCmd, nil)
	})
	resetTopMigrateFlags(t)
	var rep rdSync.ParityReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rep); err != nil {
		t.Fatalf("unmarshal parity output: %v\noutput:\n%s", err, out)
	}
	return rep, runErr
}

// TestTopLevelMigrateParity_GreenOnSeededBoard is the ground-source gate for
// ready-1c8: it SEEDS a campfire-backed project (no real board in this clone)
// with several multi-field items — varied title/status/priority/type, a
// dependency edge, an assignee, and history depth > 1 including a close reason —
// then re-emits them with the top-level `rd migrate` and asserts `rd migrate
// --parity` exits 0 with a full item-for-item field match. Written FAILING first
// (migrateCmd did not exist): the top-level command is the deliverable.
func TestTopLevelMigrateParity_GreenOnSeededBoard(t *testing.T) {
	dir := setupNostrCmdTest(t)
	seedCampfireBoard(t, dir)

	// Confirm the legacy JSONL source really is multi-field / multi-history
	// BEFORE migrating, so a green parity cannot be an artefact of trivial items.
	src, err := readSeededSource(t)
	if err != nil {
		t.Fatalf("read campfire source: %v", err)
	}
	if len(src) != 3 {
		t.Fatalf("expected 3 seeded source items, got %d", len(src))
	}
	assertSeededSourceShape(t, src)

	// --- `rd migrate` (migration mode) ---
	migRes, err := runTopMigrate(t)
	if err != nil {
		t.Fatalf("rd migrate: %v", err)
	}
	if got, want := migRes["migrated_items"], float64(3); got != want {
		t.Fatalf("migrated_items = %v, want %v (result=%+v)", got, want, migRes)
	}

	// --- `rd migrate --parity` must exit 0 with a full field match ---
	rep, err := runTopParity(t)
	if err != nil {
		t.Fatalf("rd migrate --parity returned non-nil error (CLI exit 1) on a fully-migrated seeded board: %v\nreport=%+v", err, rep)
	}
	if !rep.AllMatch() {
		t.Fatalf("rd migrate --parity did not report AllMatch on a fully-migrated seeded board: %+v", rep)
	}
	if rep.SourceCount != 3 || rep.ProjectedCount != 3 || rep.Matched != 3 {
		t.Fatalf("expected source=projected=matched=3, got source=%d projected=%d matched=%d", rep.SourceCount, rep.ProjectedCount, rep.Matched)
	}
	for _, ip := range rep.Items {
		if !ip.Match() {
			t.Fatalf("item %s mismatched on a green parity run: %v", ip.ItemID, ip.Diffs)
		}
	}
}

// readSeededSource reads the campfire-backed source the same way the
// migrate/parity commands do, for the pre-flight shape assertion.
func readSeededSource(t *testing.T) ([]*state.Item, error) {
	t.Helper()
	return allItemsFromJSONLOrStore()
}

// assertSeededSourceShape proves the seeded board is genuinely multi-field /
// multi-history so parity is a real comparison, not a trivial one.
func assertSeededSourceShape(t *testing.T, src []*state.Item) {
	t.Helper()
	byID := map[string]*state.Item{}
	for _, it := range src {
		byID[it.ID] = it
	}
	i1 := byID["ready-mig01"]
	i2 := byID["ready-mig02"]
	i3 := byID["ready-mig03"]
	if i1 == nil || i2 == nil || i3 == nil {
		t.Fatalf("missing seeded items: %v", byID)
	}
	if len(i1.History) < 2 {
		t.Fatalf("ready-mig01 must have history depth > 1, got %d", len(i1.History))
	}
	if len(i2.BlockedBy) == 0 {
		t.Fatalf("ready-mig02 must carry a dependency edge (BlockedBy), got none")
	}
	if i1.Priority == i2.Priority && i2.Priority == i3.Priority {
		t.Fatalf("seeded priorities must vary; all = %q", i1.Priority)
	}
	if i2.Type == i1.Type && i1.Type == i3.Type {
		// at least one differing type (bug vs task)
		t.Fatalf("seeded types must vary; all = %q", i1.Type)
	}
	found := false
	for _, h := range i3.History {
		if strings.Contains(h.Note, "shipped and verified") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ready-mig03 close reason not present in history: %+v", i3.History)
	}
	fmt.Fprintf(os.Stderr, "seeded shape ok: mig01.history=%d mig02.deps=%v\n", len(i1.History), i2.BlockedBy)
}
