package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// setupNostrNativeProject builds a nostr-native rd project on top of
// setupNostrCmdTest's isolation: it pins a board coordinate (30301:<owner>:<boardD>)
// in .ready/config.json and appends the signed board event to the authoritative
// log — the exact on-disk signature the default `rd init` (initNostr) leaves. This
// is the state under which nostrNativeProject() reports true and every mutation
// takes the secp256k1 no-.cf path. Returns (projectDir, ownerPubkeyHex).
func setupNostrNativeProject(t *testing.T) (string, string) {
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
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	board := rdSync.BoardSpec{BoardD: boardD, Title: "project", Maintainers: []string{owner}}
	be, err := rdSync.BuildBoardEvent(k, board, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be}); err != nil {
		t.Fatalf("append board event: %v", err)
	}
	return dir, owner
}

// assertNoDotCf enforces the ALL-OR-NOTHING no-.cf invariant of the nostr-native
// default path: no mutation or read may create or read a campfire identity.
//
// ready-6ef #3: this is a WHOLE-TREE walk, not a shallow os.Stat(IdentityPath())
// spot-check. A shallow check missed the load-bearing breach the veracity
// adversary proved — `rd show --audit` provisioned .cf/identity.json via
// requireClient()/protocol.Init on a code path IdentityPath() did not name. The
// walk fails if a campfire identity.json OR a .campfire/ directory appears ANYWHERE
// under CFHome, its parent tmp base, or the project dir — so this class of breach
// is enforced by CI, not spot-checked.
//
// store.db is now asserted absent GLOBALLY too (ready-cb6 I7): the campfire SDK
// and every openStore/store.Open call site have been deleted, so NO rd command —
// read or write — provisions a campfire store.db anywhere. The whole-tree walk
// fails on a store.db just as it does on a campfire identity.json.
func assertNoDotCf(t *testing.T) {
	t.Helper()
	roots := map[string]bool{}
	if h := CFHome(); h != "" {
		roots[h] = true
		roots[filepath.Dir(h)] = true
	}
	if dir, ok := readyProjectDir(); ok {
		roots[dir] = true
	}
	for root := range roots {
		walkAssertNoCampfireIdentity(t, root)
	}
}

// walkAssertNoCampfireIdentity fails the test if any campfire identity.json file
// or .campfire/ directory exists anywhere under root. An absent/unreadable subtree
// is not a breach (WalkDir err -> skip). nostr-identity.json is NOT a campfire
// identity — the exact-name match "identity.json" deliberately excludes it.
func walkAssertNoCampfireIdentity(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".campfire" {
				t.Fatalf("FAIL: a .campfire/ directory was provisioned at %s — the nostr-native path must never write campfire state", path)
			}
			return nil
		}
		if d.Name() == "identity.json" {
			t.Fatalf("FAIL: a .cf identity was provisioned at %s — the nostr-native path must never write .cf", path)
		}
		if d.Name() == "store.db" {
			t.Fatalf("FAIL: a campfire store.db was provisioned at %s — the campfire SDK is gone; no rd command may open a store", path)
		}
		return nil
	})
}

// assertNoCampfireStore fails if a campfire store.db exists under CFHome — the
// point-blank proof that the `rd show` native path no longer opens a campfire
// store (ready-6ef #4). Scoped to the show path: list/ready still touch store.db
// transitionally (see assertNoDotCf's note), so this is asserted only in tests
// that drive exactly the show path in an otherwise store-free project.
func assertNoCampfireStore(t *testing.T) {
	t.Helper()
	storePath := filepath.Join(CFHome(), "store.db")
	if _, err := os.Stat(storePath); err == nil {
		t.Fatalf("FAIL: a campfire store.db was provisioned at %s — the nostr-native `rd show` path must not open a campfire store", storePath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat store.db: %v", err)
	}
}

// TestNostrNative_Detected proves the discriminator: a pinned-board project is
// nostr-native; the same project with no pin is not.
func TestNostrNative_Detected(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	if got, native := nostrNativeProject(); !native || got != dir {
		t.Fatalf("nostrNativeProject() = (%q, %v); want (%q, true)", got, native, dir)
	}
	if !nostrWriteActive() {
		t.Fatalf("nostrWriteActive() = false on a nostr-native project; want true")
	}
}

// TestNostrNative_CreateClaimClose_AttributesToSecp256k1AndNoDotCf is the core
// DONE#2 proof: create→claim→close round-trips through the nostr projection,
// item.By and audit ChangedBy derive from the secp256k1 signing pubkey, and no
// .cf/identity.json is ever created.
func TestNostrNative_CreateClaimClose_AttributesToSecp256k1AndNoDotCf(t *testing.T) {
	_, owner := setupNostrNativeProject(t)

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "Fix the thing", itemType: "task", priority: "p1", context: "ctx",
	})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}

	item, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve after create: %v", err)
	}
	if item.Status != state.StatusInbox {
		t.Fatalf("created status = %q; want inbox", item.Status)
	}
	if item.For != owner {
		t.Fatalf("created For = %q; want owner secp256k1 pubkey %q (default --for = signer)", item.For, owner)
	}

	if err := runClaimNostr(id, "picking up"); err != nil {
		t.Fatalf("runClaimNostr: %v", err)
	}
	item, err = nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve after claim: %v", err)
	}
	if item.Status != state.StatusActive {
		t.Fatalf("claimed status = %q; want active", item.Status)
	}
	if item.By != owner {
		t.Fatalf("claimed By = %q; want secp256k1 signer %q (NOT a .cf ed25519 pubkey)", item.By, owner)
	}
	// Every history entry's ChangedBy must be the secp256k1 signer.
	if len(item.History) == 0 {
		t.Fatalf("claimed item has empty history")
	}
	for _, h := range item.History {
		if h.ChangedBy != owner {
			t.Fatalf("history ChangedBy = %q; want secp256k1 signer %q", h.ChangedBy, owner)
		}
	}

	if err := runCloseNostr(id, "done", "shipped it", "closed"); err != nil {
		t.Fatalf("runCloseNostr: %v", err)
	}
	item, err = nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve after close: %v", err)
	}
	if item.Status != state.StatusDone {
		t.Fatalf("closed status = %q; want done", item.Status)
	}
	// close-with-reason is preserved in the terminal history entry.
	last := item.History[len(item.History)-1]
	if last.ToStatus != state.StatusDone || last.Note != "shipped it" {
		t.Fatalf("terminal history = %+v; want to_status=done note=%q", last, "shipped it")
	}

	assertNoDotCf(t)
}

// TestNostrNative_CreateCmd_EndToEnd_NoDotCf drives the real createCmd/claimCmd
// cobra RunE (proving the in-command branch dispatches to the nostr path) and
// asserts no .cf is provisioned.
func TestNostrNative_CreateCmd_EndToEnd_NoDotCf(t *testing.T) {
	setupNostrNativeProject(t)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("flag set: %v", err)
		}
	}
	must(createCmd.Flags().Set("type", "task"))
	must(createCmd.Flags().Set("priority", "p2"))
	t.Cleanup(func() {
		_ = createCmd.Flags().Set("type", "")
		_ = createCmd.Flags().Set("priority", "")
	})
	if err := createCmd.RunE(createCmd, []string{"End to end item"}); err != nil {
		t.Fatalf("createCmd.RunE: %v", err)
	}

	items, _, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	var id string
	for _, it := range items {
		if it.Title == "End to end item" {
			id = it.ID
		}
	}
	if id == "" {
		t.Fatalf("created item not found in projection")
	}
	if err := claimCmd.RunE(claimCmd, []string{id}); err != nil {
		t.Fatalf("claimCmd.RunE: %v", err)
	}
	it, err := nostrResolveItem(id)
	if err != nil || it.Status != state.StatusActive {
		t.Fatalf("after claim: item=%+v err=%v; want active", it, err)
	}
	assertNoDotCf(t)
}

// TestNostrNative_DelegateGateApprove covers the delegate publisher gap plus the
// gate→approve transition — all attributed to the secp256k1 signer, no .cf.
func TestNostrNative_DelegateGateApprove(t *testing.T) {
	_, owner := setupNostrNativeProject(t)
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "T", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// delegate (previously published NO nostr event).
	if err := runDelegateNostr(id, "atlas/worker-3", "routing"); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	it, _ := nostrResolveItem(id)
	if it.By != "atlas/worker-3" {
		t.Fatalf("after delegate By = %q; want atlas/worker-3", it.By)
	}

	// gate → waiting.
	if err := runGateNostr(id, "design", "confirm approach"); err != nil {
		t.Fatalf("gate: %v", err)
	}
	it, _ = nostrResolveItem(id)
	if it.Status != state.StatusWaiting || it.WaitingType != "gate" {
		t.Fatalf("after gate: status=%q waitingType=%q; want waiting/gate", it.Status, it.WaitingType)
	}

	// approve → active, gate cleared.
	if err := runApproveNostr(id, "go ahead"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	it, _ = nostrResolveItem(id)
	if it.Status != state.StatusActive {
		t.Fatalf("after approve: status=%q; want active", it.Status)
	}
	_ = owner
	assertNoDotCf(t)
}

// TestNostrNative_ImplicitUnblock_UnresolvableIDWarnsInsteadOfSwallowing proves
// publishImplicitUnblockNostrNative surfaces a resolve failure via
// warnNostrPublishFailure instead of a bare `continue` (ready-c00 fix): a
// blocked-item ID that no longer resolves in the nostr projection (e.g. dropped
// between derive and republish) must be diagnosable on stderr, not silently
// dropped.
func TestNostrNative_ImplicitUnblock_UnresolvableIDWarnsInsteadOfSwallowing(t *testing.T) {
	setupNostrNativeProject(t)

	stderrOut := captureStderrPipe(t, func() {
		publishImplicitUnblockNostrNative([]string{"ready-does-not-exist"})
	})
	if !strings.Contains(stderrOut, "implicit-unblock ready-does-not-exist") {
		t.Fatalf("expected a warnNostrPublishFailure diagnostic naming the unresolved id, got: %q", stderrOut)
	}
	if !strings.Contains(stderrOut, "warning: nostr publish failed") {
		t.Fatalf("expected the standard warnNostrPublishFailure prefix, got: %q", stderrOut)
	}
}

// TestNostrNative_DepAndLabel covers the dep + label publisher gaps as card-only
// edits that the projection reads back.
func TestNostrNative_DepAndLabel(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)
	a, err := runCreateNostr(dir, nostrCreateSpec{title: "A", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	b, err := runCreateNostr(dir, nostrCreateSpec{title: "B", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	// A is blocked by B.
	if err := runDepAddNostr(a, b); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	it, _ := nostrResolveItem(a)
	if !sliceContains(it.BlockedBy, b) {
		t.Fatalf("after dep add, %s.BlockedBy = %v; want to contain %s", a, it.BlockedBy, b)
	}

	// remove the dep.
	if err := runDepRemoveNostr(a, b); err != nil {
		t.Fatalf("dep remove: %v", err)
	}
	it, _ = nostrResolveItem(a)
	if sliceContains(it.BlockedBy, b) {
		t.Fatalf("after dep remove, %s.BlockedBy = %v; want %s removed", a, it.BlockedBy, b)
	}

	// label add/remove.
	if err := runLabelAddNostr(a, "bug"); err != nil {
		t.Fatalf("label add: %v", err)
	}
	it, _ = nostrResolveItem(a)
	if !sliceContains(it.Labels, "bug") {
		t.Fatalf("after label add, Labels = %v; want to contain bug", it.Labels)
	}
	if err := runLabelRemoveNostr(a, "bug"); err != nil {
		t.Fatalf("label remove: %v", err)
	}
	it, _ = nostrResolveItem(a)
	if sliceContains(it.Labels, "bug") {
		t.Fatalf("after label remove, Labels = %v; want bug removed", it.Labels)
	}
	assertNoDotCf(t)
}

// TestNostrNative_UpdateFieldsAndStatus covers the update command's field-edit +
// status-transition branches on the nostr path.
func TestNostrNative_UpdateFieldsAndStatus(t *testing.T) {
	setupNostrNativeProject(t)
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "Old", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runUpdateNostr(id, nostrUpdateSpec{
		title: "New title", priority: "p0", hasFieldUpdate: true,
		statusTo: state.StatusActive, hasStatusUpdate: true, note: "starting",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	it, _ := nostrResolveItem(id)
	if it.Title != "New title" || it.Priority != "p0" || it.Status != state.StatusActive {
		t.Fatalf("after update: title=%q priority=%q status=%q; want New title/p0/active", it.Title, it.Priority, it.Status)
	}
	assertNoDotCf(t)
}

// TestNostrNative_ShowAudit_NoDotCf is the ready-6ef veracity-fix proof: `rd show
// --audit` on a nostr-native default-path project provisions NO campfire identity
// (.cf/identity.json) and NO campfire store (store.db), and still renders a correct
// audit trail sourced from the nostr projection — every history entry attributed to
// the secp256k1 signer, the owner annotated "owner (root principal)".
//
// BEFORE the fix this FAILS: show.go called openStore() (store.db) and, under
// --audit, requireClient() -> protocol.Init -> identity.Generate+Save (.cf/identity.json).
func TestNostrNative_ShowAudit_NoDotCf(t *testing.T) {
	_, owner := setupNostrNativeProject(t)

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "Audit me", itemType: "task", priority: "p1", context: "ctx",
	})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}
	if err := runClaimNostr(id, "picking up"); err != nil {
		t.Fatalf("runClaimNostr: %v", err)
	}
	if err := runCloseNostr(id, "done", "shipped it", "closed"); err != nil {
		t.Fatalf("runCloseNostr: %v", err)
	}

	// Drive the real `rd show --audit` cobra RunE with stdout captured.
	if err := showCmd.Flags().Set("audit", "true"); err != nil {
		t.Fatalf("set --audit: %v", err)
	}
	t.Cleanup(func() { _ = showCmd.Flags().Set("audit", "false") })

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	runErr := showCmd.RunE(showCmd, []string{id})
	w.Close()
	os.Stdout = origStdout

	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()
	if runErr != nil {
		t.Fatalf("showCmd.RunE --audit error: %v", runErr)
	}
	out := b.String()

	// The load-bearing invariant: no campfire identity and no campfire store.
	assertNoDotCf(t)
	assertNoCampfireStore(t)

	// Audit output is still correct: history present, attributed to the secp256k1
	// signer, owner annotated as the root principal from the nostr projection.
	if !strings.Contains(out, "History:") {
		t.Fatalf("show --audit output missing History section:\n%s", out)
	}
	if !strings.Contains(out, owner) {
		t.Fatalf("show --audit output does not attribute history to the secp256k1 signer %q:\n%s", owner, out)
	}
	if !strings.Contains(out, "authority: owner (root principal)") {
		t.Fatalf("show --audit did not annotate the owner's authority from the nostr projection:\n%s", out)
	}
	// ready-c64: `rd show` must not leak the word "campfire" on a nostr-native
	// item (the old unconditional "Campfire:" label rendered empty and violated
	// the zero-campfire invariant).
	if strings.Contains(strings.ToLower(out), "campfire") {
		t.Fatalf("rd show output leaked 'campfire' on a nostr-native item:\n%s", out)
	}
}

// TestNostrBoardAuthor_MalformedPinHardErrors is the HIGH-2 fail-open proof: when
// the pinned board coordinate in .ready/config.json is present but unparseable,
// nostrBoardAuthor MUST hard-error (matching resolveBoardAuthorD) instead of
// silently falling back to the signer's own pubkey — which would publish items
// under the WRONG authority. A create against a malformed pin errors and publishes
// nothing.
func TestNostrBoardAuthor_MalformedPinHardErrors(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	// Corrupt the pin to a present-but-unparseable coordinate.
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	cfg.Board = "not-a-valid-board-coord"
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	// The direct resolver hard-errors.
	if _, err := nostrBoardAuthor(dir, "deadbeef"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("nostrBoardAuthor on malformed pin = %v, want a 'malformed' hard error", err)
	}

	// And a real create refuses rather than publishing under the signer's authority.
	_, err = runCreateNostr(dir, nostrCreateSpec{
		title: "should not publish", itemType: "task", priority: "p1",
	})
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("runCreateNostr on malformed pin = %v, want a 'malformed' hard error", err)
	}

	// Nothing landed in the log under the signer's own authority.
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	events, err := log.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	for _, e := range events {
		if v, ok := tagVal(e.Tags, "d"); ok && v == "ready-zzz" {
			t.Fatalf("a card for ready-zzz was published despite the malformed pin (fail-open)")
		}
	}
	assertNoDotCf(t)
}

// TestNostrNative_RejectGate is the coverage-sweep security-path unit for
// runRejectNostr: a gated item that is REJECTED stays StatusWaiting and records the
// rejection reason in the audit-history replay (a status event re-affirming waiting),
// while rejecting a non-waiting / non-gated item errors. Before the reject publisher
// existed, reject emitted NO nostr event; this proves the ruling is now preserved
// without transitioning the item out of the gate.
func TestNostrNative_RejectGate(t *testing.T) {
	setupNostrNativeProject(t)
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "Gated", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Rejecting a non-gated item (still inbox, no gate) must ERROR.
	if err := runRejectNostr(id, "no gate yet"); err == nil {
		t.Fatalf("reject of a non-gated item must error, got nil")
	}

	// Gate the item → waiting.
	if err := runGateNostr(id, "design", "confirm approach"); err != nil {
		t.Fatalf("gate: %v", err)
	}
	it, _ := nostrResolveItem(id)
	if it.Status != state.StatusWaiting {
		t.Fatalf("after gate status = %q; want waiting", it.Status)
	}

	// Reject the gate: item STAYS waiting, and the reason lands in history.
	const rejectReason = "scope too broad — split it"
	if err := runRejectNostr(id, rejectReason); err != nil {
		t.Fatalf("reject: %v", err)
	}
	it, err = nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve after reject: %v", err)
	}
	if it.Status != state.StatusWaiting {
		t.Fatalf("after reject status = %q; want STILL waiting (reject does not transition out of the gate)", it.Status)
	}
	foundReason := false
	for _, h := range it.History {
		if h.ToStatus == state.StatusWaiting && h.Note == rejectReason {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("reject reason %q not preserved in history: %+v", rejectReason, it.History)
	}
	assertNoDotCf(t)
}

func mustDir(t *testing.T) string {
	t.Helper()
	dir, ok := readyProjectDir()
	if !ok {
		t.Fatalf("no .ready project dir")
	}
	return dir
}

func sliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestNostrNative_ReadActive_DefaultReadsProjection is the S-read proof: on a
// nostr-native project with NO RD_NOSTR_READ env set, the dual-read surface
// resolves items from the nostr projection by DEFAULT. A create publishes to the
// nostr log only (never JSONL/store), so if the default read still went through
// the campfire/JSONL backend, list would be empty. Reading it back via the shared
// allItemsFromJSONLOrStore(openStore) path — exactly what `rd list` does — proves
// reads default to nostr. No .cf is provisioned.
func TestNostrNative_ReadActive_DefaultReadsProjection(t *testing.T) {
	setupNostrNativeProject(t)

	if !nostrReadActive() {
		t.Fatalf("nostrReadActive() = false on a nostr-native project with no env; want true (S-read default ON)")
	}

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "Read me back", itemType: "task", priority: "p2",
	})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}

	items, err := allItemsFromJSONLOrStore()
	if err != nil {
		t.Fatalf("allItemsFromJSONLOrStore: %v", err)
	}
	var found *state.Item
	for _, it := range items {
		if it.ID == id {
			found = it
			break
		}
	}
	if found == nil {
		t.Fatalf("item %s not returned by the default read surface — reads did not default to the nostr projection", id)
	}
	if found.Title != "Read me back" {
		t.Fatalf("read title = %q; want %q", found.Title, "Read me back")
	}

	// byIDFromJSONLOrStore (the `rd show` path) must resolve from nostr too.
	byID, err := byIDFromJSONLOrStore(id)
	if err != nil {
		t.Fatalf("byIDFromJSONLOrStore: %v", err)
	}
	if byID == nil || byID.ID != id {
		t.Fatalf("byIDFromJSONLOrStore(%s) = %+v; want the projected item", id, byID)
	}

	assertNoDotCf(t)
}

// NOTE (ready-a4a): the campfire-backed playbook/engage surface (removed in
// ready-cb6 I7) was rebuilt store-free on the nostr-native path. The playbook
// create->list round-trip and the engage instantiate-with-dep-edges proofs
// (which exercise publishEngagedItemsNostr) now live in engage_test.go.

// TestNostrNative_LabelPropose_CreatesDecisionItem proves `rd label propose` on a
// nostr-native project creates a p3 decision item via the secp256k1 path with no
// .cf, carrying the freeform label-proposal atom.
func TestNostrNative_LabelPropose_CreatesDecisionItem(t *testing.T) {
	setupNostrNativeProject(t)

	if err := labelProposeCmd.RunE(labelProposeCmd, []string{"incident"}); err != nil {
		t.Fatalf("label propose RunE: %v", err)
	}

	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	var found *state.Item
	for _, it := range byID {
		if it.Title == "Label proposal: incident" {
			found = it
			break
		}
	}
	if found == nil {
		t.Fatalf("label propose did not create the decision item in the nostr projection")
	}
	if found.Type != "decision" || found.Priority != "p3" {
		t.Fatalf("proposal item = type %q priority %q; want decision/p3", found.Type, found.Priority)
	}
	if !sliceContains(found.Labels, "label-proposal") {
		t.Fatalf("proposal labels = %v; want to contain label-proposal", found.Labels)
	}

	assertNoDotCf(t)
}

// TestNostrNative_LabelDefine_NoRegistryNoDotCf proves `rd label define` on a
// nostr-native project reports the no-registry note and provisions no .cf (it must
// not crash at identity.Load).
func TestNostrNative_LabelDefine_NoRegistryNoDotCf(t *testing.T) {
	setupNostrNativeProject(t)

	if err := labelDefineCmd.RunE(labelDefineCmd, []string{"hotfix"}); err != nil {
		t.Fatalf("label define RunE on nostr-native should succeed as a no-op, got: %v", err)
	}
	assertNoDotCf(t)
}

// TestNostrNative_PublishCmd_ResolvesFromProjection_NoDotCf is the ready-50a
// regression proof: `rd nostr publish <id>` on a nostr-native project must resolve
// the item via nostrResolveItem (the nostr projection) — NOT via the legacy
// jsonlPath()/DeriveFromJSONLWithCampfire lookup, which has no mutations.jsonl on
// a nostr-native project and always failed with "item %q not found in rd state".
//
// BEFORE the fix this FAILS: nostrPublishCmd.RunE returns an item-not-found error
// (either "no mutations.jsonl found" or "item %q not found in rd state") for ANY
// item on a nostr-native project, because jsonlPath()/DeriveFromJSONLWithCampfire
// never sees the item — it was only ever recorded in the nostr log, not JSONL.
//
// AFTER the fix: the command resolves the item via nostrResolveItem, republishes a
// card event + a status event carrying the item's recorded close reason, and
// provisions no .cf/.campfire state anywhere.
func TestNostrNative_PublishCmd_ResolvesFromProjection_NoDotCf(t *testing.T) {
	setupNostrNativeProject(t)

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "Republish me", itemType: "task", priority: "p1", context: "ctx",
	})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}
	if err := runCloseNostr(id, "done", "shipped it", "closed"); err != nil {
		t.Fatalf("runCloseNostr: %v", err)
	}

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	stdout := captureStdoutPipe(t, func() {
		if err := nostrPublishCmd.RunE(nostrPublishCmd, []string{id}); err != nil {
			t.Fatalf("nostrPublishCmd.RunE: %v (item-not-found means the legacy JSONL lookup is still in place)", err)
		}
	})

	var res rdSync.PublishResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("unmarshal publish result: %v\nstdout=%s", err, stdout)
	}
	var sawCard, sawStatus bool
	for _, ev := range res.Events {
		switch ev.Kind {
		case rdSync.KindCard:
			sawCard = true
		case rdSync.KindStatusOpen, rdSync.KindStatusResolved, rdSync.KindStatusClosed, rdSync.KindStatusDraft:
			sawStatus = true
		}
	}
	if !sawCard {
		t.Fatalf("publish result events = %+v; want a card event", res.Events)
	}
	if !sawStatus {
		t.Fatalf("publish result events = %+v; want a status event", res.Events)
	}

	// The republished status event must carry the item's recorded close reason
	// (ready-da7): lastStatusReason(item) reads the reason back off the resolved
	// item's history, so this only passes once the resolver reads the SAME item
	// the close actually wrote (the nostr projection, not an empty legacy lookup).
	item, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("nostrResolveItem after publish: %v", err)
	}
	if got := lastStatusReason(item); got != "shipped it" {
		t.Fatalf("lastStatusReason(item) = %q; want %q", got, "shipped it")
	}

	assertNoDotCf(t)
}
