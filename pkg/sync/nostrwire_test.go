package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
)

func testKey(t *testing.T) *nostr.Key {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func findTag(tags [][]string, name string) (string, bool) {
	for _, tg := range tags {
		if len(tg) >= 2 && tg[0] == name {
			return tg[1], true
		}
	}
	return "", false
}

// TestWireMapping_Card asserts the 30302 card carries the mapped tags
// (d,title,a,s,rank,p) and that the event verifies (canonical id + schnorr).
func TestWireMapping_Card(t *testing.T) {
	k := testKey(t)
	card := CardSpec{
		ItemID:   "ready-a13",
		Title:    "round-trip keystone",
		Status:   state.StatusActive,
		Priority: "p1",
		Type:     "task",
		Assignee: k.PubKeyHex(),
		Context:  "the description <>&\"",
		BoardD:   "ready",
	}
	e, err := BuildCardEvent(k, card, 1700000000)
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	if e.Kind != KindCard {
		t.Fatalf("kind = %d, want %d", e.Kind, KindCard)
	}
	if e.CreatedAt != 1700000000 {
		t.Fatalf("created_at not preserved at second granularity: %d", e.CreatedAt)
	}
	for name, want := range map[string]string{
		"d":        "ready-a13",
		"title":    "round-trip keystone",
		"s":        state.StatusActive,
		"rank":     "p1",
		"priority": "p1",
		"itype":    "task",
		"p":        k.PubKeyHex(),
		"a":        BoardCoord(k.PubKeyHex(), "ready"),
	} {
		got, ok := findTag(e.Tags, name)
		if !ok || got != want {
			t.Errorf("card tag %q = %q (present=%v), want %q", name, got, ok, want)
		}
	}
	if e.Content != card.Context {
		t.Errorf("content = %q, want %q", e.Content, card.Context)
	}
	if err := e.Verify(); err != nil {
		t.Fatalf("card event does not verify: %v", err)
	}
}

// TestWireMapping_StatusKind checks the NIP-34 kind mapping AND that the exact rd
// status is preserved in the status tag (so waiting/blocked survive).
func TestWireMapping_StatusKind(t *testing.T) {
	k := testKey(t)
	cases := []struct {
		rdStatus string
		wantKind int
	}{
		{state.StatusInbox, KindStatusOpen},
		{state.StatusActive, KindStatusOpen},
		{state.StatusWaiting, KindStatusOpen},
		{state.StatusBlocked, KindStatusOpen},
		{state.StatusDone, KindStatusResolved},
		{state.StatusCancelled, KindStatusClosed},
		{state.StatusFailed, KindStatusClosed},
	}
	for _, c := range cases {
		e, err := BuildStatusEvent(k, "ready-a13", c.rdStatus, "cardid", "reason text", 1700000001)
		if err != nil {
			t.Fatalf("build status %s: %v", c.rdStatus, err)
		}
		if e.Kind != c.wantKind {
			t.Errorf("status %q -> kind %d, want %d", c.rdStatus, e.Kind, c.wantKind)
		}
		if got, _ := findTag(e.Tags, "status"); got != c.rdStatus {
			t.Errorf("status tag = %q, want %q (exact rd status must survive)", got, c.rdStatus)
		}
		if got, _ := findTag(e.Tags, "a"); got != CardCoord(k.PubKeyHex(), "ready-a13") {
			t.Errorf("status a-coord = %q, want card coord", got)
		}
		if e.Content != "reason text" {
			t.Errorf("close-with-reason lost: content = %q", e.Content)
		}
		if err := e.Verify(); err != nil {
			t.Fatalf("status event does not verify: %v", err)
		}
	}
}

func TestWireMapping_Board(t *testing.T) {
	k := testKey(t)
	e, err := BuildBoardEvent(k, BoardSpec{BoardD: "ready", Title: "Ready", Maintainers: []string{k.PubKeyHex()}}, 1700000000)
	if err != nil {
		t.Fatalf("build board: %v", err)
	}
	if e.Kind != KindBoard {
		t.Fatalf("board kind = %d, want %d", e.Kind, KindBoard)
	}
	if d, _ := findTag(e.Tags, "d"); d != "ready" {
		t.Errorf("board d = %q, want ready", d)
	}
	if p, _ := findTag(e.Tags, "p"); p != k.PubKeyHex() {
		t.Errorf("board maintainer p = %q", p)
	}
}

// TestLog_RoundTrip proves append -> read replays events in order and survives a
// wipe (missing file => empty, no error), and AppendUnique dedupes by id.
func TestLog_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	log := NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile))

	// Missing file reads as empty.
	if evs, err := log.ReadAll(); err != nil || len(evs) != 0 {
		t.Fatalf("empty log: evs=%d err=%v", len(evs), err)
	}

	k := testKey(t)
	c, _ := BuildCardEvent(k, CardSpec{ItemID: "x-1", Title: "t", Status: state.StatusInbox, BoardD: "x"}, 1700000000)
	s, _ := BuildStatusEvent(k, "x-1", state.StatusInbox, c.ID, "", 1700000000)
	if err := log.Append(c); err != nil {
		t.Fatalf("append card: %v", err)
	}
	if err := log.Append(s); err != nil {
		t.Fatalf("append status: %v", err)
	}
	evs, err := log.ReadAll()
	if err != nil || len(evs) != 2 {
		t.Fatalf("readall: evs=%d err=%v", len(evs), err)
	}
	if evs[0].ID != c.ID || evs[1].ID != s.ID {
		t.Fatalf("append order not preserved")
	}
	// AppendUnique dedupes: re-appending c adds nothing.
	added, err := log.AppendUnique([]*nostr.Event{c, s})
	if err != nil || added != 0 {
		t.Fatalf("AppendUnique dedupe: added=%d err=%v", added, err)
	}
}

// TestProjection_LatestWinsAndStatusAuthority is the core replay proof:
//   - card latest-wins (a later card supersedes the earlier one)
//   - status authority: newest status from the author wins; a NEWER status from a
//     non-author/non-maintainer pubkey is IGNORED.
func TestProjection_LatestWinsAndStatusAuthority(t *testing.T) {
	author := testKey(t)
	intruder := testKey(t)

	// create card @ t0 (inbox), then a status open @ t0.
	c0, _ := BuildCardEvent(author, CardSpec{ItemID: "ready-a13", Title: "v1", Status: state.StatusInbox, Priority: "p2", BoardD: "ready"}, 1700000000)
	s0, _ := BuildStatusEvent(author, "ready-a13", state.StatusInbox, c0.ID, "", 1700000000)
	// later card @ t1 (title v2, priority p1).
	c1, _ := BuildCardEvent(author, CardSpec{ItemID: "ready-a13", Title: "v2", Status: state.StatusActive, Priority: "p1", BoardD: "ready"}, 1700000100)
	// author moves it to done @ t2, with a close reason.
	s1, _ := BuildStatusEvent(author, "ready-a13", state.StatusDone, c1.ID, "shipped keystone", 1700000200)
	// intruder tries to reopen @ t3 (NEWER) — must be ignored (not author/maintainer).
	sBad, _ := BuildStatusEvent(intruder, "ready-a13", state.StatusActive, c1.ID, "hijack", 1700000300)

	events := []*nostr.Event{c0, s0, c1, s1, sBad}
	items := ProjectItems(events, ProjectOptions{Maintainers: map[string]bool{author.PubKeyHex(): true}})

	it, ok := items["ready-a13"]
	if !ok {
		t.Fatalf("item not projected")
	}
	if it.Title != "v2" {
		t.Errorf("latest-wins card failed: title = %q, want v2", it.Title)
	}
	if it.Priority != "p1" {
		t.Errorf("latest-wins card failed: priority = %q, want p1", it.Priority)
	}
	if it.Status != state.StatusDone {
		t.Errorf("status-authority failed: status = %q, want done (intruder reopen must be ignored)", it.Status)
	}
	// close reason preserved in history.
	foundReason := false
	for _, h := range it.History {
		if h.Note == "shipped keystone" {
			foundReason = true
		}
	}
	if !foundReason {
		t.Errorf("close-with-reason not preserved in history")
	}
}

// TestProjection_RejectsTampered proves a tampered log line cannot influence the
// projection (read-side trust gate).
func TestProjection_RejectsTampered(t *testing.T) {
	author := testKey(t)
	c, _ := BuildCardEvent(author, CardSpec{ItemID: "ready-a13", Title: "real", Status: state.StatusActive, BoardD: "ready"}, 1700000000)
	// Tamper the title tag after signing — id/sig no longer match.
	bad := *c
	bad.Tags = append([][]string{}, c.Tags...)
	for i := range bad.Tags {
		if bad.Tags[i][0] == "title" {
			bad.Tags[i] = []string{"title", "forged"}
		}
	}
	items := ProjectItems([]*nostr.Event{&bad}, ProjectOptions{})
	if _, ok := items["ready-a13"]; ok {
		t.Fatalf("tampered event was projected — trust gate broken")
	}
	// The genuine event still projects.
	items = ProjectItems([]*nostr.Event{c}, ProjectOptions{})
	if items["ready-a13"].Title != "real" {
		t.Fatalf("genuine event failed to project")
	}
}

// TestPublisher_OfflineBuffers proves the relay-offline path: with NO reachable
// write relays, PublishItem still appends to the authoritative log (durable) and
// buffers to the pending file, returning success. This is the "must work with
// every relay offline" guarantee.
func TestPublisher_OfflineBuffers(t *testing.T) {
	dir := t.TempDir()
	k := testKey(t)
	logPath := filepath.Join(dir, ".ready", NostrLogFile)
	pendingPath := filepath.Join(dir, ".ready", NostrPendingFile)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(logPath),
		WriteRelays: []string{"ws://127.0.0.1:1"}, // guaranteed-unreachable
		PendingPath: pendingPath,
		Timeout:     500_000_000, // 0.5s so the test is fast
	}
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{ItemID: "ready-a13", Title: "offline", Status: state.StatusInbox, Priority: "p1", BoardD: "ready"}
	res, err := pub.PublishItem(context.Background(), &board, card, 1700000000)
	if err != nil {
		t.Fatalf("publish (offline) should not fail: %v", err)
	}
	if !res.Buffered {
		t.Errorf("expected events to be buffered when no relay reachable")
	}
	// Authoritative log must contain the 3 events regardless of relay.
	evs, err := NewNostrLog(logPath).ReadAll()
	if err != nil || len(evs) != 3 {
		t.Fatalf("log should hold board+card+status offline: evs=%d err=%v", len(evs), err)
	}
	// Read-back from the LOCAL log alone reconstructs current state.
	items := ProjectItems(evs, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	if items["ready-a13"].Status != state.StatusInbox || items["ready-a13"].Title != "offline" {
		t.Fatalf("relay-offline local-log read-back mismatch: %+v", items["ready-a13"])
	}
	// Pending buffer written.
	if _, err := os.Stat(pendingPath); err != nil {
		t.Errorf("pending buffer not written: %v", err)
	}
}
