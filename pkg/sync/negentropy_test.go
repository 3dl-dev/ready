package sync

import (
	"path/filepath"
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
)

func mustKey(t *testing.T) *nostr.Key {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return k
}

func mustCard(t *testing.T, k *nostr.Key, id, status string, ts int64) *nostr.Event {
	t.Helper()
	e, err := BuildCardEvent(k, CardSpec{ItemID: id, Title: id, Status: status, Type: "task", BoardD: "ready"}, ts)
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	return e
}

func TestMatchesFilter(t *testing.T) {
	k := mustKey(t)
	card := mustCard(t, k, "ready-x1", state.StatusActive, 1000)

	// kinds + authors match.
	if !matchesFilter(card, map[string]any{"kinds": []int{KindCard}, "authors": []string{k.PubKeyHex()}}) {
		t.Error("expected card to match kinds+authors filter")
	}
	// wrong kind.
	if matchesFilter(card, map[string]any{"kinds": []int{KindBoard}}) {
		t.Error("card should not match board-kind filter")
	}
	// wrong author.
	if matchesFilter(card, map[string]any{"authors": []string{"deadbeef"}}) {
		t.Error("card should not match wrong-author filter")
	}
	// #d tag match / mismatch.
	if !matchesFilter(card, map[string]any{"#d": []string{"ready-x1"}}) {
		t.Error("card should match #d=ready-x1")
	}
	if matchesFilter(card, map[string]any{"#d": []string{"ready-x2"}}) {
		t.Error("card should not match #d=ready-x2")
	}
	// ids match via []any (JSON-decoded shape).
	if !matchesFilter(card, map[string]any{"ids": []any{card.ID}}) {
		t.Error("card should match its own id via []any")
	}
	// empty filter matches everything.
	if !matchesFilter(card, map[string]any{}) {
		t.Error("empty filter should match")
	}
}

func TestBoardSyncFilterOmitsCoordWhenEmpty(t *testing.T) {
	f := BoardSyncFilter("", []string{"aa"})
	if _, ok := f["#a"]; ok {
		t.Error("empty boardCoord must not add #a (would exclude status events)")
	}
	kinds, _ := f["kinds"].([]int)
	if len(kinds) == 0 {
		t.Fatal("expected kinds in filter")
	}
	// Must include both card and status kinds.
	var hasCard, hasStatus bool
	for _, k := range kinds {
		if k == KindCard {
			hasCard = true
		}
		if k == KindStatusResolved {
			hasStatus = true
		}
	}
	if !hasCard || !hasStatus {
		t.Errorf("filter must include card+status kinds, got %v", kinds)
	}
}

// TestBoardSyncFilterMatchesCardAndStatusEvents is the ready-7ec proof: a
// board-scoped negentropy filter (BoardSyncFilter(boardCoord, nil)) must match
// BOTH the board's 30302 card AND its accompanying NIP-34 status event — before
// this change it matched ONLY the card, because BuildStatusEvent's "a" tag was
// always the CARD's own coordinate (30302:<signer>:<itemID>), never the board's
// (30301:<owner>:<boardD>). A naive board-scoped sync therefore silently dropped
// every status transition (claim/close/cancel), breaking NIP-34 status
// convergence for anything but the author-scoped, whole-portfolio filter.
//
// This test proves the fix two ways: (1) the NEW status event shape (built via
// BuildStatusEventWithIssueRoot with the board coordinate threaded through, the
// same helper cardBoardCoord/PublishItemWithReason/PublishStatusChange now use)
// matches the board filter, and (2) the OLD shape (BuildStatusEvent alone, no
// board tag — reproducing pre-ready-7ec behaviour exactly) does NOT match it —
// i.e. this is a real regression test, not a tautology that would pass either way.
func TestBoardSyncFilterMatchesCardAndStatusEvents(t *testing.T) {
	k := mustKey(t)
	boardD := "ready-7ec"
	boardCoord := BoardCoord(k.PubKeyHex(), boardD)
	itemID := "ready-7ec-x1"

	card, err := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "x1", Status: state.StatusActive, Type: "task", BoardD: boardD}, 1000)
	if err != nil {
		t.Fatalf("build card: %v", err)
	}

	// NEW shape: status event additively carries the board coordinate.
	newStatus, err := BuildStatusEventWithIssueRoot(k, itemID, state.StatusDone, card.ID, "", boardCoord, "shipped", 1001)
	if err != nil {
		t.Fatalf("build status (with board): %v", err)
	}

	// OLD shape: status event carries ONLY the card coordinate (pre-ready-7ec).
	oldStatus, err := BuildStatusEvent(k, itemID, state.StatusDone, card.ID, "shipped", 1001)
	if err != nil {
		t.Fatalf("build status (old shape): %v", err)
	}

	filter := BoardSyncFilter(boardCoord, nil)

	if !matchesFilter(card, filter) {
		t.Error("expected the card to match its own board-scoped filter")
	}
	if !matchesFilter(newStatus, filter) {
		t.Error("BUG: board-scoped filter should match a status event carrying the board 'a' tag (ready-7ec fix), but it did not")
	}
	if matchesFilter(oldStatus, filter) {
		t.Error("sanity check failed: the pre-fix status event shape (no board tag) unexpectedly matched the board filter")
	}

	// The card-coordinate anchor at tag position 0 must be UNCHANGED by the
	// additive board tag — rd's own projection (tagValue, first-match) and the
	// ready-da7 issue anchor depend on this.
	if got, want := newStatus.Tags[0], ([]string{"a", CardCoord(k.PubKeyHex(), itemID)}); got[0] != want[0] || got[1] != want[1] {
		t.Errorf("card-coordinate anchor must remain the FIRST tag, got %v", newStatus.Tags[0])
	}
}

// TestMergeFromDegradeFloor proves the relay-free degrade floor: machine B merges
// machine A's committed JSONL log, gaining A's events, idempotently and with a
// verify gate that rejects forged lines.
func TestMergeFromDegradeFloor(t *testing.T) {
	kA := mustKey(t)
	kB := mustKey(t)
	dir := t.TempDir()

	logA := NewNostrLog(filepath.Join(dir, "a", "nostr-log.jsonl"))
	logB := NewNostrLog(filepath.Join(dir, "b", "nostr-log.jsonl"))

	// A authors two cards; B authors one.
	for _, e := range []*nostr.Event{
		mustCard(t, kA, "ready-a1", state.StatusActive, 1000),
		mustCard(t, kA, "ready-a2", state.StatusActive, 1001),
	} {
		if err := logA.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := logB.Append(mustCard(t, kB, "ready-b1", state.StatusActive, 2000)); err != nil {
		t.Fatal(err)
	}

	// B merges A's committed log (the git-JSONL fallback). A is an ADMITTED machine
	// (ready-b57): both keys are in B's trust set, so A's log merges in full.
	trust := map[string]bool{kA.PubKeyHex(): true, kB.PubKeyHex(): true}
	added, err := logB.MergeFrom(logA.Path(), trust)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if added != 2 {
		t.Fatalf("expected 2 events merged, got %d", added)
	}
	// Idempotent: a second merge adds nothing.
	added2, err := logB.MergeFrom(logA.Path(), trust)
	if err != nil {
		t.Fatal(err)
	}
	if added2 != 0 {
		t.Fatalf("second merge should add 0, got %d", added2)
	}
	// B now replays to all three items with zero relay involvement.
	evs, _ := logB.ReadAll()
	items := ProjectItems(evs, ProjectOptions{})
	for _, id := range []string{"ready-a1", "ready-a2", "ready-b1"} {
		if items[id] == nil {
			t.Errorf("degrade-floor merge missing item %s", id)
		}
	}

	// Verify gate: a tampered event in the source log is NOT merged.
	tampered := mustCard(t, kA, "ready-a3", state.StatusActive, 1002)
	tampered.Content = "forged after signing"
	forgedLog := NewNostrLog(filepath.Join(dir, "forged", "nostr-log.jsonl"))
	if err := forgedLog.Append(tampered); err != nil {
		t.Fatal(err)
	}
	addedF, err := logB.MergeFrom(forgedLog.Path(), trust)
	if err != nil {
		t.Fatal(err)
	}
	if addedF != 0 {
		t.Errorf("tampered event must be rejected by the verify gate, merged %d", addedF)
	}
}

// TestMergeFrom_TrustGate_RejectsUntrustedAuthor proves ready-b57 fix (1) for the
// git-JSONL degrade floor: a VALIDLY-SIGNED event authored by a key that is NOT in
// the trust set is REJECTED from the local authoritative log — not merely re-gated
// later at projection. The event verifies (real signature), so the pre-b57 Verify-
// only gate would have admitted it and poisoned the log. With the web-of-trust
// author gate on MergeFrom it never lands.
func TestMergeFrom_TrustGate_RejectsUntrustedAuthor(t *testing.T) {
	kSelf := mustKey(t)
	kIntruder := mustKey(t)
	dir := t.TempDir()

	logLocal := NewNostrLog(filepath.Join(dir, "local", "nostr-log.jsonl"))
	logForeign := NewNostrLog(filepath.Join(dir, "foreign", "nostr-log.jsonl"))

	// The foreign log carries a perfectly-valid, correctly-SIGNED card from an
	// UNADMITTED key.
	intruderCard := mustCard(t, kIntruder, "ready-b57-intruder", state.StatusActive, 1000)
	if err := intruderCard.Verify(); err != nil {
		t.Fatalf("precondition: intruder event must verify (real signature): %v", err)
	}
	if err := logForeign.Append(intruderCard); err != nil {
		t.Fatal(err)
	}

	// Trust set = self only (kIntruder is NOT admitted).
	trust := map[string]bool{kSelf.PubKeyHex(): true}
	added, err := logLocal.MergeFrom(logForeign.Path(), trust)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if added != 0 {
		t.Fatalf("untrusted-author event must be REJECTED at merge, got added=%d", added)
	}
	// The local authoritative log must be empty — the event never landed (rejected at
	// ingestion, not just filtered at projection).
	evs, err := logLocal.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("untrusted event poisoned the local log: %d events present", len(evs))
	}

	// Admitting the intruder key lets its log merge (proves the gate is the ONLY
	// thing that was blocking it — not some unrelated rejection).
	trust[kIntruder.PubKeyHex()] = true
	added2, err := logLocal.MergeFrom(logForeign.Path(), trust)
	if err != nil {
		t.Fatal(err)
	}
	if added2 != 1 {
		t.Fatalf("once admitted, the same event must merge, got added=%d", added2)
	}
}

// TestAdmitDownloaded_TrustGate is the deterministic proof of ready-b57 fix (1),
// NEGENTROPY DOWNLOAD path: the client-side admission gate NegentropySync applies to
// everything a relay serves for a `need` set. A validly-signed event from an
// UNTRUSTED author is dropped BEFORE AppendUnique (not merely re-gated at
// projection), while a tampered event and a trusted event behave as expected. This
// exercises the exact filter NegentropySync uses; the live end-to-end variant is
// TestLiveRelay_NegentropyDownloadTrustGate (env-gated; blocked in CI-less envs only
// by a pre-existing relay REQ-by-ids timeout unrelated to this gate).
func TestAdmitDownloaded_TrustGate(t *testing.T) {
	kTrusted := mustKey(t)
	kUntrusted := mustKey(t)

	trustedCard := mustCard(t, kTrusted, "ready-b57-dl-ok", state.StatusActive, 1000)
	untrustedCard := mustCard(t, kUntrusted, "ready-b57-dl-bad", state.StatusActive, 1000)
	tampered := mustCard(t, kTrusted, "ready-b57-dl-tampered", state.StatusActive, 1001)
	tampered.Content = "forged after signing" // breaks Verify

	fetched := []*nostr.Event{trustedCard, untrustedCard, tampered, nil}
	trust := map[string]bool{kTrusted.PubKeyHex(): true}

	merge, bytes := admitDownloaded(fetched, trust)
	if len(merge) != 1 || merge[0].ID != trustedCard.ID {
		t.Fatalf("gate must admit exactly the trusted, verifiable event; got %d events %+v", len(merge), merge)
	}
	if bytes <= 0 {
		t.Fatalf("admitted event must contribute wire bytes, got %d", bytes)
	}
	// Untrusted-author admission is what the pre-b57 Verify-only download allowed.
	for _, e := range merge {
		if e.PubKey == kUntrusted.PubKeyHex() {
			t.Fatal("untrusted-author event admitted via negentropy download")
		}
	}

	// Nil trust set disables the gate (legacy/test path): both verifiable events pass,
	// the tampered one still fails Verify.
	merge2, _ := admitDownloaded(fetched, nil)
	if len(merge2) != 2 {
		t.Fatalf("nil trust set must admit both verifiable events (gate disabled), got %d", len(merge2))
	}
}

// TestPendingBufferMechanics checks the offline buffer read/rewrite: rewriting
// with a subset keeps only those events; rewriting empty removes the file.
func TestPendingBufferMechanics(t *testing.T) {
	k := mustKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nostr-pending.jsonl")

	e1 := mustCard(t, k, "ready-p1", state.StatusInbox, 1)
	e2 := mustCard(t, k, "ready-p2", state.StatusInbox, 2)
	e3 := mustCard(t, k, "ready-p3", state.StatusInbox, 3)
	if err := appendPendingEvent(path, e1); err != nil {
		t.Fatal(err)
	}
	if err := appendPendingEvent(path, e2); err != nil {
		t.Fatal(err)
	}
	if err := appendPendingEvent(path, e3); err != nil {
		t.Fatal(err)
	}

	got, err := readPendingEvents(path)
	if err != nil || len(got) != 3 {
		t.Fatalf("read pending: n=%d err=%v", len(got), err)
	}

	// Keep only e2 (simulate e1,e3 flushed).
	if err := rewritePendingEvents(path, []*nostr.Event{e2}); err != nil {
		t.Fatal(err)
	}
	got, _ = readPendingEvents(path)
	if len(got) != 1 || got[0].ID != e2.ID {
		t.Fatalf("expected only e2 to remain, got %d", len(got))
	}

	// Empty rewrite removes the file.
	if err := rewritePendingEvents(path, nil); err != nil {
		t.Fatal(err)
	}
	got, err = readPendingEvents(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("expected empty buffer, n=%d err=%v", len(got), err)
	}
}
