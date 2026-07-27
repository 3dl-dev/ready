package main

import "testing"

// TestProbeBoardD_NeverReservedProductionBoard is the ready-fce / ready-6d0
// regression lock for finding (3): this probe signs with the REAL portfolio key
// in "allowlisted" mode and calls BuildCardEvent + nostr.Publish DIRECTLY — it
// never goes through pkg/sync's Publisher, so Publisher.Production (the
// write-path guard added for the other two findings) cannot protect it. The only
// fix available at this call site is to never target the reserved production
// board coordinate ("ready") in the first place.
func TestProbeBoardD_NeverReservedProductionBoard(t *testing.T) {
	if probeBoardD == "ready" {
		t.Fatalf("probe BoardD must never equal the reserved production board coordinate %q (ready-fce finding 3 / ready-6d0)", probeBoardD)
	}
}
