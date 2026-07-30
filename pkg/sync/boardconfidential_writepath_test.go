package sync

// The WRITE half of §11.13a's owner-signed cutover assertion (ready-475 REWORK):
// Publisher.buildBoardDefinition, the single choke point through which the item
// write path materializes a board's own kind-30301 definition.
//
// WHY THIS LAYER GETS ITS OWN TESTS. cmd/rd/board_confidential_since_test.go
// proves the three CLI paths that reach it (`rd create`, `rd nostr publish`,
// `rd nostr put`) each keep the assertion. What it cannot prove is the property
// that makes a FOURTH caller safe without anybody remembering: that the guard
// lives in PublishItem itself and holds for any caller, with any BoardSpec, and
// fails CLOSED rather than silently publishing an unasserted definition when it
// cannot establish what the current assertion is.
//
// A kind-30301 is addressable, so a definition republished without the tag
// REPLACES the asserted one on every conformant relay — which is why "cannot
// establish it" must refuse the write rather than guess.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// bwPublisher returns a Publisher writing to a fresh log under dir, with no
// relays configured: every assertion below is against the local authoritative
// log (§16.1), which is where the durability guarantee lives anyway.
func bwPublisher(k *nostr.Key, logPath string) *Publisher {
	return &Publisher{Key: k, Log: NewNostrLog(logPath)}
}

// TestPublishItemCarriesTheAssertionForwardForAnyCaller pins the choke point: a
// caller that hands PublishItem a BoardSpec built from NOTHING — which is exactly
// what cmd/rd's boardSpecForProject does, from the project directory name — still
// republishes a definition carrying the board's current assertion, because the
// value comes from the log rather than from the caller.
func TestPublishItemCarriesTheAssertionForwardForAnyCaller(t *testing.T) {
	k := kdKey(t)
	const boardD = "bw-board"
	const since = int64(1784206981)
	coord := BoardCoord(k.PubKeyHex(), boardD)
	pub := bwPublisher(k, filepath.Join(t.TempDir(), ".ready", NostrLogFile))

	// The owner asserts the cutover, exactly as `rd board confidential-since` does.
	asserted, err := BuildBoardEventWithConfidentialSince(k, BoardSpec{
		BoardD: boardD, Title: "BW Board", Maintainers: []string{k.PubKeyHex()},
	}, since, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildBoardEventWithConfidentialSince: %v", err)
	}
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{asserted}); err != nil {
		t.Fatalf("publish the assertion: %v", err)
	}

	// An ORDINARY item write, by a caller that has never heard of the assertion.
	spec := BoardSpec{BoardD: boardD, Title: "BW Board", Maintainers: []string{k.PubKeyHex()}}
	if _, err := pub.PublishItem(context.Background(), &spec, CardSpec{
		ItemID: "bw-1", Title: "an ordinary item", Status: "inbox", BoardD: boardD,
	}, 1_700_000_100); err != nil {
		t.Fatalf("PublishItem: %v", err)
	}

	events, err := pub.Log.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var last *nostr.Event
	defs := 0
	for _, e := range events {
		if e != nil && e.Kind == KindBoard {
			last, defs = e, defs+1
		}
	}
	if defs < 2 {
		t.Fatalf("PublishItem republished no definition (%d in the log) — the case would pass vacuously", defs)
	}
	if got, ok := BoardConfidentialSince(last); !ok || got != since {
		t.Fatalf("the item write republished the definition WITHOUT the assertion: got (%d, %v), want (%d, true); tags=%v", got, ok, since, last.Tags)
	}
	// And it must survive as the READERS read it: coordinate-bound and verified.
	if got, found := AssertedConfidentialSince([]*nostr.Event{last}, coord); !found || got != since {
		t.Fatalf("AssertedConfidentialSince = (%d, %v), want (%d, true)", got, found, since)
	}
	// The caller's own spec is untouched — the carry-forward is not a mutation of
	// what it passed, so a caller reusing the spec cannot be surprised by it.
	if spec.Title != "BW Board" || spec.BoardD != boardD {
		t.Fatalf("PublishItem mutated the caller's BoardSpec: %+v", spec)
	}
}

// TestPublishItemRefusesWhenTheAssertionCannotBeEstablished pins the fail-closed
// direction. A log this publisher cannot read is a cutover it cannot establish,
// and publishing an unasserted definition on top of an asserted one is precisely
// the silent regression this whole guard exists to stop — so the write is refused
// instead of guessed at, and NOTHING becomes durable.
func TestPublishItemRefusesWhenTheAssertionCannotBeEstablished(t *testing.T) {
	k := kdKey(t)
	const boardD = "bw-unreadable"
	spec := BoardSpec{BoardD: boardD, Title: "BW Unreadable", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{ItemID: "bw-2", Title: "an ordinary item", Status: "inbox", BoardD: boardD}

	t.Run("no log at all", func(t *testing.T) {
		pub := &Publisher{Key: k}
		if _, err := pub.PublishItem(context.Background(), &spec, card, 1_700_000_100); err == nil {
			t.Fatal("a publisher with no authoritative log published a board definition anyway")
		}
	})

	t.Run("a log that cannot be read", func(t *testing.T) {
		// A DIRECTORY where the log file belongs: opening succeeds, reading does
		// not — a structural read error, which is the shape ReadAll reports (a
		// per-line parse failure is deliberately NOT an error; see scanEvents).
		dir := t.TempDir()
		pub := bwPublisher(k, dir)
		if _, err := pub.Log.ReadAll(); err == nil {
			t.Skip("this platform reads a directory as an empty file; the fixture cannot produce a read error here")
		}
		if _, err := pub.PublishItem(context.Background(), &spec, card, 1_700_000_100); err == nil {
			t.Fatal("an unreadable log did not stop the definition republish — the board's assertion would be dropped silently")
		}
	})
}
