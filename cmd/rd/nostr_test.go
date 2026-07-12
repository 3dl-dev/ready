// nostr_test.go — cmd/rd-level test coverage for the rd<->nostr integration
// (ready-633, wave-9 HIGH finding): before this file, no test set RD_NOSTR=1 or
// RD_NOSTR_READ=1, and cmd/rd's live mutation-publish hooks, the dual-read path,
// and the `rd nostr` subcommands were exercised only at the pkg/sync level.
//
// Three groups of coverage, matching the ready-633 DONE conditions:
//
//   - Group A: RD_NOSTR=1 mutation-hook coverage (create / status-change-claim /
//     dep-add / label-add / implicit-unblock) against a RELAY-LESS publish path
//     (RD_NOSTR_RELAY_URL points at an unreachable loopback address so the
//     publish attempt fails fast and the event is proven durable in the local
//     authoritative log, exactly as the "every relay offline" contract requires).
//     These call the REAL cmd/rd hook functions (publishItemCreateNostr et al.),
//     not a reimplementation, mirroring the exact call sequence each command
//     (create.go/claim.go/dep.go/label.go) performs AFTER campfire enforcement
//     succeeds.
//   - Group B: RD_NOSTR_READ=1 dual-read parity — rd list/ready/show produce the
//     SAME item set (on every field rdSync.CompareItem checks) whether reading
//     the default campfire/JSONL backend or the nostr projection.
//   - Group C: `rd nostr migrate` + `rd nostr parity` CLI-level exit codes and
//     output, including the ready-187 "undercount is a hard mismatch" contract.
//   - Group D: one RD_NOSTR_LIVE_RELAY=1-gated live-relay round-trip proof for
//     the real create hook (skipped by default; mirrors pkg/sync's liveRelayKey
//     pattern so it never runs against the locked relays without an admitted key).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
	rdSync "github.com/campfire-net/ready/pkg/sync"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// unreachableRelayURL is a loopback address nothing listens on. nostr.Publish's
// DialContext fails immediately (connection refused) instead of waiting out
// nostr.DefaultTimeout (10s), so every non-live-relay test in this file points
// RD_NOSTR_RELAY_URL here -- the real publish hooks run their FULL path (log
// append -> relay attempt -> buffer-on-failure) without ever dialing the locked
// production relays (relay-a/relay-b; see CLAUDE.md -- LIVE RELAYS, LOCKED).
const unreachableRelayURL = "ws://127.0.0.1:1"

// setupNostrCmdTest creates an isolated JSONL-only rd project (no campfire) plus
// a fresh nostr portfolio-key home ($TMP/.cf -- named ".cf" so
// nostr.LoadOrCreatePortfolioKey's requireUnderCFHome guard accepts it) and
// chdirs into the project. Every mutable cmd/rd global the nostr paths touch
// (rdHome, jsonOutput, debugOutput, protocolClient) is saved and restored via
// t.Cleanup. Returns the project directory.
func setupNostrCmdTest(t *testing.T) string {
	t.Helper()

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	base := t.TempDir()
	cfHome := filepath.Join(base, ".cf")
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		t.Fatalf("mkdir .cf: %v", err)
	}
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ready"), 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}

	origRdHome := rdHome
	rdHome = cfHome
	t.Cleanup(func() { rdHome = origRdHome })

	// Isolate RDHome() (where nostrKey now reads/writes the nostr identity) to a
	// per-test dir under $TMPDIR — outside any git work tree, so the key-path
	// guard accepts it — distinct from cfHome. cfHome (the legacy .cf) starts
	// empty, so no migration fires and a fresh key is generated under RD_HOME.
	rdHomeDir := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHomeDir, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHomeDir)

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	origJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = origJSON })

	origDebug := debugOutput
	debugOutput = false
	t.Cleanup(func() { debugOutput = origDebug })

	t.Setenv("RD_NOSTR_RELAY_URL", unreachableRelayURL)
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir project: %v", err)
	}
	return projectDir
}

// tagVal returns the first value of the first tag named `name`, and whether it
// was found. Mirrors pkg/sync's private tagValue for use from cmd/rd tests.
func tagVal(tags [][]string, name string) (string, bool) {
	for _, tg := range tags {
		if len(tg) >= 2 && tg[0] == name {
			return tg[1], true
		}
	}
	return "", false
}

// tagVals returns every value of every tag named `name`, in tag order.
func tagVals(tags [][]string, name string) []string {
	var out []string
	for _, tg := range tags {
		if len(tg) >= 2 && tg[0] == name {
			out = append(out, tg[1])
		}
	}
	return out
}

// mutationFixture is one work:create fixture item for writeCreateMutations.
type mutationFixture struct {
	id, title, forParty, priority, context string
}

// writeCreateMutations writes one work:create mutation per fixture into
// <dir>/.ready/mutations.jsonl -- the exact on-disk shape pkg/state's JSONL
// derive path (DeriveFromJSONLWithCampfire) expects, mirroring the format
// already proven in list_test.go's setupMutationsDir. Every item lands with
// status=inbox and history length 1 (the synthetic "created" entry Derive
// always appends) -- the same shape `rd nostr migrate` faithfully reproduces.
func writeCreateMutations(t *testing.T, dir string, items []mutationFixture) {
	t.Helper()
	campfireHex := strings.Repeat("ab", 32)
	var buf strings.Builder
	for i, it := range items {
		payload := map[string]any{
			"id":       it.id,
			"title":    it.title,
			"type":     "task",
			"for":      it.forParty,
			"priority": it.priority,
		}
		if it.context != "" {
			payload["context"] = it.context
		}
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		rec := map[string]any{
			"msg_id":      fmt.Sprintf("msg-%s", it.id),
			"campfire_id": campfireHex,
			"timestamp":   1700000000000000000 + int64(i)*1_000_000_000,
			"operation":   "work:create",
			"payload":     json.RawMessage(payloadBytes),
			"tags":        []string{"work:create"},
			"sender":      "testsender",
		}
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal mutation record: %v", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	path := filepath.Join(dir, ".ready", "mutations.jsonl")
	if err := os.WriteFile(path, []byte(buf.String()), 0600); err != nil {
		t.Fatalf("write mutations.jsonl: %v", err)
	}
}

// captureStderrPipe replaces os.Stderr with a pipe, calls fn, then returns the
// captured output. Mirrors list_test.go's captureStdoutPipe for stderr.
func captureStderrPipe(t *testing.T, fn func()) string {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = origStderr

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	r.Close()
	return buf.String()
}

// ===========================================================================
// Group A -- RD_NOSTR=1 mutation-hook coverage (relay-less: fast, deterministic)
// ===========================================================================

// TestNostrHooks_Disabled_NoOp proves the off-by-default contract: with RD_NOSTR
// unset, the create hook is a true no-op -- no log file, no error.
func TestNostrHooks_Disabled_NoOp(t *testing.T) {
	dir := setupNostrCmdTest(t)

	if err := publishItemCreateNostr("ready-off1", "Off item", "task", "p2", "", "", "agent@test"); err != nil {
		t.Fatalf("publishItemCreateNostr (RD_NOSTR unset) returned error: %v", err)
	}
	logPath := rdSync.NostrLogPath(dir)
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("nostr log %s must not be created when RD_NOSTR is unset (off by default)", logPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat nostr log: %v", err)
	}
}

// TestNostrHooks_Create_PublishesBoardCardAndStatus mirrors create.go's call to
// publishItemCreateNostr AFTER a (simulated) successful campfire write, and
// asserts the local authoritative log carries the board (30301), card (30302),
// status (1630, open/inbox), and NIP-34 issue-root (1621, ready-da7) events with
// the correct field content, and that the status event anchors to BOTH the
// 30302 card (rd's own projection, unchanged) AND the 1621 issue root
// (additive generic-client interop anchor).
func TestNostrHooks_Create_PublishesBoardCardAndStatus(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR", "1")

	if err := publishItemCreateNostr("ready-c01", "Fix the thing", "task", "p1", "", "some context", "agent@test.dev"); err != nil {
		t.Fatalf("publishItemCreateNostr: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events (board+card+issue+status), got %d: %+v", len(events), events)
	}
	var board, card, status, issue *nostr.Event
	for _, e := range events {
		switch e.Kind {
		case rdSync.KindBoard:
			board = e
		case rdSync.KindCard:
			card = e
		case rdSync.KindStatusOpen:
			status = e
		case rdSync.KindIssue:
			issue = e
		}
	}
	if board == nil {
		t.Fatal("no board (kind 30301) event appended")
	}
	if card == nil {
		t.Fatal("no card (kind 30302) event appended")
	}
	if status == nil {
		t.Fatal("no status (kind 1630) event appended")
	}
	if issue == nil {
		t.Fatal("no issue-root (kind 1621, ready-da7) event appended")
	}
	if d, _ := tagVal(card.Tags, "d"); d != "ready-c01" {
		t.Errorf("card d tag = %q, want ready-c01", d)
	}
	if title, _ := tagVal(card.Tags, "title"); title != "Fix the thing" {
		t.Errorf("card title tag = %q, want %q", title, "Fix the thing")
	}
	if s, _ := tagVal(card.Tags, "s"); s != state.StatusInbox {
		t.Errorf("card s (status) tag = %q, want %q (default create status)", s, state.StatusInbox)
	}
	if rank, _ := tagVal(card.Tags, "rank"); rank != "p1" {
		t.Errorf("card rank tag = %q, want p1", rank)
	}
	if itype, _ := tagVal(card.Tags, "itype"); itype != "task" {
		t.Errorf("card itype tag = %q, want task", itype)
	}
	if forTag, _ := tagVal(card.Tags, "for"); forTag != "agent@test.dev" {
		t.Errorf("card for tag = %q, want agent@test.dev", forTag)
	}
	if card.Content != "some context" {
		t.Errorf("card content = %q, want %q", card.Content, "some context")
	}
	if s, _ := tagVal(status.Tags, "status"); s != state.StatusInbox {
		t.Errorf("status event status tag = %q, want inbox", s)
	}
	// Issue-root event carries the rd-extension "d" lookup tag + NIP-34 "subject".
	if d, _ := tagVal(issue.Tags, "d"); d != "ready-c01" {
		t.Errorf("issue d tag = %q, want ready-c01", d)
	}
	if subj, _ := tagVal(issue.Tags, "subject"); subj != "Fix the thing" {
		t.Errorf("issue subject tag = %q, want %q", subj, "Fix the thing")
	}
	// The status event's "a"/first "e" tag (rd's own projection anchor) is
	// UNCHANGED; it also carries a SECOND, "root"-marked "e" tag pointing at the
	// issue event -- the additive generic-NIP-34-client anchor (ready-da7).
	if a, _ := tagVal(status.Tags, "a"); a != rdSync.CardCoord(card.PubKey, "ready-c01") {
		t.Errorf("status a tag = %q, want card coord (unchanged rd anchor)", a)
	}
	es := tagVals(status.Tags, "e")
	if len(es) != 2 {
		t.Fatalf("status event should carry 2 \"e\" tags (card id + issue-root id), got %d: %v", len(es), es)
	}
	if es[0] != card.ID {
		t.Errorf("status first e tag = %q, want card id %q (unchanged rd anchor)", es[0], card.ID)
	}
	if es[1] != issue.ID {
		t.Errorf("status second e tag = %q, want issue id %q (ready-da7 anchor)", es[1], issue.ID)
	}
	foundRoot := false
	for _, tg := range status.Tags {
		if len(tg) >= 4 && tg[0] == "e" && tg[1] == issue.ID && tg[3] == "root" {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Errorf("status event's issue e tag missing NIP-10 \"root\" marker: %v", status.Tags)
	}
}

// TestNostrHooks_StatusChange_ClaimAppendsStatusEventWithReason mirrors
// claim.go's exact sequence: mutate the in-memory item (status->active,
// by->claimer), THEN call publishItemStatusChangeNostr with the claim reason.
// Asserts a refreshed card (assignee + new status) plus a NIP-34 status event
// carrying the close/change reason in its content -- and that claim does NOT
// republish the board (only create does). The item's first-ever nostr publish
// also mints its NIP-34 issue-root event (ready-da7, additive) here, since this
// test claims an item with no prior nostr history.
func TestNostrHooks_StatusChange_ClaimAppendsStatusEventWithReason(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR", "1")

	item := &state.Item{ID: "ready-cl01", Title: "Claimable", Type: "task", Priority: "p2", Status: state.StatusInbox}
	// Mirror claim.go: transition to active, assign, THEN publish (after enforcement).
	item.Status = state.StatusActive
	item.By = "abc123deadbeef"
	if err := publishItemStatusChangeNostr(item, "picking this up"); err != nil {
		t.Fatalf("publishItemStatusChangeNostr: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected exactly 3 events (card + issue-root + status, NO board republish), got %d: %+v", len(events), events)
	}
	var card, status, issue *nostr.Event
	for _, e := range events {
		switch e.Kind {
		case rdSync.KindCard:
			card = e
		case rdSync.KindStatusOpen:
			status = e
		case rdSync.KindIssue:
			issue = e
		}
	}
	if card == nil || status == nil || issue == nil {
		t.Fatalf("missing card/status/issue event: card=%v status=%v issue=%v", card, status, issue)
	}
	if es := tagVals(status.Tags, "e"); len(es) != 2 || es[1] != issue.ID {
		t.Errorf("status e tags = %v, want [cardID, issueID(%s)]", es, issue.ID)
	}
	if s, _ := tagVal(card.Tags, "s"); s != state.StatusActive {
		t.Errorf("card s tag = %q, want active", s)
	}
	if p, _ := tagVal(card.Tags, "p"); p != "abc123deadbeef" {
		t.Errorf("card assignee (p) tag = %q, want abc123deadbeef", p)
	}
	if status.Content != "picking this up" {
		t.Errorf("status event content (reason) = %q, want %q", status.Content, "picking this up")
	}
	if s, _ := tagVal(status.Tags, "status"); s != state.StatusActive {
		t.Errorf("status tag = %q, want active", s)
	}
}

// TestNostrHooks_DepAdd_CardCarriesDepTag mirrors dep.go's depAddCmd: append the
// new blocker id to BlockedBy, then a card-ONLY edit (no status event -- blocked
// status is a projection of the dep tags, not an authoritative transition).
func TestNostrHooks_DepAdd_CardCarriesDepTag(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR", "1")

	blocked := &state.Item{ID: "ready-dep01", Title: "Blocked item", Type: "task", Priority: "p1", Status: state.StatusActive}
	blocked.BlockedBy = strSliceAppendUnique(blocked.BlockedBy, "ready-blocker01")
	if err := publishItemCardEditNostr(blocked); err != nil {
		t.Fatalf("publishItemCardEditNostr: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event (card-only edit), got %d: %+v", len(events), events)
	}
	card := events[0]
	if card.Kind != rdSync.KindCard {
		t.Fatalf("expected kind %d (card), got %d", rdSync.KindCard, card.Kind)
	}
	deps := tagVals(card.Tags, "i")
	if len(deps) != 1 || deps[0] != "ready-blocker01" {
		t.Errorf("card i (dep) tags = %v, want [ready-blocker01]", deps)
	}
}

// TestNostrHooks_LabelAdd_CardCarriesLabelTag mirrors label.go's labelAddCmd:
// append the new label, then a card-only edit carrying the updated "l" tags.
func TestNostrHooks_LabelAdd_CardCarriesLabelTag(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR", "1")

	item := &state.Item{ID: "ready-lbl01", Title: "Labelled item", Type: "task", Priority: "p2", Status: state.StatusActive}
	item.Labels = strSliceAppendUnique(item.Labels, "bug")
	if err := publishItemCardEditNostr(item); err != nil {
		t.Fatalf("publishItemCardEditNostr: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 card-only edit event, got %d: %+v", len(events), events)
	}
	labels := tagVals(events[0].Tags, "l")
	if len(labels) != 1 || labels[0] != "bug" {
		t.Errorf("card l (label) tags = %v, want [bug]", labels)
	}
}

// TestNostrHooks_ImplicitUnblock_RepublishesBlockedItemCard mirrors close.go /
// complete.go's cascade: publishImplicitUnblockNostr re-derives each
// previously-blocked item's CURRENT campfire/JSONL state (edge already removed
// there) and republishes its card so the nostr side drops the stale "i" tag too.
func TestNostrHooks_ImplicitUnblock_RepublishesBlockedItemCard(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR", "1")

	// Current campfire/JSONL state: the edge is already gone (as it would be
	// immediately after the blocker item reached a terminal status).
	writeCreateMutations(t, dir, []mutationFixture{
		{id: "ready-wasblocked", title: "Was blocked", forParty: "agent@test", priority: "p1"},
	})

	// byIDFromJSONLOrStore resolves entirely from JSONL in this project (no
	// campfire), so the store argument is never dereferenced -- nil is safe.
	publishImplicitUnblockNostr(nil, []string{"ready-wasblocked"})

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 card-only republish event, got %d: %+v", len(events), events)
	}
	card := events[0]
	if d, _ := tagVal(card.Tags, "d"); d != "ready-wasblocked" {
		t.Errorf("republished card d tag = %q, want ready-wasblocked", d)
	}
	if deps := tagVals(card.Tags, "i"); len(deps) != 0 {
		t.Errorf("republished card still carries dep tags %v; current JSONL state has no BlockedBy", deps)
	}
}

// ===========================================================================
// Group B -- RD_NOSTR_READ=1 dual-read parity: list/ready/show
// ===========================================================================

type flagSet struct{ name, value string }

// runJSONItems sets --json, applies flags, runs cmd.RunE, and unmarshals the
// captured stdout into a slice of items (list/ready's --json shape).
func runJSONItems(t *testing.T, cmd *cobra.Command, flags []flagSet) []*state.Item {
	t.Helper()
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()
	for _, f := range flags {
		if err := cmd.Flags().Set(f.name, f.value); err != nil {
			t.Fatalf("%s: set --%s=%s: %v", cmd.Name(), f.name, f.value, err)
		}
	}
	out := captureStdoutPipe(t, func() {
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("%s RunE: %v", cmd.Name(), err)
		}
	})
	var items []*state.Item
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &items); err != nil {
		t.Fatalf("unmarshal %s --json output: %v\noutput:\n%s", cmd.Name(), err, out)
	}
	return items
}

// runJSONShow sets --json, runs showCmd.RunE for id, and unmarshals the item.
func runJSONShow(t *testing.T, id string) *state.Item {
	t.Helper()
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()
	out := captureStdoutPipe(t, func() {
		if err := showCmd.RunE(showCmd, []string{id}); err != nil {
			t.Fatalf("show RunE: %v", err)
		}
	})
	var item state.Item
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &item); err != nil {
		t.Fatalf("unmarshal show --json output: %v\noutput:\n%s", err, out)
	}
	return &item
}

// assertItemSetsMatch fails the test with the concrete field-level diffs when
// two item sets do not project identically, via the SAME field-for-field
// comparator (rdSync.CompareItemSets) the ready-187 parity gate uses.
func assertItemSetsMatch(t *testing.T, label string, defaultRead, dualRead []*state.Item) {
	t.Helper()
	if len(defaultRead) == 0 {
		t.Fatalf("%s: default (campfire/JSONL) read returned 0 items -- fixture broken", label)
	}
	dm := make(map[string]*state.Item, len(defaultRead))
	for _, it := range defaultRead {
		dm[it.ID] = it
	}
	nm := make(map[string]*state.Item, len(dualRead))
	for _, it := range dualRead {
		nm[it.ID] = it
	}
	rep := rdSync.CompareItemSets(dm, nm)
	if !rep.AllMatch() {
		t.Errorf("%s: RD_NOSTR_READ=1 dual-read does NOT match the default read: source=%d projected=%d matched=%d mismatched=%d",
			label, rep.SourceCount, rep.ProjectedCount, rep.Matched, rep.Mismatched)
		for _, ip := range rep.Items {
			if !ip.Match() {
				t.Errorf("  %s: %v", ip.ItemID, ip.Diffs)
			}
		}
	}
}

// TestNostrDualRead_ListReadyShow_MatchDefault populates a JSONL project (the
// default campfire-backed read surface, minus an actual campfire), migrates the
// SAME item set into the nostr log via the real `rd nostr migrate --local-only`
// command, then asserts rd list / rd ready / rd show produce THE SAME items on
// every parity-checked field whether RD_NOSTR_READ is unset (default) or =1
// (nostr projection) -- the ready-d65 dual-read contract.
func TestNostrDualRead_ListReadyShow_MatchDefault(t *testing.T) {
	dir := setupNostrCmdTest(t)

	writeCreateMutations(t, dir, []mutationFixture{
		{id: "ready-dr01", title: "First dual-read item", forParty: "agent@test.dev", priority: "p1", context: "ctx one"},
		{id: "ready-dr02", title: "Second dual-read item", forParty: "agent@test.dev", priority: "p2"},
		{id: "ready-dr03", title: "Third dual-read item", forParty: "someone-else@test.dev", priority: "p0"},
	})

	if _, err := requireClient(); err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	resetMigrateFlags(t)
	if err := nostrMigrateCmd.Flags().Set("local-only", "true"); err != nil {
		t.Fatal(err)
	}
	var migrateErr error
	captureStdoutPipe(t, func() {
		migrateErr = nostrMigrateCmd.RunE(nostrMigrateCmd, nil)
	})
	if migrateErr != nil {
		t.Fatalf("nostr migrate --local-only: %v", migrateErr)
	}
	resetMigrateFlags(t)

	// --- default (campfire/JSONL) read ---
	defaultList := runJSONItems(t, listCmd, nil)
	defaultReady := runJSONItems(t, readyCmd, []flagSet{{"for", ""}})
	defaultShow := runJSONShow(t, "ready-dr01")

	// --- dual-read (nostr projection) ---
	t.Setenv("RD_NOSTR_READ", "1")
	nostrList := runJSONItems(t, listCmd, nil)
	nostrReady := runJSONItems(t, readyCmd, []flagSet{{"for", ""}})
	nostrShow := runJSONShow(t, "ready-dr01")

	assertItemSetsMatch(t, "list", defaultList, nostrList)
	assertItemSetsMatch(t, "ready", defaultReady, nostrReady)
	assertItemSetsMatch(t, "show", []*state.Item{defaultShow}, []*state.Item{nostrShow})

	if len(defaultList) != 3 {
		t.Fatalf("expected 3 items in the default list read, got %d", len(defaultList))
	}
	if len(nostrList) != 3 {
		t.Fatalf("expected 3 items in the dual-read list, got %d", len(nostrList))
	}
}

// ===========================================================================
// Group C -- `rd nostr migrate` + `rd nostr parity` CLI: exit codes + output
// ===========================================================================

// resetStringSliceFlag clears a StringSlice-typed flag back to empty AND resets
// its "changed" bookkeeping. This is necessary because pflag's stringSliceValue
// APPENDS on every Set() call once changed=true (see spf13/pflag/string_slice.go),
// so Set(name, "") after a prior non-empty Set is a no-op, not a reset.
func resetStringSliceFlag(t *testing.T, cmd *cobra.Command, name string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("flag %q not found on %s", name, cmd.Name())
	}
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		if err := sv.Replace(nil); err != nil {
			t.Fatalf("reset flag %q: %v", name, err)
		}
	}
	f.Changed = false
}

func resetMigrateFlags(t *testing.T) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(nostrMigrateCmd.Flags().Set("local-only", "false"))
	must(nostrMigrateCmd.Flags().Set("limit", "0"))
	resetStringSliceFlag(t, nostrMigrateCmd, "only")
	must(nostrMigrateCmd.Flags().Set("all", "true"))
}

func resetParityFlags(t *testing.T) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(nostrParityCmd.Flags().Set("verbose", "false"))
	must(nostrParityCmd.Flags().Set("sample", "false"))
}

// runJSONNostrMigrate runs nostrMigrateCmd.RunE with --json and returns the
// decoded result map plus the RunE error (callers assert on both).
func runJSONNostrMigrate(t *testing.T) (map[string]any, error) {
	t.Helper()
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()
	var runErr error
	out := captureStdoutPipe(t, func() {
		runErr = nostrMigrateCmd.RunE(nostrMigrateCmd, nil)
	})
	if runErr != nil {
		return nil, runErr
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		t.Fatalf("unmarshal migrate output: %v\noutput:\n%s", err, out)
	}
	return result, nil
}

// runJSONNostrParity runs nostrParityCmd.RunE with --json and returns the
// decoded ParityReport plus the RunE error (a non-nil error IS the CLI's
// exit-code-1 contract: cobra's Execute() would os.Exit(1) on this same error).
func runJSONNostrParity(t *testing.T) (rdSync.ParityReport, error) {
	t.Helper()
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()
	var runErr error
	out := captureStdoutPipe(t, func() {
		runErr = nostrParityCmd.RunE(nostrParityCmd, nil)
	})
	var rep rdSync.ParityReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rep); err != nil {
		t.Fatalf("unmarshal parity output: %v\noutput:\n%s", err, out)
	}
	return rep, runErr
}

// TestNostrMigrateAndParity_CLI_PassAndFail exercises the ready-d65/ready-187
// CLI surface end to end:
//  1. `rd nostr migrate --local-only --only <2 of 3>` migrates a deliberate subset.
//  2. `rd nostr parity` WITHOUT --sample must FAIL (non-nil error, exit 1
//     equivalent) and flag the un-migrated item as LOST -- an undercount can
//     never be silently masked.
//  3. `rd nostr parity --sample` on that SAME subset must PASS -- the operator
//     explicitly asserted the subset was intentional.
//  4. After completing the migration, `rd nostr parity` (no --sample) must PASS.
func TestNostrMigrateAndParity_CLI_PassAndFail(t *testing.T) {
	dir := setupNostrCmdTest(t)
	_ = dir

	writeCreateMutations(t, dir, []mutationFixture{
		{id: "ready-mp01", title: "Migrate item one", forParty: "agent@test", priority: "p1"},
		{id: "ready-mp02", title: "Migrate item two", forParty: "agent@test", priority: "p2"},
		{id: "ready-mp03", title: "Migrate item three", forParty: "agent@test", priority: "p0"},
	})

	if _, err := requireClient(); err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	// --- 1. partial migration: only 2 of 3 items (forces an undercount) ---
	resetMigrateFlags(t)
	if err := nostrMigrateCmd.Flags().Set("local-only", "true"); err != nil {
		t.Fatal(err)
	}
	if err := nostrMigrateCmd.Flags().Set("only", "ready-mp01,ready-mp02"); err != nil {
		t.Fatal(err)
	}
	migrateResult, err := runJSONNostrMigrate(t)
	if err != nil {
		t.Fatalf("nostr migrate --only (partial): %v", err)
	}
	if got, want := migrateResult["migrated_items"], float64(2); got != want {
		t.Errorf("migrated_items = %v, want %v (migrate output: %+v)", got, want, migrateResult)
	}
	resetMigrateFlags(t)

	// --- 2. parity WITHOUT --sample must FAIL: ready-mp03 was never migrated ---
	resetParityFlags(t)
	rep, err := runJSONNostrParity(t)
	if err == nil {
		t.Error("nostr parity without --sample should return a non-nil error (CLI exit-1 equivalent) on an undercounted projection")
	}
	if rep.AllMatch() {
		t.Error("parity report reported AllMatch=true despite the undercounted projection")
	}
	foundLost := false
	for _, ip := range rep.Items {
		if ip.ItemID == "ready-mp03" && !ip.Match() {
			foundLost = true
		}
	}
	if !foundLost {
		t.Errorf("parity report did not flag ready-mp03 as LOST: %+v", rep.Items)
	}

	// --- 3. parity WITH --sample must PASS on the intentionally-migrated subset ---
	if err := nostrParityCmd.Flags().Set("sample", "true"); err != nil {
		t.Fatal(err)
	}
	sampleRep, err := runJSONNostrParity(t)
	if err != nil {
		t.Errorf("nostr parity --sample should return nil error on the intentional subset, got: %v (report=%+v)", err, sampleRep)
	}
	if !sampleRep.AllMatch() {
		t.Errorf("nostr parity --sample should report AllMatch=true on the intentional subset, got %+v", sampleRep)
	}
	resetParityFlags(t)

	// --- 4. complete the migration, then default parity (no --sample) must PASS ---
	if err := nostrMigrateCmd.Flags().Set("local-only", "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := runJSONNostrMigrate(t); err != nil {
		t.Fatalf("nostr migrate (complete): %v", err)
	}
	resetMigrateFlags(t)

	finalRep, err := runJSONNostrParity(t)
	if err != nil {
		t.Errorf("nostr parity should return nil error after full migration, got: %v (report=%+v)", err, finalRep)
	}
	if !finalRep.AllMatch() {
		t.Errorf("nostr parity should report AllMatch=true after full migration, got %+v", finalRep)
	}
	if finalRep.SourceCount != 3 || finalRep.ProjectedCount != 3 {
		t.Errorf("expected source=projected=3 after full migration, got source=%d projected=%d", finalRep.SourceCount, finalRep.ProjectedCount)
	}
	resetParityFlags(t)
}

// ===========================================================================
// Group D -- env-gated live-relay round trip for the REAL cmd/rd create hook
// ===========================================================================

// resolveLiveRelayKeyForCmdTest resolves an ADMITTED portfolio signing key the
// locked strfry relays accept (ready-266), mirroring pkg/sync's liveRelayKey
// resolution order exactly:
//  1. RD_NOSTR_TEST_SECRET_HEX -- 32-byte hex secret of an admitted key.
//  2. RD_NOSTR_TEST_KEY_PATH -- path to a SaveKeyFile-format key file.
//  3. $HOME/.cf/nostr-identity.json -- this machine's persistent portfolio key.
//
// Skips the test if none resolve: a write-allowlisted relay cannot be exercised
// for a publish proof without an admitted key.
func resolveLiveRelayKeyForCmdTest(t *testing.T) *nostr.Key {
	t.Helper()
	if h := os.Getenv("RD_NOSTR_TEST_SECRET_HEX"); h != "" {
		k, err := nostr.KeyFromHex(h)
		if err != nil {
			t.Fatalf("RD_NOSTR_TEST_SECRET_HEX: %v", err)
		}
		return k
	}
	path := os.Getenv("RD_NOSTR_TEST_KEY_PATH")
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".cf", "nostr-identity.json")
		}
	}
	if path != "" {
		if k, err := nostr.LoadKeyFile(path); err == nil {
			return k
		}
	}
	t.Skip("no allowlisted portfolio key available: set RD_NOSTR_TEST_SECRET_HEX or RD_NOSTR_TEST_KEY_PATH (write-allowlisted relays reject non-admitted keys; ready-266)")
	return nil
}

// TestLiveRelay_CreateHookPublishesToLockedRelay is the env-gated, no-mock proof
// that cmd/rd's REAL create hook (publishItemCreateNostr) -- not a
// reimplementation -- round-trips through a LIVE, write-allowlisted relay.
// Skipped unless RD_NOSTR_LIVE_RELAY=1 (mirrors pkg/sync's live-relay tests).
// Materializes the resolved allowlisted key under an ISOLATED per-test .cf home
// (never touches the real ~/.cf beyond a read) so the local nostr log this test
// writes never mixes with real project state. Verifies relay acceptance via the
// hook's own debug logging (debugOutput=true), so production code is untouched.
func TestLiveRelay_CreateHookPublishesToLockedRelay(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable, allowlisted portfolio key) to run the live cmd/rd create-hook round-trip")
	}

	k := resolveLiveRelayKeyForCmdTest(t)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	base := t.TempDir()
	cfHome := filepath.Join(base, ".cf")
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		t.Fatalf("mkdir .cf: %v", err)
	}
	if err := nostr.SaveKeyFile(filepath.Join(cfHome, "nostr-identity.json"), k, cfHome); err != nil {
		t.Fatalf("materialize allowlisted key under isolated .cf: %v", err)
	}
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ready"), 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}

	origRdHome := rdHome
	rdHome = cfHome
	t.Cleanup(func() { rdHome = origRdHome })

	// Point RDHome() at the isolated .cf so nostrKey loads the admitted key we
	// just materialized (present ⇒ no migration, no regeneration).
	t.Setenv("RD_HOME", cfHome)

	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	t.Setenv("RD_NOSTR", "1")
	// Deliberately do NOT override RD_NOSTR_RELAY_URL here unless the operator
	// already did -- nostrWriteRelays() falls back to pkg/rdconfig's locked LAN
	// relays, exactly as production does.

	origDebug := debugOutput
	debugOutput = true
	t.Cleanup(func() { debugOutput = origDebug })

	itemID := fmt.Sprintf("ready-633-live-%d", time.Now().UnixNano())
	var publishErr error
	stderrOut := captureStderrPipe(t, func() {
		publishErr = publishItemCreateNostr(itemID, "cmd/rd live create-hook proof", "task", "p1", "", "live proof", "agent@test")
	})
	if publishErr != nil {
		t.Fatalf("publishItemCreateNostr: %v", publishErr)
	}
	if !strings.Contains(stderrOut, "relay-accepted=true") {
		t.Fatalf("RELAY ROUND-TRIP FAILED: no event was accepted by a live relay; debug output:\n%s", stderrOut)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(projectDir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events appended to the local authoritative log")
	}
	for _, e := range events {
		if e.PubKey != k.PubKeyHex() {
			t.Errorf("event %s signed by unexpected key %s, want allowlisted %s", e.ID, e.PubKey, k.PubKeyHex())
		}
	}
	t.Logf("PROVEN: cmd/rd create hook published %d event(s) for %s under the allowlisted key, ACCEPTED by a live relay", len(events), itemID)
}

// ===========================================================================
// ready-be1 — nostrNextCreatedAt future-drift -> cross-machine lost update.
//
// nostrNextCreatedAt stamps createdAt = max(now, newest+1). BEFORE ready-be1 the
// "newest" was the LOG-WIDE newest, so a burst of same-second writes to UNRELATED
// items/grantees drifted the created_at of the NEXT write to ANY item/grantee one
// second per burst event — pushing a FRESH card/grant arbitrarily into the future.
// Because latest-wins (newerThan / DeriveLevels) orders purely by (created_at, id),
// that future-drifted card/grant then BEAT a genuinely-later edit/REVOKE issued at
// real wall-clock time by another machine: a silent lost update / ignored revoke
// (violating ready-f92 cross-machine convergence). The MaxCreatedAtSkew=15min
// ingestion gate only rejects drift BEYOND 15min, so drift inside that window (a few
// hundred writes) is admitted cross-machine and the bug bites — these tests keep the
// drift < 15min so the skew defense does NOT mask it.
//
// ready-be1 scopes "newest" to the writing item's / grantee-grant's OWN causal chain,
// so an unrelated burst can no longer poison a fresh chain, while same-chain intent
// order is preserved. Ordering key (created_at, id) is untouched — convergence holds.
// ===========================================================================

// TestNostrNextCreatedAt_ScopedToChain_NoCrossChainDrift is the root-cause unit test:
// an unrelated burst that drove the log's newest event minutes into the future must
// NOT drift a brand-new item's stamp (different causal chain), yet the SAME item's
// chain must still be strictly monotonic (intent order for sequential same-machine
// writes like `rd create && rd claim`).
func TestNostrNextCreatedAt_ScopedToChain_NoCrossChainDrift(t *testing.T) {
	base := t.TempDir()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	log := rdSync.NewNostrLog(filepath.Join(base, "log.jsonl"))
	now := time.Now().Unix()

	// A burst drove the log's newest event to now+300 (5 min of drift), all on item Y.
	drift, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{ItemID: "ready-Y", Title: "y", Status: state.StatusActive, Priority: "p2", BoardD: "ready"}, now+300)
	if err != nil {
		t.Fatalf("build drift card: %v", err)
	}
	if err := log.Append(drift); err != nil {
		t.Fatalf("append drift card: %v", err)
	}

	// A brand-new item X (different chain) must be stamped at ~now, NOT now+300.
	gotX := nostrNextCreatedAt(log, rdSync.ItemDriftScope("ready-X"))
	if gotX > now+2 {
		t.Fatalf("cross-chain future drift: fresh item X stamped %ds ahead of now (got=%d now=%d); an unrelated burst on item Y poisoned it", gotX-now, gotX, now)
	}

	// The SAME chain still gets strict monotonicity — intent order preserved.
	gotY := nostrNextCreatedAt(log, rdSync.ItemDriftScope("ready-Y"))
	if gotY != now+301 {
		t.Fatalf("same-chain monotonicity broken: next write to Y stamped %d, want %d (newest-for-Y + 1)", gotY, now+301)
	}
}

// TestNostr_CrossMachineLostUpdate_CardFutureDriftBeatsHonestEdit reproduces the
// end-to-end card lost update: machine A burst-drifts its log, then stamps item X's
// card via nostrNextCreatedAt; machine B makes an HONEST edit of X at real wall-clock
// time (genuinely later than A's real write, but BEFORE A's inflated stamp); after
// convergence through the REAL skew gate + projection, the honest edit must win.
func TestNostr_CrossMachineLostUpdate_CardFutureDriftBeatsHonestEdit(t *testing.T) {
	base := t.TempDir()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	self := k.PubKeyHex()
	now := time.Now().Unix()
	const itemID = "ready-lu1"
	const driftSecs = 120 // < MaxCreatedAtSkew(15min): A's card is ADMITTED cross-machine.

	// --- Machine A: an unrelated burst drove A's log newest to now+driftSecs. ---
	aLogPath := filepath.Join(base, "a.jsonl")
	aLog := rdSync.NewNostrLog(aLogPath)
	burst, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{ItemID: "ready-burst", Title: "burst", Status: state.StatusActive, Priority: "p2", BoardD: "ready"}, now+driftSecs)
	if err != nil {
		t.Fatalf("build burst card: %v", err)
	}
	if err := aLog.Append(burst); err != nil {
		t.Fatalf("append burst: %v", err)
	}
	// A creates item X — stamp comes from the code under test.
	aTs := nostrNextCreatedAt(aLog, rdSync.ItemDriftScope(itemID))
	aCard, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{ItemID: itemID, Title: "A-stale", Status: state.StatusActive, Priority: "p1", BoardD: "ready"}, aTs)
	if err != nil {
		t.Fatalf("build A card: %v", err)
	}
	if err := aLog.Append(aCard); err != nil {
		t.Fatalf("append A card: %v", err)
	}

	// --- Machine B: HONEST edit of X at real now+10 (10s genuinely after A's real
	// create), no burst -> no drift. ---
	bCard, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{ItemID: itemID, Title: "B-honest", Status: state.StatusActive, Priority: "p1", BoardD: "ready"}, now+10)
	if err != nil {
		t.Fatalf("build B card: %v", err)
	}
	bLogPath := filepath.Join(base, "b.jsonl")
	bLog := rdSync.NewNostrLog(bLogPath)
	if err := bLog.Append(bCard); err != nil {
		t.Fatalf("append B card: %v", err)
	}

	// --- Converge on machine B through the REAL skew gate (AppendUnique via MergeFrom). ---
	trust := map[string]bool{self: true}
	if _, err := bLog.MergeFrom(aLogPath, trust); err != nil {
		t.Fatalf("merge A into B: %v", err)
	}
	events, err := bLog.ReadAll()
	if err != nil {
		t.Fatalf("read B log: %v", err)
	}
	// Precondition: A's future-drifted card really was ADMITTED (skew gate did NOT mask
	// the bug) — otherwise the test proves nothing.
	haveA := false
	for _, e := range events {
		if e.ID == aCard.ID {
			haveA = true
		}
	}
	if !haveA {
		t.Fatalf("precondition failed: A's card (drift %ds) was rejected by the skew gate; cannot exercise the lost update", driftSecs)
	}

	items := rdSync.ProjectItems(events, rdSync.ProjectOptions{Trusted: trust})
	it, ok := items[itemID]
	if !ok {
		t.Fatalf("item %s not projected", itemID)
	}
	if it.Title != "B-honest" {
		t.Fatalf("cross-machine LOST UPDATE: honest later edit (real now+10) lost to future-drifted card (now+%d); projected title=%q want %q (aTs=%d now=%d)",
			driftSecs, it.Title, "B-honest", aTs, now)
	}
}

// TestNostr_CrossMachineLostRevoke_GrantFutureDriftBeatsHonestRevoke reproduces the
// lost-REVOKE variant on the 39301 role-grant chain: machine A burst-drifts its log,
// then stamps a contributor grant for G via nostrNextCreatedAt; machine B (the owner)
// HONESTLY revokes G at real wall-clock time. After convergence, DeriveLevels must see
// the revoke win — G must be level 0 (revoked), not left admitted by the stale grant.
func TestNostr_CrossMachineLostRevoke_GrantFutureDriftBeatsHonestRevoke(t *testing.T) {
	base := t.TempDir()
	owner, err := nostr.GenerateKey() // board author: only the owner may grant/revoke contributor
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	ownerPub := owner.PubKeyHex()
	gKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("generate grantee key: %v", err)
	}
	grantee := gKey.PubKeyHex()
	otherKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	const boardD = "ready"
	now := time.Now().Unix()
	const driftSecs = 120 // < MaxCreatedAtSkew.

	// --- Machine A: an unrelated grant burst drove A's log newest to now+driftSecs. ---
	aLogPath := filepath.Join(base, "a.jsonl")
	aLog := rdSync.NewNostrLog(aLogPath)
	burst, err := rdSync.BuildRoleGrantEvent(owner, rdSync.RoleGrantSpec{BoardD: boardD, BoardAuthor: ownerPub, Grantee: otherKey.PubKeyHex(), Role: rdSync.RoleContributor}, now+driftSecs)
	if err != nil {
		t.Fatalf("build burst grant: %v", err)
	}
	if err := aLog.Append(burst); err != nil {
		t.Fatalf("append burst grant: %v", err)
	}
	// A grants G contributor — stamp from the code under test (per-grantee scope).
	aTs := nostrNextCreatedAt(aLog, rdSync.GrantDriftScope(boardD, grantee))
	grant, err := rdSync.BuildRoleGrantEvent(owner, rdSync.RoleGrantSpec{BoardD: boardD, BoardAuthor: ownerPub, Grantee: grantee, Role: rdSync.RoleContributor}, aTs)
	if err != nil {
		t.Fatalf("build grant: %v", err)
	}
	if err := aLog.Append(grant); err != nil {
		t.Fatalf("append grant: %v", err)
	}

	// --- Machine B: owner HONESTLY revokes G at real now+10 (10s after the real grant),
	// no burst -> no drift. ---
	revoke, err := rdSync.BuildRoleGrantEvent(owner, rdSync.RoleGrantSpec{BoardD: boardD, BoardAuthor: ownerPub, Grantee: grantee, Role: rdSync.RoleRevoked}, now+10)
	if err != nil {
		t.Fatalf("build revoke: %v", err)
	}
	bLogPath := filepath.Join(base, "b.jsonl")
	bLog := rdSync.NewNostrLog(bLogPath)
	if err := bLog.Append(revoke); err != nil {
		t.Fatalf("append revoke: %v", err)
	}

	// --- Converge on machine B through the REAL skew gate. ---
	trust := map[string]bool{ownerPub: true, grantee: true}
	if _, err := bLog.MergeFrom(aLogPath, trust); err != nil {
		t.Fatalf("merge A into B: %v", err)
	}
	events, err := bLog.ReadAll()
	if err != nil {
		t.Fatalf("read B log: %v", err)
	}

	levels, until := rdSync.DeriveLevels(events, ownerPub, boardD)
	if levels[grantee] != rdSync.LevelRevoked {
		t.Fatalf("cross-machine LOST REVOKE: honest revoke (real now+10) lost to future-drifted grant (now+%d); grantee level=%d want %d (revoked). aTs=%d now=%d",
			driftSecs, levels[grantee], rdSync.LevelRevoked, aTs, now)
	}
	// The point-in-time revocation gate must be armed at the revoke time (now+10), NOT
	// left at +infinity (which is what a surviving grant would yield).
	if u, ok := until[grantee]; !ok || u != now+10 {
		t.Fatalf("revoke not authoritative: authoritative-until for G = %d (ok=%v), want %d (the revoke's created_at)", u, ok, now+10)
	}
}
