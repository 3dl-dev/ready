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

// appendSeedRecord APPENDS one more mutation record to the already-seeded
// <dir>/.ready/mutations.jsonl, simulating a real post-migration SOURCE
// mutation (a campfire-side edit/unblock landing AFTER `rd migrate` already
// re-emitted the nostr projection). This is how a genuine divergence arises in
// production: the campfire source keeps moving, the nostr projection is a
// point-in-time snapshot, and `rd migrate --parity` must catch the drift.
func appendSeedRecord(t *testing.T, dir string, r seedRec) {
	t.Helper()
	line, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal appended seed record: %v", err)
	}
	path := filepath.Join(dir, ".ready", "mutations.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open mutations.jsonl for append: %v", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatalf("append mutations.jsonl: %v", err)
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

// TestTopLevelMigrateParity_NonZeroOnSeededTitleDivergence is the NEGATIVE-path
// companion to TestTopLevelMigrateParity_GreenOnSeededBoard (ready-cf9). It
// proves `rd migrate --parity` actually has teeth on a COMMITTED test, not just
// via throwaway probes: seed the board, migrate it (projection now matches),
// confirm parity is green, then land ONE more campfire-source mutation AFTER
// the migration — a work:update that changes ready-mig01's title — so the
// nostr projection is now stale on that single field. `rd migrate --parity`
// must exit non-zero (RunE returns a non-nil error, the CLI's exit-code-1
// contract) and the report must name the SPECIFIC item and field that diverged
// (not just a bare mismatch count), leaving every other seeded item matched.
func TestTopLevelMigrateParity_NonZeroOnSeededTitleDivergence(t *testing.T) {
	dir := setupNostrCmdTest(t)
	seedCampfireBoard(t, dir)

	if _, err := runTopMigrate(t); err != nil {
		t.Fatalf("rd migrate: %v", err)
	}

	// Baseline: parity is green immediately after migration, so the failure we
	// assert below is caused by the divergence we introduce next, not a
	// pre-existing bug in the harness.
	if rep, err := runTopParity(t); err != nil || !rep.AllMatch() {
		t.Fatalf("expected green parity immediately after migration, got err=%v rep=%+v", err, rep)
	}

	const corruptedTitle = "CORRUPTED: post-migration source drift"
	appendSeedRecord(t, dir, seedRec{
		MsgID: "msg-mig01-update-drift", CampfireID: strings.Repeat("ab", 32),
		Timestamp: int64(1700000000000000000) + 8*1_000_000_000,
		Operation: "work:update", Sender: "human",
		Payload:     json.RawMessage(`{"target":"msg-mig01-create","title":"` + corruptedTitle + `"}`),
		Tags:        []string{"work:update"},
		Antecedents: []string{"msg-mig01-create"},
	})

	// The campfire SOURCE now diverges from the nostr PROJECTION on
	// ready-mig01's title (the projection was never re-migrated). Parity must
	// catch it: non-zero exit (non-nil RunE error) reporting the mismatch.
	rep, err := runTopParity(t)
	if err == nil {
		t.Fatalf("rd migrate --parity returned nil error (exit 0) on a seeded title divergence; report=%+v", rep)
	}
	if rep.AllMatch() {
		t.Fatalf("parity report claims AllMatch() true despite seeded divergence: %+v", rep)
	}
	if rep.Mismatched != 1 {
		t.Fatalf("expected exactly 1 mismatched item, got %d (report=%+v)", rep.Mismatched, rep)
	}

	var got *rdSync.ItemParity
	for i := range rep.Items {
		if rep.Items[i].ItemID == "ready-mig01" {
			got = &rep.Items[i]
		} else if !rep.Items[i].Match() {
			t.Fatalf("unexpected mismatch on untouched item %s: %v", rep.Items[i].ItemID, rep.Items[i].Diffs)
		}
	}
	if got == nil {
		t.Fatalf("ready-mig01 missing from parity report entirely: %+v", rep)
	}
	if got.Match() {
		t.Fatalf("ready-mig01 reported as matched despite seeded title divergence")
	}
	foundTitleDiff := false
	for _, d := range got.Diffs {
		if strings.HasPrefix(d, "title:") {
			foundTitleDiff = true
			if !strings.Contains(d, corruptedTitle) {
				t.Fatalf("title diff does not name the corrupted value: %q", d)
			}
			if !strings.Contains(d, "First migrated item") {
				t.Fatalf("title diff does not name the original projected value: %q", d)
			}
		}
	}
	if !foundTitleDiff {
		t.Fatalf("expected a specific 'title:' diff for ready-mig01, got: %v", got.Diffs)
	}
}
