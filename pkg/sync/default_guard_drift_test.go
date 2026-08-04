// Cross-package drift lock for pkg/nostr's default-guard constants (ready-fcf
// round-2 SECOND DEFECT).
//
// pkg/nostr/publishguard.go carries its OWN copy of this repo's reserved
// board D-tag (defaultReservedBoardD) and the board event kind
// (defaultBoardEventKind) — duplicated, not imported, because pkg/nostr
// cannot import pkg/sync without an import cycle (see that file's doc
// comment). Duplication with no equality check anywhere in the tree is a
// silent-drift hazard: if reservedProductionBoardD or KindBoard ever changes
// here in pkg/sync (the actual production values — reservedProductionBoardD
// pins THIS repo's live board coordinate, ".ready/config.json"'s "30301:
// <owner>:ready") and pkg/nostr's copies are not updated in lockstep,
// pkg/nostr's default guard silently stops protecting the REAL board: it
// would keep refusing whatever ITS OWN (now-stale) constant says, which is no
// longer the production coordinate, while the actual production coordinate
// sails through unguarded in any binary that never links pkg/sync. Nothing
// about that failure mode makes any EXISTING test fail — including
// pkg/nostr's own publishguard_test.go, which builds its test events from
// the same constants it is checking (tautological under drift; see that
// file's own package doc comment) — because every existing assertion reads
// only one side of the copy, never compares it to the other.
//
// This test reads BOTH sides and fails the instant they disagree.
package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// TestDefaultGuardConstants_MatchProductionBoardConstants is the drift lock.
// It is deliberately independent of the exact mechanism through which either
// side is reached — swap out sync.KindBoard's constant value, or
// nostr.publishguard.go's defaultReservedBoardD, and this test must go red
// regardless of which side moved.
func TestDefaultGuardConstants_MatchProductionBoardConstants(t *testing.T) {
	if got, want := nostr.DefaultReservedBoardD(), reservedProductionBoardD; got != want {
		t.Fatalf("pkg/nostr's default reserved board D-tag (%q) no longer matches pkg/sync's "+
			"reservedProductionBoardD (%q) — pkg/nostr's own default guard has silently stopped "+
			"protecting the ACTUAL production board coordinate; update publishguard.go's "+
			"defaultReservedBoardD to match", got, want)
	}
	if got, want := nostr.DefaultBoardEventKind(), KindBoard; got != want {
		t.Fatalf("pkg/nostr's default board event kind (%d) no longer matches pkg/sync's "+
			"KindBoard (%d) — pkg/nostr's own default guard has silently stopped recognizing the "+
			"board event itself; update publishguard.go's defaultBoardEventKind to match", got, want)
	}
}
