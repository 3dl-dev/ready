package main

// ready-fcd regression: `rd relay audit` must name a deliberately re-sealed
// coordinate as SUPERSEDED, distinctly from a genuinely missing one.
//
// WHY THIS MATTERS (ready-336, ready-260). Sealing a plaintext card's free text
// republishes it under a NEW event id — kind-30302 is addressable, so the
// sealed replacement evicts the plaintext copy from the relay via ordinary
// NIP-33 replacement, not a delete. That breaks ready-260's verbatim-republish
// assumption (the id that was on the relay yesterday is gone today) for every
// coordinate a bulk re-seal pass touches. Without a distinct classification,
// the first `rd relay audit` run after re-sealing 24 boards would report every
// one of those coordinates the same way it reports a coordinate that was NEVER
// published at all — mass false "corruption" burying the one signal an audit
// exists to surface.
//
// Both states are constructed here deliberately: a test that only proved the
// superseded case clean could not fail for the reason that matters — an
// implementation that quietly reclassified EVERY mismatch (including a real,
// never-published gap) as "superseded, ignore it" would pass a superseded-only
// test while silently defeating the audit's actual job.
import (
	"encoding/json"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/pflag"
)

// setRelayAuditRelay pins relayAuditCmd's --relay (a StringArray flag shared
// across every test in this package) to exactly one URL, REPLACING whatever a
// prior test left there rather than appending to it — pflag's StringArray.Set
// only replaces on the FIRST call per flag lifetime and appends on every call
// after, which would silently carry a previous test's relay URL into this one.
func setRelayAuditRelay(t *testing.T, url string) {
	t.Helper()
	f := relayAuditCmd.Flags().Lookup("relay")
	if f == nil {
		t.Fatal("relayAuditCmd has no --relay flag")
	}
	sv, ok := f.Value.(pflag.SliceValue)
	if !ok {
		t.Fatalf("--relay flag value %T does not implement pflag.SliceValue", f.Value)
	}
	if err := sv.Replace([]string{url}); err != nil {
		t.Fatalf("replace --relay: %v", err)
	}
	t.Cleanup(func() { _ = sv.Replace(nil) })
}

// resealFixture builds one board's local log holding, for a SINGLE item
// coordinate, a plaintext card followed by a later, confidential (sealed)
// replacement — exactly what a deliberate re-seal leaves behind — plus a
// SECOND item that is signed locally but never reaches the relay at all. It
// seeds a storingRelay with only the board definition and the sealed
// replacement, mirroring a relay that (a) fully converged on the re-seal and
// (b) never received the second item.
type resealFixture struct {
	dir           string
	owner         string
	boardD        string
	boardCoord    string
	relay         *storingRelay
	supersededID  string // event id of the coordinate's relay-served (and local) winner
	supersededTag string // coordinate string for the resealed item
	missingTag    string // coordinate string for the genuinely-absent item
}

func newResealFixture(t *testing.T) *resealFixture {
	t.Helper()
	dir, owner := setupNostrNativeProject(t)
	boardD := projectPrefix(dir)
	boardCoord := rdSync.BoardCoord(owner, boardD)

	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}

	base := time.Now().Unix() - 10000

	// The re-sealed coordinate: plaintext first, sealed replacement later, same
	// item id (same addressable coordinate).
	plainCard, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
		ItemID: "resealed-item", Title: "plaintext title", Status: "active",
		Priority: "p2", Type: "task", BoardD: boardD,
	}, base)
	if err != nil {
		t.Fatalf("build plaintext card: %v", err)
	}
	sealedCard, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
		ItemID: "resealed-item", Title: "plaintext title", Status: "active",
		Priority: "p2", Type: "task", BoardD: boardD,
		Enc: &rdSync.Envelope{CEK: [32]byte{1, 2, 3, 4}, Epoch: 1},
	}, base+100)
	if err != nil {
		t.Fatalf("build sealed card: %v", err)
	}
	if sealedCard.ID == plainCard.ID {
		t.Fatal("sealed replacement has the SAME id as the plaintext card — the fixture proves nothing about minted ids")
	}

	// A second item, signed locally, that never reaches the relay at all.
	missingCard, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
		ItemID: "missing-item", Title: "never published", Status: "active",
		Priority: "p2", Type: "task", BoardD: boardD,
	}, base+50)
	if err != nil {
		t.Fatalf("build missing card: %v", err)
	}

	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	if _, err := log.AppendUnique([]*nostr.Event{plainCard, sealedCard, missingCard}); err != nil {
		t.Fatalf("append fixture events: %v", err)
	}

	all, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var boardDef *nostr.Event
	for _, e := range all {
		if e.Kind == rdSync.KindBoard {
			boardDef = e
		}
	}
	if boardDef == nil {
		t.Fatal("no board definition in the local log — setupNostrNativeProject should have appended one")
	}

	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	// The relay converged on the re-seal (serves the SEALED replacement, not the
	// plaintext original) and never received the missing item at all.
	relay.seed(boardDef, sealedCard)

	return &resealFixture{
		dir: dir, owner: owner, boardD: boardD, boardCoord: boardCoord, relay: relay,
		supersededID:  sealedCard.ID,
		supersededTag: rdSync.CardCoord(owner, "resealed-item"),
		missingTag:    rdSync.CardCoord(owner, "missing-item"),
	}
}

// runRelayAuditJSON runs relayAuditCmd against f.relay with --json and decodes
// the single-relay result. Returns the RunE error alongside it, since exit
// status is itself part of what this item's done condition asserts.
func runRelayAuditJSON(t *testing.T, f *resealFixture) (rdSync.BoardRelayAudit, error) {
	t.Helper()
	setRelayAuditRelay(t, f.relay.url())

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	var runErr error
	stdout := captureStdoutPipe(t, func() {
		runErr = relayAuditCmd.RunE(relayAuditCmd, nil)
	})

	var results []rdSync.BoardRelayAudit
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("unmarshal audit JSON: %v\nstdout=%s", err, stdout)
	}
	if len(results) != 1 {
		t.Fatalf("got %d audit result(s), want exactly 1: %+v", len(results), results)
	}
	return results[0], runErr
}

// TestRelayAuditCmd_SupersededDistinctFromGenuinelyMissing is the ready-fcd
// done condition: on ONE board carrying both a fully-converged re-seal and a
// coordinate that was never published, the audit must name them differently.
func TestRelayAuditCmd_SupersededDistinctFromGenuinelyMissing(t *testing.T) {
	f := newResealFixture(t)
	a, err := runRelayAuditJSON(t, f)

	// The genuinely-absent coordinate is a real gap: it must fail the audit.
	if err == nil {
		t.Fatal("relay audit returned nil error although one coordinate was never published — a genuine gap must not exit clean")
	}
	if a.Match {
		t.Fatalf("Match=true although %+v", a)
	}

	if !sliceContains(a.MissingCoords, f.missingTag) {
		t.Fatalf("MissingCoords = %v, want it to contain the genuinely-absent coordinate %s", a.MissingCoords, f.missingTag)
	}
	if !sliceContains(a.SupersededCoords, f.supersededTag) {
		t.Fatalf("SupersededCoords = %v, want it to contain the re-sealed coordinate %s", a.SupersededCoords, f.supersededTag)
	}

	// The two must be reported through DIFFERENT fields, not the same one — the
	// whole point of this item.
	if sliceContains(a.MissingCoords, f.supersededTag) {
		t.Fatalf("the re-sealed coordinate %s wrongly appears in MissingCoords: %+v", f.supersededTag, a)
	}
	if sliceContains(a.StaleCoords, f.supersededTag) {
		t.Fatalf("the re-sealed coordinate %s wrongly appears in StaleCoords: %+v", f.supersededTag, a)
	}
	if sliceContains(a.SupersededCoords, f.missingTag) {
		t.Fatalf("the genuinely-absent coordinate %s wrongly appears in SupersededCoords: %+v", f.missingTag, a)
	}
}

// TestRelayAuditCmd_ResealedCoordinateAloneExitsClean isolates the "exits
// clean" half of the done condition: with NOTHING wrong except a fully
// converged re-seal, the command must succeed and Match must be true — a
// deliberate, already-landed re-seal is not corruption and must not require
// repair.
func TestRelayAuditCmd_ResealedCoordinateAloneExitsClean(t *testing.T) {
	f := newResealFixture(t)
	// Publish the missing item's event too, so the ONLY irregularity left on
	// this board is the reseal.
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(f.dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	seeded := false
	for _, e := range events {
		if e.Kind == rdSync.KindCard && sameCoord(e, f.owner, "missing-item") {
			f.relay.seed(e)
			seeded = true
		}
	}
	if !seeded {
		t.Fatal("did not find the missing-item card in the local log to seed onto the relay")
	}

	a, err := runRelayAuditJSON(t, f)
	if err != nil {
		t.Fatalf("relay audit returned an error for a board whose only irregularity is a converged re-seal: %v (%+v)", err, a)
	}
	if !a.Match {
		t.Fatalf("Match=false for a board whose only irregularity is a converged re-seal: %+v", a)
	}
	if len(a.MissingCoords) != 0 {
		t.Fatalf("MissingCoords = %v, want none", a.MissingCoords)
	}
	if len(a.StaleCoords) != 0 {
		t.Fatalf("StaleCoords = %v, want none", a.StaleCoords)
	}
	if !sliceContains(a.SupersededCoords, f.supersededTag) {
		t.Fatalf("SupersededCoords = %v, want it to contain the re-sealed coordinate %s even though the audit exits clean", a.SupersededCoords, f.supersededTag)
	}
}

// sameCoord reports whether e is the kind-30302 card for (owner, itemID).
func sameCoord(e *nostr.Event, owner, itemID string) bool {
	if e.PubKey != owner {
		return false
	}
	for _, tg := range e.Tags {
		if len(tg) >= 2 && tg[0] == "d" && tg[1] == itemID {
			return true
		}
	}
	return false
}
