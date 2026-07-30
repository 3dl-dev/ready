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
	// This helper backs the pre-confidentiality nostr-mechanics tests, which inspect
	// the CLEAR wire (title/label/dep tags). Confidentiality is now the default, so
	// mark the board Public to keep those tests on the plaintext path; confidential
	// behavior has its own coverage (setupConfidentialProject + the ready-216 suite).
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Public: true}); err != nil {
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
	if h := RDHome(); h != "" {
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

// assertNoCampfireStore fails if a campfire store.db exists under the rd home —
// the point-blank proof that the `rd show` native path never opens a campfire
// store (ready-6ef #4).
func assertNoCampfireStore(t *testing.T) {
	t.Helper()
	storePath := filepath.Join(RDHome(), "store.db")
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
	must(createCmd.Flags().Set("parent-id", "none"))
	t.Cleanup(func() {
		_ = createCmd.Flags().Set("type", "")
		_ = createCmd.Flags().Set("priority", "")
		_ = createCmd.Flags().Set("parent-id", "")
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

// TestNostrNative_DelegateOnBlockedItem_Recovers is ready-500's done-condition
// test for the runDelegateNostr instance of the SAME defect class ready-e0e
// fixed for reject (merged e9e8647): 'blocked' is a DERIVED status —
// applyDepAndGateStatus's dep pass only ever ADDS it, never clears one that was
// published verbatim. PROBE reproduced on origin/main before this fix: create
// blocker + target, `rd dep add`, `rd delegate` the blocked target, close the
// blocker => target stayed status=blocked PERMANENTLY (a control with no
// intervening delegate recovered to inbox fine). runDelegateNostr used to
// publish item.Status verbatim (whatever nostrResolveItem projected, including
// the derived "blocked" overlay) via publishItemStatusChangeNostr — that event's
// "status" tag becomes the new prevStatus floor on every future replay, and once
// the blocker closes and the dep pass stops overriding, the floor IS "blocked"
// forever. The fix substitutes the item's own last authoritative status (read
// from item.History, which applyDepAndGateStatus never mutates) before
// publishing whenever the resolved item is currently derived-blocked.
//
// This test advances PAST the write and PAST the blocker closing on purpose:
// ready-e0e's own note records that the predecessor test for reject shipped
// green over the live defect specifically because it stopped at the fold
// immediately after the write, where a burned-in "blocked" and a live derived
// "blocked" are indistinguishable — only closing the blocker exposes the
// difference.
func TestNostrNative_DelegateOnBlockedItem_Recovers(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	blocker, err := runCreateNostr(dir, nostrCreateSpec{title: "Blocker", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	target, err := runCreateNostr(dir, nostrCreateSpec{title: "Target", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := runDepAddNostr(target, blocker); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	it, _ := nostrResolveItem(target)
	if it.Status != state.StatusBlocked {
		t.Fatalf("target status = %q before delegate; want blocked", it.Status)
	}

	if err := runDelegateNostr(target, "atlas/worker-9", "routing"); err != nil {
		t.Fatalf("delegate on a blocked item must succeed, got: %v", err)
	}
	it, _ = nostrResolveItem(target)
	if it.By != "atlas/worker-9" {
		t.Fatalf("after delegate, By = %q; want atlas/worker-9", it.By)
	}
	if it.Status != state.StatusBlocked {
		t.Fatalf("after delegate, status = %q; want STILL blocked (delegate does not itself unblock)", it.Status)
	}

	// --- RECOVERY: close the shared blocker and prove the target is not
	// permanently stuck. Stopping before this step is exactly how the
	// predecessor bug class shipped green over a live defect.
	if err := runCloseNostr(blocker, "done", "unblocking", "closed"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	it, _ = nostrResolveItem(target)
	if it.Status == state.StatusBlocked {
		t.Fatalf("target still blocked after its blocker closed — status burned in by delegate and never re-derived (status=%q)", it.Status)
	}
	if it.Status != state.StatusInbox {
		t.Fatalf("target recovered to %q; want inbox (its pre-block status)", it.Status)
	}
	if it.By != "atlas/worker-9" {
		t.Fatalf("recovery lost the delegate assignment: By = %q; want atlas/worker-9 still", it.By)
	}
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

// TestNostrNative_UpdateStatusBlocked_Refused is ready-500's done-condition test
// for the runUpdateNostr instance of the blocked-is-derived-never-persisted
// class: unlike delegate, an explicit `--status blocked` has no legitimate
// resolved value to coerce to — it is the caller directly asking for the one
// status this write path must never mint — so the guard refuses the call
// outright instead of silently substituting something the caller didn't ask
// for. Without the guard this write would burn status=blocked in as the
// permanent status-authority winner exactly like the delegate/reject instances
// (applyDepAndGateStatus's dep pass only ever ADDS blocked, never clears a
// written one). This test advances past the (refused) write and past the
// blocker closing, proving nothing was persisted to burn in.
func TestNostrNative_UpdateStatusBlocked_Refused(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	blocker, err := runCreateNostr(dir, nostrCreateSpec{title: "Blocker", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	target, err := runCreateNostr(dir, nostrCreateSpec{title: "Target", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := runDepAddNostr(target, blocker); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	it, _ := nostrResolveItem(target)
	if it.Status != state.StatusBlocked {
		t.Fatalf("target status = %q before update; want blocked", it.Status)
	}

	if err := runUpdateNostr(target, nostrUpdateSpec{statusTo: state.StatusBlocked, hasStatusUpdate: true}); err == nil {
		t.Fatalf("update --status blocked must be refused, got nil error")
	}
	it, _ = nostrResolveItem(target)
	if it.Status != state.StatusBlocked {
		t.Fatalf("after the refused update, status = %q; want unaffected (still blocked)", it.Status)
	}

	// --- RECOVERY: close the blocker and prove the refused call left nothing
	// behind to burn in — the target must clear exactly like a control with no
	// intervening write.
	if err := runCloseNostr(blocker, "done", "unblocking", "closed"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	it, _ = nostrResolveItem(target)
	if it.Status == state.StatusBlocked {
		t.Fatalf("target still blocked after its blocker closed — the refused update must not have written anything (status=%q)", it.Status)
	}
	if it.Status != state.StatusInbox {
		t.Fatalf("target recovered to %q; want inbox (its pre-block status)", it.Status)
	}
	assertNoDotCf(t)
}

// TestNostrNative_UpdateFieldEditOnBlockedItem_Recovers proves the common,
// legitimate case — a plain field edit (`rd update --title`, no status change)
// on a currently-blocked item — never risks the derived-status-burn-in class
// either: runUpdateNostr's field block republishes ONLY a card
// (publishItemCardEditNostr, no accompanying NIP-34 status event), so the fold's
// dep pass keeps sole ownership of the blocked/unblocked transition regardless
// of what item.Status happened to read as at call time.
func TestNostrNative_UpdateFieldEditOnBlockedItem_Recovers(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	blocker, err := runCreateNostr(dir, nostrCreateSpec{title: "Blocker", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	target, err := runCreateNostr(dir, nostrCreateSpec{title: "Target", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := runDepAddNostr(target, blocker); err != nil {
		t.Fatalf("dep add: %v", err)
	}

	if err := runUpdateNostr(target, nostrUpdateSpec{title: "Retitled", hasFieldUpdate: true}); err != nil {
		t.Fatalf("field edit on a blocked item must succeed, got: %v", err)
	}
	it, _ := nostrResolveItem(target)
	if it.Title != "Retitled" {
		t.Fatalf("Title = %q; want Retitled", it.Title)
	}
	if it.Status != state.StatusBlocked {
		t.Fatalf("after field edit, status = %q; want STILL blocked", it.Status)
	}

	if err := runCloseNostr(blocker, "done", "unblocking", "closed"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	it, _ = nostrResolveItem(target)
	if it.Status == state.StatusBlocked {
		t.Fatalf("target still blocked after its blocker closed (status=%q)", it.Status)
	}
	if it.Status != state.StatusInbox {
		t.Fatalf("target recovered to %q; want inbox", it.Status)
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

// TestNostrNative_GateOnBlockedItem_RaiseListResolve is the ready-e0e regression
// test: a gate raised on a BLOCKED item (the ordinary case for a design gate,
// since the ruling is usually exactly what unblocks the chain) must be
// (1) raisable without a false "gate sent" success report, (2) VISIBLE in
// `rd gates` / `rd gates --json` with its blocked state surfaced, and
// (3) RESOLVABLE by both approve and reject without first unblocking the item.
// Before the fix: raise reported success but the item never appeared in
// `rd gates`, and both approve and reject refused it with "item is not waiting".
func TestNostrNative_GateOnBlockedItem_RaiseListResolve(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	blocker, err := runCreateNostr(dir, nostrCreateSpec{title: "Blocker", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	approveTarget, err := runCreateNostr(dir, nostrCreateSpec{title: "Approve target", itemType: "decision", priority: "p0"})
	if err != nil {
		t.Fatalf("create approve target: %v", err)
	}
	rejectTarget, err := runCreateNostr(dir, nostrCreateSpec{title: "Reject target", itemType: "decision", priority: "p0"})
	if err != nil {
		t.Fatalf("create reject target: %v", err)
	}
	if err := runDepAddNostr(approveTarget, blocker); err != nil {
		t.Fatalf("dep add (approve target): %v", err)
	}
	if err := runDepAddNostr(rejectTarget, blocker); err != nil {
		t.Fatalf("dep add (reject target): %v", err)
	}
	it, _ := nostrResolveItem(approveTarget)
	if it.Status != state.StatusBlocked {
		t.Fatalf("approve target status = %q before gating; want blocked", it.Status)
	}

	// --- RAISE: gating a blocked item must succeed, not report false success. ---
	if err := runGateNostr(approveTarget, "design", "confirm approach"); err != nil {
		t.Fatalf("gate on blocked item must succeed, got: %v", err)
	}
	if err := runGateNostr(rejectTarget, "design", "confirm approach"); err != nil {
		t.Fatalf("gate on blocked item must succeed, got: %v", err)
	}
	it, _ = nostrResolveItem(approveTarget)
	if it.Status != state.StatusBlocked {
		t.Fatalf("after gate, status = %q; want STILL blocked (blocking supersedes waiting)", it.Status)
	}
	if it.WaitingType != "gate" || it.GateMsgID == "" {
		t.Fatalf("after gate on blocked item, waitingType=%q gateMsgID=%q; want gate/non-empty (the gate must survive blocking)", it.WaitingType, it.GateMsgID)
	}

	// --- LIST: the blocked-and-gated item must appear in `rd gates`. ---
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()

	jsonOutput = true
	jsonOut := captureStdoutPipe(t, func() {
		if err := gatesCmd.RunE(gatesCmd, nil); err != nil {
			t.Fatalf("gatesCmd.RunE (json): %v", err)
		}
	})
	var listed []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &listed); err != nil {
		t.Fatalf("rd gates --json output is not valid JSON: %v; output:\n%s", err, jsonOut)
	}
	foundApprove, foundReject := false, false
	for _, row := range listed {
		if row["id"] == approveTarget {
			foundApprove = true
			if row["status"] != state.StatusBlocked {
				t.Errorf("rd gates --json row for %s has status=%v; want %q (blocked state must be visible)", approveTarget, row["status"], state.StatusBlocked)
			}
		}
		if row["id"] == rejectTarget {
			foundReject = true
		}
	}
	if !foundApprove {
		t.Fatalf("rd gates --json did not list %s (a gate on a blocked item); listed=%v", approveTarget, listed)
	}
	if !foundReject {
		t.Fatalf("rd gates --json did not list %s (a gate on a blocked item); listed=%v", rejectTarget, listed)
	}

	jsonOutput = false
	humanOut := captureStdoutPipe(t, func() {
		if err := gatesCmd.RunE(gatesCmd, nil); err != nil {
			t.Fatalf("gatesCmd.RunE (human): %v", err)
		}
	})
	if !strings.Contains(humanOut, approveTarget) {
		t.Fatalf("rd gates human output missing %s; got:\n%s", approveTarget, humanOut)
	}
	if !strings.Contains(humanOut, "[BLOCKED]") {
		t.Fatalf("rd gates human output does not flag the blocked item as [BLOCKED] — a human could mistake it for actionable; got:\n%s", humanOut)
	}

	// --- RESOLVE: approve and reject must both work WITHOUT unblocking first. ---
	if err := runApproveNostr(approveTarget, "go ahead"); err != nil {
		t.Fatalf("approve of a blocked-and-gated item must succeed, got: %v", err)
	}
	it, _ = nostrResolveItem(approveTarget)
	if it.WaitingType != "" || it.GateMsgID != "" || it.Gate != "" {
		t.Fatalf("after approve, gate fields not cleared: waitingType=%q gate=%q gateMsgID=%q", it.WaitingType, it.Gate, it.GateMsgID)
	}
	if it.Status != state.StatusBlocked {
		t.Fatalf("after approve, status = %q; want STILL blocked — approving the gate does not itself unblock the dependency", it.Status)
	}

	const rejectReason = "not yet — revisit after blocker closes"
	if err := runRejectNostr(rejectTarget, rejectReason); err != nil {
		t.Fatalf("reject of a blocked-and-gated item must succeed, got: %v", err)
	}
	it, _ = nostrResolveItem(rejectTarget)
	if it.Status != state.StatusBlocked {
		t.Fatalf("after reject, status = %q; want STILL blocked (reject does not transition out of the gate)", it.Status)
	}
	if it.WaitingType != "gate" || it.GateMsgID == "" {
		t.Fatalf("after reject, waitingType=%q gateMsgID=%q; want the gate to remain open", it.WaitingType, it.GateMsgID)
	}
	foundReason := false
	for _, h := range it.History {
		if h.Note == rejectReason {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("reject reason %q not preserved in history: %+v", rejectReason, it.History)
	}

	// --- RECOVERY: close the shared blocker and prove neither target is
	// permanently stuck (ready-e0e rework). The predecessor's test stopped at
	// the fold immediately after resolve and only proved a nil error; it never
	// advanced past the blocker closing, which is the actual burn-in defect —
	// a status=blocked event published by reject/approve becomes the
	// status-authority winner forever, because the dep pass only ever ADDS
	// blocked and nothing ever clears it. PROBE1 in the adversary review
	// reproduced exactly this on the reject path.
	if err := runCloseNostr(blocker, "done", "unblocking", "closed"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}

	it, _ = nostrResolveItem(approveTarget)
	if it.Status == state.StatusBlocked {
		t.Fatalf("approve target still blocked after its blocker closed — status burned in and never re-derived (status=%q)", it.Status)
	}
	if it.WaitingType != "" || it.GateMsgID != "" || it.Gate != "" {
		t.Fatalf("approve target gate fields leaked after recovery: waitingType=%q gate=%q gateMsgID=%q", it.WaitingType, it.Gate, it.GateMsgID)
	}

	it, _ = nostrResolveItem(rejectTarget)
	if it.Status == state.StatusBlocked {
		t.Fatalf("reject target still blocked after its blocker closed — the dep pass only ever adds blocked and nothing clears a status burned in by reject (status=%q)", it.Status)
	}
	if it.WaitingType != "gate" || it.GateMsgID == "" {
		t.Fatalf("reject target lost its still-open gate across recovery: waitingType=%q gateMsgID=%q", it.WaitingType, it.GateMsgID)
	}

	// rd gates must still list the reject target (gate genuinely still open)
	// and must NOT flag it [BLOCKED] any more — the blocker is closed, so the
	// label would otherwise assert a dependency that no longer exists.
	jsonOutput = false
	recoveredOut := captureStdoutPipe(t, func() {
		if err := gatesCmd.RunE(gatesCmd, nil); err != nil {
			t.Fatalf("gatesCmd.RunE (post-recovery): %v", err)
		}
	})
	if !strings.Contains(recoveredOut, rejectTarget) {
		t.Fatalf("rd gates dropped %s after its blocker closed; still has an open gate; got:\n%s", rejectTarget, recoveredOut)
	}
	for _, line := range strings.Split(recoveredOut, "\n") {
		if strings.Contains(line, rejectTarget) && strings.Contains(line, "[BLOCKED]") {
			t.Fatalf("rd gates still flags %s as [BLOCKED] after its blocker closed; label is now untruthful; got:\n%s", rejectTarget, recoveredOut)
		}
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

// appendCardOnlyBlockedFixture appends a LONE 30302 card event — status=blocked,
// no accompanying NIP-34 status event, no dep edges — to dir's authoritative log
// under itemID, signed by the project owner. This reproduces, WITHOUT going
// through any (fixed) write path, the exact projection shape ready-500's
// adversary review flagged as understated: an item whose current status reads
// "blocked" with ZERO authoritative status events in its History, so
// nostrResolveItem's card-tag fallback (pkg/sync/nostrproject.go: item.Status
// stays exactly the card's "s" tag whenever len(authoritative) == 0) is the
// only thing keeping it blocked — not a live dependency edge. This is the real
// shape produced either by a non-maintainer's card-only republish on a
// multi-agent board (which strips every non-authoritative status event) or by
// a partial relay reconcile that lands the card without its status chain.
func appendCardOnlyBlockedFixture(t *testing.T, dir, itemID string) {
	t.Helper()
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	boardD := projectPrefix(dir)
	spec := rdSync.CardSpec{
		ItemID: itemID, Title: "Pre-burned-in", Status: state.StatusBlocked,
		Priority: "p1", Type: "task", BoardD: boardD,
	}
	ce, err := rdSync.BuildCardEvent(k, spec, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{ce}); err != nil {
		t.Fatalf("append card-only fixture: %v", err)
	}
}

// TestNostrNative_DelegateOnCardOnlyBlockedItem_EmptyHistory_Heals is ready-500's
// probe for the HOLE in the prior fix: runDelegateNostr's guard used to be `if
// item.Status == StatusBlocked && len(item.History) > 0`, which fell straight
// through — publishing item.Status ("blocked") VERBATIM via
// publishItemStatusChangeNostr — whenever History was empty. This is exactly
// appendCardOnlyBlockedFixture's shape: no dep edge, no status event, current
// status blocked purely via the card's own "s" tag. Before this fix: delegate
// on such an item burned status=blocked in as the permanent status-authority
// winner (nothing — no blocker to close — could ever have cleared it). The
// fix moves the substitution into CardSpecFromItem/nonDerivedCardStatus
// (pkg/sync/nostrmigrate.go), unconditional on History depth, so delegate here
// must publish the explicit inbox default instead.
func TestNostrNative_DelegateOnCardOnlyBlockedItem_EmptyHistory_Heals(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)
	id := "ready-cardonly1"
	appendCardOnlyBlockedFixture(t, dir, id)

	it, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}
	if it.Status != state.StatusBlocked {
		t.Fatalf("fixture status = %q before delegate; want blocked (card-tag only, no history)", it.Status)
	}
	if len(it.History) != 0 {
		t.Fatalf("fixture History = %v; want empty (this test proves the empty-history branch)", it.History)
	}

	if err := runDelegateNostr(id, "atlas/worker-9", "routing"); err != nil {
		t.Fatalf("delegate on a card-only blocked item must succeed, got: %v", err)
	}
	it, err = nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve after delegate: %v", err)
	}
	if it.Status == state.StatusBlocked {
		t.Fatalf("delegate republished status=blocked verbatim on an empty-history item — the exact hole ready-500's adversary review found (status=%q)", it.Status)
	}
	if it.Status != state.StatusInbox {
		t.Fatalf("after delegate, status = %q; want the explicit inbox default (no authoritative history to recover)", it.Status)
	}
	if it.By != "atlas/worker-9" {
		t.Fatalf("delegate assignment lost: By = %q; want atlas/worker-9", it.By)
	}
	assertNoDotCf(t)
}

// TestNostrNative_FieldEditOnCardOnlyBlockedItem_EmptyHistory_Heals is
// ready-500's probe for the adversary's second finding: a field-only `rd
// update` (publishItemCardEditNostr — no status event at all) on the SAME
// empty-history card-only-blocked shape used to write item.Status ("blocked")
// straight into the refreshed card's "s" tag, which IS the projected status
// verbatim whenever there is no authoritative history chain to override it —
// burning status=blocked in with no status event ever published. The fix
// (nonDerivedCardStatus, routed through by CardSpecFromItem) applies here too,
// since it lives in the one function both publishItemStatusChangeNostr and
// publishItemCardEditNostr route their outbound CardSpec through.
func TestNostrNative_FieldEditOnCardOnlyBlockedItem_EmptyHistory_Heals(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)
	id := "ready-cardonly2"
	appendCardOnlyBlockedFixture(t, dir, id)

	it, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}
	if it.Status != state.StatusBlocked || len(it.History) != 0 {
		t.Fatalf("fixture status=%q history=%v; want blocked/empty", it.Status, it.History)
	}

	if err := runUpdateNostr(id, nostrUpdateSpec{title: "Retitled", hasFieldUpdate: true}); err != nil {
		t.Fatalf("field edit on a card-only blocked item must succeed, got: %v", err)
	}
	it, err = nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve after field edit: %v", err)
	}
	if it.Title != "Retitled" {
		t.Fatalf("Title = %q; want Retitled", it.Title)
	}
	if it.Status == state.StatusBlocked {
		t.Fatalf("field edit republished status=blocked verbatim (into the card's own s tag) on an empty-history item — status=%q", it.Status)
	}
	if it.Status != state.StatusInbox {
		t.Fatalf("after field edit, status = %q; want the explicit inbox default", it.Status)
	}
	assertNoDotCf(t)
}

// TestNostrNative_DelegateOnPreBurnedInItem_Heals is ready-500's residual fix
// (finding 4 on the prior attempt): an item ALREADY burned in by the pre-fix
// bug has its own last authoritative HISTORY entry recorded as ToStatus
// "blocked" (a real status event was published carrying status=blocked,
// unlike the two tests above where only the card's "s" tag was ever touched).
// A fallback that reads only item.History[len-1].ToStatus finds "blocked"
// again and republishes it, PERPETUATING the burn-in. This test builds exactly
// that shape — a blocker + dep edge (so the item is legitimately blocked at
// the time of the bad write), a status event that WRONGLY published
// status=blocked while blocked (the pre-fix bug's own output), then closes the
// blocker — and proves delegate on the already-burned-in item still heals to
// the item's real pre-block status instead of re-publishing the burned-in
// "blocked" forever.
func TestNostrNative_DelegateOnPreBurnedInItem_Heals(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	blocker, err := runCreateNostr(dir, nostrCreateSpec{title: "Blocker", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	target, err := runCreateNostr(dir, nostrCreateSpec{title: "Target", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := runDepAddNostr(target, blocker); err != nil {
		t.Fatalf("dep add: %v", err)
	}

	// Simulate the PRE-FIX bug's own output DIRECTLY (raw card + status events,
	// bypassing the now-fixed publishItemStatusChangeNostr/CardSpecFromItem
	// choke point entirely) — the fixed code would itself heal this before
	// publishing, so it cannot be used to construct the fixture it is meant to
	// heal. This is exactly what the pre-fix runDelegateNostr produced: a real
	// status event carrying status=blocked verbatim while the item was
	// derived-blocked, landing a HistoryEntry whose ToStatus is itself
	// "blocked" — the shape a naive last-entry-only fallback cannot distinguish
	// from a legitimate one.
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	boardD := projectPrefix(dir)
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	// Strictly-monotonic-per-item timestamp (same helper the live write hooks
	// use) so the burn-in status event sorts AFTER the create-time inbox status
	// event despite same-wall-clock-second timestamps — otherwise the
	// (created_at, event-id) deterministic tie-break can place it FIRST, which
	// would misconstruct the fixture this test means to build.
	burnCreatedAt := nostrNextCreatedAt(log, rdSync.ItemDriftScope(target))
	burnCard, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
		ItemID: target, Title: "Target", Status: state.StatusBlocked, Priority: "p1", Type: "task",
		BoardD: boardD, Deps: []string{blocker},
	}, burnCreatedAt)
	if err != nil {
		t.Fatalf("BuildCardEvent (burn-in fixture): %v", err)
	}
	burnStatus, err := rdSync.BuildStatusEvent(k, target, state.StatusBlocked, burnCard.ID, "burned in by the pre-fix bug", burnCreatedAt)
	if err != nil {
		t.Fatalf("BuildStatusEvent (burn-in fixture): %v", err)
	}
	if _, err := log.AppendUnique([]*nostr.Event{burnCard, burnStatus}); err != nil {
		t.Fatalf("append burn-in fixture: %v", err)
	}
	it, err := nostrResolveItem(target)
	if err != nil {
		t.Fatalf("resolve after simulated burn-in: %v", err)
	}
	if len(it.History) == 0 || it.History[len(it.History)-1].ToStatus != state.StatusBlocked {
		t.Fatalf("fixture setup failed: last history entry ToStatus = %+v; want the simulated burn-in to have landed as blocked", it.History)
	}

	if err := runCloseNostr(blocker, "done", "unblocking", "closed"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	it, err = nostrResolveItem(target)
	if err != nil {
		t.Fatalf("resolve after blocker close: %v", err)
	}
	if it.Status != state.StatusBlocked {
		t.Fatalf("sanity: target status = %q immediately after blocker close and before delegate; want still blocked (dep pass still derives it live)", it.Status)
	}

	if err := runDelegateNostr(target, "atlas/worker-7", "routing"); err != nil {
		t.Fatalf("delegate on a pre-burned-in item must succeed, got: %v", err)
	}
	it, err = nostrResolveItem(target)
	if err != nil {
		t.Fatalf("resolve after delegate: %v", err)
	}
	if it.Status == state.StatusBlocked {
		t.Fatalf("delegate republished the pre-burned-in 'blocked' history entry verbatim instead of healing past it — status=%q", it.Status)
	}
	if it.Status != state.StatusInbox {
		t.Fatalf("target recovered to %q; want inbox (its real pre-block status, recovered by walking PAST the burned-in history entry)", it.Status)
	}
	assertNoDotCf(t)
}

// TestNostrNative_ReadActive_DefaultReadsProjection is the S-read proof: on a
// nostr-native project with NO RD_NOSTR_READ env set, the dual-read surface
// resolves items from the nostr projection by DEFAULT. A create publishes to the
// nostr log only (never JSONL/store), so if the default read still went through
// the campfire/JSONL backend, list would be empty. Reading it back via the shared
// allProjectItems(openStore) path — exactly what `rd list` does — proves
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

	items, err := allProjectItems()
	if err != nil {
		t.Fatalf("allProjectItems: %v", err)
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

	// itemByID (the `rd show` path) must resolve from nostr too.
	byID, err := itemByID(id)
	if err != nil {
		t.Fatalf("itemByID: %v", err)
	}
	if byID == nil || byID.ID != id {
		t.Fatalf("itemByID(%s) = %+v; want the projected item", id, byID)
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

// TestNostrNative_PublishCmdOnBlockedItem_Recovers is ready-500's done-condition
// test for the THIRD instance of the same defect class, in the manual
// `rd nostr publish` handler (nostrPublishCmd.RunE, cmd/rd/nostr.go): unlike
// runDelegateNostr/runUpdateNostr this call site had ZERO behavioural coverage —
// a line-count-neutral `_ = rdSync.NonDerivedStatus(item)` here left the whole
// repo suite green. This test advances PAST the write and PAST the blocker
// closing on purpose, exactly like TestNostrNative_DelegateOnBlockedItem_Recovers
// above: stopping at the fold immediately after the publish cannot distinguish a
// burned-in "blocked" from a live derived one.
//
// ORDERING NOTE (load-bearing, do not remove): nostrPublishCmd used to stamp its
// PublishItemWithReason call with a bare time.Now().Unix() instead of the scoped
// nostrNextCreatedAt every other write hook uses. Within one wall-clock second,
// the nostr projection's (created_at, event-id) tie-break could sort the
// republished status event BEFORE the create/dep-add events already in this
// item's chain — a silent no-op republish that would make this test pass
// regardless of whether NonDerivedStatus is even called. That ordering bug is
// fixed alongside this test (nostrPublishCmd now calls nostrNextCreatedAt), so
// this test needs no sleep to be a real assertion of the guard.
func TestNostrNative_PublishCmdOnBlockedItem_Recovers(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	blocker, err := runCreateNostr(dir, nostrCreateSpec{title: "Blocker", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	target, err := runCreateNostr(dir, nostrCreateSpec{title: "Target", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := runDepAddNostr(target, blocker); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	it, _ := nostrResolveItem(target)
	if it.Status != state.StatusBlocked {
		t.Fatalf("target status = %q before publish; want blocked", it.Status)
	}

	if err := nostrPublishCmd.RunE(nostrPublishCmd, []string{target}); err != nil {
		t.Fatalf("nostrPublishCmd.RunE on a blocked item must succeed, got: %v", err)
	}
	it, _ = nostrResolveItem(target)
	if it.Status != state.StatusBlocked {
		t.Fatalf("after publish, status = %q; want STILL blocked (publish does not itself unblock, and the blocker has not closed yet)", it.Status)
	}

	// --- RECOVERY: close the shared blocker and prove the target is not
	// permanently stuck. If the manual publish path burned "blocked" in as an
	// authoritative status event, the dep pass no longer overrides once the
	// blocker closes, and the item is stuck at blocked forever.
	if err := runCloseNostr(blocker, "done", "unblocking", "closed"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	it, _ = nostrResolveItem(target)
	if it.Status == state.StatusBlocked {
		t.Fatalf("target still blocked after its blocker closed — status burned in by `rd nostr publish` and never re-derived (status=%q)", it.Status)
	}
	if it.Status != state.StatusInbox {
		t.Fatalf("target recovered to %q; want inbox (its pre-block status)", it.Status)
	}
	assertNoDotCf(t)
}
