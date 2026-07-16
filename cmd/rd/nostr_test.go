// nostr_test.go — cmd/rd-level test coverage for the rd<->nostr integration
// (ready-633, wave-9 HIGH finding): before this file, no test set RD_NOSTR=1 or
// RD_NOSTR_READ=1, and cmd/rd's live mutation-publish hooks, the dual-read path,
// and the `rd nostr` subcommands were exercised only at the pkg/sync level.
//
// Coverage groups, matching the ready-633 DONE conditions:
//
//   - Group A: RD_NOSTR=1 mutation-hook coverage (status-change-claim / dep-add /
//     label-add) against a RELAY-LESS publish path (RD_NOSTR_RELAY_URL points at
//     an unreachable loopback address so the publish attempt fails fast and the
//     event is proven durable in the local authoritative log, exactly as the
//     "every relay offline" contract requires). These call the REAL cmd/rd hook
//     functions, not a reimplementation, mirroring the exact call sequence each
//     command (claim.go/dep.go/label.go) performs AFTER campfire enforcement
//     succeeds. (Create and implicit-unblock coverage now lives in
//     nostrwrite_test.go against the nostr-native hooks — ready-c00.)
//   - Group B: RD_NOSTR_READ=1 dual-read parity — rd list/ready/show produce the
//     SAME item set (on every field rdSync.CompareItem checks) whether reading
//     the default campfire/JSONL backend or the nostr projection.
//   - Group C: `rd nostr migrate` + `rd nostr parity` CLI-level exit codes and
//     output, including the ready-187 "undercount is a hard mismatch" contract.
package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
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
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ready"), 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}

	// Isolate RDHome() (where nostrKey reads/writes the nostr identity) to a
	// per-test dir under $TMPDIR — outside any git work tree, so the key-path
	// guard accepts it. A fresh key is generated under RD_HOME on first use.
	rdHomeDir := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHomeDir, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHomeDir)

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

// setupNostrProjectWithItems creates an isolated nostr-native project (Public
// board so title/label tags project in the clear) named projectName and publishes
// each item as a signed card, returning the project dir. This is the nostr-native
// replacement for the retired mutations.jsonl test fixtures. Cleanup is handled by
// setupNostrCmdTest via t.Cleanup / t.TempDir.
func setupNostrProjectWithItems(t *testing.T, projectName string, items []*state.Item) string {
	t.Helper()
	dir := setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	boardD := projectPrefix(dir)
	if boardD == "" {
		t.Fatalf("projectPrefix(%q) is empty; test dir must have a >=2-char name", dir)
	}
	coord := rdSync.BoardCoord(owner, boardD)
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: projectName, Board: coord, Public: true}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	for _, it := range items {
		if it.Status == "" {
			it.Status = state.StatusInbox
		}
		if err := publishItemFullCreateNostr(dir, owner, it); err != nil {
			t.Fatalf("publish %s: %v", it.ID, err)
		}
	}
	return dir
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
