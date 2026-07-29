package main

import (
	"fmt"

	"github.com/3dl-dev/ready/pkg/state"
)

// refuseRedactedRepublish is the write-path guard that stops rd from destroying
// confidential content it cannot read (ready-76b).
//
// Every mutation republishes the WHOLE latest-wins 30302 card, rebuilt from the
// PROJECTED item via CardSpecFromItem. When the projection could not decrypt the
// card, that item's Title/Context/Description are the literal string "[encrypted]"
// — so an unguarded republish re-seals the placeholder AS THE ITEM'S CONTENT and
// the original is gone from latest-wins forever. This is not a hypothetical: it
// silently destroyed four items on the ready board (ready-2b25 and three enc-live
// fixtures) before anyone noticed, because a read that correctly fail-closed was
// laundered into a write that could not.
//
// The refusal is deliberately TOTAL — it blocks the mutation before any event is
// built, rather than publishing a partial or best-effort card. An item you cannot
// read is an item you must not overwrite; there is no safe subset. The caller is
// told which item and why, because the usual cause (an old CEK epoch whose grant
// no longer exists) is fixable, and silently degrading would hide it.
func refuseRedactedRepublish(item *state.Item) error {
	if item == nil || !item.Redacted {
		return nil
	}
	return fmt.Errorf(
		"refusing to modify %s: its confidential free text could not be decrypted, so rd is holding a %q placeholder instead of the real title/context. "+
			"Republishing the card would re-seal that placeholder AS the item's content and destroy the original irreversibly. "+
			"You are most likely missing the CEK epoch this card was sealed under — check `rd confidential status`, and recover the epoch key before mutating this item",
		item.ID, "[encrypted]")
}
