package main

// ready-5e7 done-condition tests for `rd confidential reseal`, the per-board
// execution pass.
//
// THE ONLY QUESTION THAT MATTERS is what a stranger can fetch off the RELAY, and
// every assertion here is made against a relay the test controls — never against
// the local log. The log is append-only and retains superseded events forever, so
// a suite that read it would stay green no matter what the pass did to the relay.
// That is not hypothetical: an entire confidential suite once stayed green while a
// rotation was deleting keys from the relay, because every test read the log.
//
// So the fixture seeds a storingRelay with what an outsider would see, runs the
// real cobra command, and re-reads the relay to check the result.

import (
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/pflag"
)

// sweepFixture is a confidential board with a plaintext tail, plus a relay serving
// exactly what the test says an outsider sees.
type sweepFixture struct {
	dir    string
	owner  string
	boardD string
	coord  string
	relay  *storingRelay
}

// newSweepFixture builds a confidential project holding `plaintextIDs` items whose
// cards are PLAINTEXT, and points both the read and write relay at one storingRelay.
func newSweepFixture(t *testing.T, plaintextTitles map[string]string) *sweepFixture {
	t.Helper()
	dir, owner, boardD := setupMixedConfidentialProject(t)

	for id, title := range plaintextTitles {
		if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{
			title: title, context: "plaintext body for " + id, itemType: "task", priority: "p2",
		}); err != nil {
			t.Fatalf("create plaintext item %s: %v", id, err)
		}
	}
	// Confidential AFTER the items exist — this is precisely the grandfathering
	// that leaves a plaintext tail, and the defect the pass exists to close.
	enableConfidential(t, dir, owner, boardD)

	relay := newStoringRelay(t)
	t.Cleanup(relay.close)

	// resolveRelayConfig walks up from the PROCESS cwd, not the project dir, so a
	// config written into the temp project would not be seen. RD_NOSTR_RELAY_URL is
	// the documented highest-precedence override and is what the live-relay tests
	// use, so the publisher and the reader both land on this fixture's relay.
	t.Setenv("RD_NOSTR_RELAY_URL", relay.url())

	f := &sweepFixture{dir: dir, owner: owner, boardD: boardD, coord: rdSync.BoardCoord(owner, boardD), relay: relay}
	// Seed the relay with what an outsider sees today: the board definition, the
	// owner's CEK grant (without it the board reads as never-confidential), and the
	// current winning card per coordinate straight out of the log.
	f.seedRelayFromLog(t)
	return f
}

// seedRelayFromLog puts the log's current winner for every card coordinate, plus the
// board definition and grants, onto the relay — an outsider's view of this board.
func (f *sweepFixture) seedRelayFromLog(t *testing.T) {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(f.dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	winners := map[string]*nostr.Event{}
	var others []*nostr.Event
	for _, e := range events {
		switch e.Kind {
		case rdSync.KindCard:
			d := tagVal1(e, "d")
			if cur, ok := winners[d]; !ok || e.CreatedAt > cur.CreatedAt {
				winners[d] = e
			}
		case rdSync.KindBoard, rdSync.KindRoleGrant:
			others = append(others, e)
		}
	}
	f.relay.seed(others...)
	for _, e := range winners {
		f.relay.seed(e)
	}
}

// runSweep runs the real `rd confidential reseal` against the fixture's relay.
func runSweep(t *testing.T, f *sweepFixture, args ...string) (string, error) {
	t.Helper()
	resetSweepFlags(t)
	if err := confidentialResealCmd.Flags().Set("relay", f.relay.url()); err != nil {
		t.Fatalf("set --relay: %v", err)
	}
	for i := 0; i+1 < len(args); i += 2 {
		if err := confidentialResealCmd.Flags().Set(args[i], args[i+1]); err != nil {
			t.Fatalf("set --%s: %v", args[i], err)
		}
	}
	var runErr error
	out := captureStdoutPipe(t, func() {
		runErr = confidentialResealCmd.RunE(confidentialResealCmd, nil)
	})
	return out, runErr
}

// resetSweepFlags clears flag state between tests — cobra flags live on the package
// level command, so a value one test sets is visible to the next.
func resetSweepFlags(t *testing.T) {
	t.Helper()
	fl := confidentialResealCmd.Flags()
	for _, name := range []string{"dry-run", "limit", "relay"} {
		f := fl.Lookup(name)
		if f == nil {
			t.Fatalf("confidentialResealCmd has no --%s flag", name)
		}
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
			continue
		}
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatalf("reset --%s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		for _, name := range []string{"dry-run", "limit", "relay"} {
			if f := fl.Lookup(name); f != nil {
				_ = f.Value.Set(f.DefValue)
			}
		}
	})
}

// relayPlaintextCount reports how many cards an OUTSIDER can still read: the winning
// card per coordinate with no envelope marker. Read straight off the relay's own
// store, so it cannot be satisfied by anything in the local log.
func (f *sweepFixture) relayPlaintextCount(t *testing.T) (plaintext int, titles []string) {
	t.Helper()
	f.relay.mu.Lock()
	defer f.relay.mu.Unlock()
	winners := map[string]*nostr.Event{}
	for _, e := range f.relay.events {
		if e.Kind != rdSync.KindCard {
			continue
		}
		d := tagVal1(e, "d")
		if cur, ok := winners[d]; !ok || e.CreatedAt > cur.CreatedAt || (e.CreatedAt == cur.CreatedAt && e.ID < cur.ID) {
			winners[d] = e
		}
	}
	for _, e := range winners {
		if tagVal1(e, "enc") == "" {
			plaintext++
			titles = append(titles, tagVal1(e, "title"))
		}
	}
	return plaintext, titles
}

// TestResealSweep_LeavesTheRelayServingZeroReadableCards is ready-5e7's done
// condition, in miniature: a confidential board with a plaintext tail ends the pass
// with an outsider able to read nothing.
func TestResealSweep_LeavesTheRelayServingZeroReadableCards(t *testing.T) {
	f := newSweepFixture(t, map[string]string{
		"a": "PLAINTEXT acquisition terms",
		"b": "PLAINTEXT payroll numbers",
		"c": "PLAINTEXT incident postmortem",
	})

	before, beforeTitles := f.relayPlaintextCount(t)
	if before != 3 {
		t.Fatalf("fixture is not the defect it claims to model: relay serves %d plaintext card(s), want 3 (%v)", before, beforeTitles)
	}

	out, err := runSweep(t, f)
	if err != nil {
		t.Fatalf("sweep failed: %v\n%s", err, out)
	}

	after, afterTitles := f.relayPlaintextCount(t)
	if after != 0 {
		t.Fatalf("relay still serves %d readable card(s) after the pass: %v\n%s", after, afterTitles, out)
	}
	if !strings.Contains(out, "serves ZERO readable cards") {
		t.Errorf("the pass did not report the board clean:\n%s", out)
	}
	// The plaintext originals must survive locally — that is what separates
	// "hidden from strangers" from "destroyed".
	events, rerr := rdSync.NewNostrLog(rdSync.NostrLogPath(f.dir)).ReadAll()
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	kept := 0
	for _, e := range events {
		if e.Kind == rdSync.KindCard && tagVal1(e, "enc") == "" {
			kept++
		}
	}
	if kept < 3 {
		t.Errorf("the local append-only log holds %d plaintext card(s), want at least 3 — the originals must not be destroyed", kept)
	}
}

// TestResealSweep_DryRunPublishesNothing: the pass is previewed before it is run,
// and a preview that quietly published would be the worst possible bug in a command
// whose whole risk profile is "it writes to a public relay".
func TestResealSweep_DryRunPublishesNothing(t *testing.T) {
	f := newSweepFixture(t, map[string]string{"a": "PLAINTEXT one", "b": "PLAINTEXT two"})

	before, _ := f.relayPlaintextCount(t)
	logBefore, err := rdSync.NewNostrLog(rdSync.NostrLogPath(f.dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	out, err := runSweep(t, f, "dry-run", "true")
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing published") {
		t.Errorf("dry run did not say it published nothing:\n%s", out)
	}

	after, _ := f.relayPlaintextCount(t)
	if after != before {
		t.Errorf("--dry-run changed what the relay serves: %d plaintext before, %d after", before, after)
	}
	logAfter, err := rdSync.NewNostrLog(rdSync.NostrLogPath(f.dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(logAfter) != len(logBefore) {
		t.Errorf("--dry-run appended %d event(s) to the local log", len(logAfter)-len(logBefore))
	}
}

// TestResealSweep_IsResumableByRederivingFromTheRelay: a partial run followed by a
// full run must converge, and must NOT re-seal what it already sealed. Re-sealing an
// already-sealed coordinate mints a new event id every run, so a pass that did it
// would churn forever and its own progress would be unmeasurable.
func TestResealSweep_IsResumableByRederivingFromTheRelay(t *testing.T) {
	f := newSweepFixture(t, map[string]string{"a": "PLAINTEXT one", "b": "PLAINTEXT two", "c": "PLAINTEXT three"})

	// Stop after one coordinate.
	if _, err := runSweep(t, f, "limit", "1"); err == nil {
		t.Fatal("a limited run left 2 readable cards on the relay and still exited clean — a partial pass must not report the board done")
	}
	mid, _ := f.relayPlaintextCount(t)
	if mid != 2 {
		t.Fatalf("after --limit 1 the relay serves %d plaintext card(s), want 2", mid)
	}

	sealedIDs := f.sealedEventIDs(t)

	// Resume: re-derives the remaining set off the relay.
	out, err := runSweep(t, f)
	if err != nil {
		t.Fatalf("resume failed: %v\n%s", err, out)
	}
	if after, titles := f.relayPlaintextCount(t); after != 0 {
		t.Fatalf("relay still serves %d readable card(s) after resuming: %v", after, titles)
	}

	// The coordinate sealed by the first run must carry the SAME event id — the
	// resume re-read it as already sealed and left it alone.
	for d, id := range sealedIDs {
		if got := f.sealedEventIDs(t)[d]; got != id {
			t.Errorf("coordinate %s was re-sealed by the resume: event %s -> %s; an already-sealed coordinate must be skipped, or the pass never converges", d, shortID(id), shortID(got))
		}
	}
}

// sealedEventIDs maps each coordinate's d-tag to the id of the SEALED card the relay
// currently serves for it.
func (f *sweepFixture) sealedEventIDs(t *testing.T) map[string]string {
	t.Helper()
	f.relay.mu.Lock()
	defer f.relay.mu.Unlock()
	winners := map[string]*nostr.Event{}
	for _, e := range f.relay.events {
		if e.Kind != rdSync.KindCard {
			continue
		}
		d := tagVal1(e, "d")
		if cur, ok := winners[d]; !ok || e.CreatedAt > cur.CreatedAt {
			winners[d] = e
		}
	}
	out := map[string]string{}
	for d, e := range winners {
		if tagVal1(e, "enc") != "" {
			out[d] = e.ID
		}
	}
	return out
}

// TestResealSweep_OutranksTheCARDTHERELAYSERVES_NotTheOneThisMachineHolds is the
// ready-500 silent no-op, sourced from the relay instead of the log.
//
// Eight projects write to this board set from more than one machine. When another
// machine has written a NEWER plaintext card at a coordinate, this machine's log
// cannot see it — nostrNextCreatedAt floors only against local events. A
// replacement stamped from that local floor sorts BEHIND what the relay serves, so
// latest-wins keeps the PLAINTEXT, the publish reports success, and every local
// signal says the coordinate is sealed. The pass would report the board clean while
// a stranger still reads it.
//
// The fixture makes the two disagree deliberately: a plaintext card stamped well
// into the future goes onto the RELAY ONLY, never into the log.
func TestResealSweep_OutranksTheCardTheRelayServesNotTheOneThisMachineHolds(t *testing.T) {
	f := newSweepFixture(t, map[string]string{"a": "PLAINTEXT contested coordinate"})

	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	var itemID string
	for id := range byID {
		itemID = id
	}
	if itemID == "" {
		t.Fatal("fixture produced no item")
	}

	// Another machine's newer PLAINTEXT card, on the relay and nowhere else.
	future := time.Now().Unix() + 5000
	newer, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
		ItemID: itemID, Title: "PLAINTEXT written by another machine", Status: "active",
		Priority: "p2", Type: "task", BoardD: f.boardD, BoardAuthor: f.owner,
	}, future)
	if err != nil {
		t.Fatalf("build the other machine's card: %v", err)
	}
	f.relay.seed(newer)

	if n, _ := f.relayPlaintextCount(t); n != 1 {
		t.Fatalf("fixture: relay serves %d plaintext card(s), want 1", n)
	}

	out, err := runSweep(t, f)
	if err != nil {
		t.Fatalf("sweep failed: %v\n%s", err, out)
	}
	if after, titles := f.relayPlaintextCount(t); after != 0 {
		t.Fatalf("the relay STILL serves %d readable card(s) (%v) — the replacement was stamped from the local floor and lost latest-wins to the copy the relay already had; the publish 'succeeded' and sealed nothing:\n%s", after, titles, out)
	}
}

// TestResealSweep_RefusesABoardThatWasNeverConfidential: 261 of the portfolio's
// boards carry no CEK-bearing grant. Their plaintext is INTENDED, and sealing one
// would make it unreadable to its own audience while achieving nothing this epic is
// for. The refusal is what keeps a portfolio-wide loop from doing that.
func TestResealSweep_RefusesABoardThatWasNeverConfidential(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)
	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "public item", context: "intended to be readable", itemType: "task", priority: "p2",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// NOTE: enableConfidential is deliberately NOT called — no CEK grant exists.
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	t.Setenv("RD_NOSTR_RELAY_URL", relay.url())
	f := &sweepFixture{dir: dir, owner: owner, boardD: boardD, coord: rdSync.BoardCoord(owner, boardD), relay: relay}
	f.seedRelayFromLog(t)

	out, err := runSweep(t, f)
	if err == nil {
		t.Fatalf("the sweep ran against a board with no CEK-bearing grant instead of refusing:\n%s", out)
	}
	if !strings.Contains(err.Error(), "never confidential") {
		t.Errorf("refusal does not explain that the board was never confidential: %v", err)
	}
	if n, _ := f.relayPlaintextCount(t); n == 0 {
		t.Error("the refusal still sealed the board's cards")
	}
}
