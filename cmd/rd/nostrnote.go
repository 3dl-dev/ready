// `rd progress` on the nostr-native path (ready-ed4).
//
// This file exists because `rd progress` is no longer a card edit. It used to
// append its note to item.Context and republish the WHOLE 30302 card, which is
// how a card grows past the 64 KiB relay ceiling until the item becomes
// permanently unpublishable — measured on this board at 39,921 bytes for
// ready-f75 (60.9% of the cap) on 2026-07-30, and already terminal for vms-760 on
// another board, whose live state had to be abandoned to a new item. A note now
// publishes ONE kind-1111 event and leaves the card alone.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// publishItemNoteNostr publishes one progress note for item as its own kind-1111
// event.
//
// It mirrors publishItemCardEditNostr's preamble exactly — the same
// redacted-republish refusal, the same board-author / board-spec resolution, the
// same confidential envelope injection — because a note is free text on the same
// board and must be sealed under the same CEK as the card's context, or the
// fail-closed fold gate quarantines it and the trail silently loses the entry.
//
// The CardSpec handed to PublishNote is NOT published as a card on the common
// path (see Publisher.PublishNote): it supplies the board coordinate, the
// envelope, and — for a legacy item whose card still carries its trail inline —
// the PendingNotes that must be minted plus the compacted card that replaces it.
func publishItemNoteNostr(item *state.Item, note state.ProgressNote) error {
	if !nostrWriteActive() {
		return nil
	}
	if err := refuseRedactedRepublish(item); err != nil {
		return err
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		return err
	}
	dir, _ := readyProjectDir()
	boardAuthor, err := nostrBoardAuthor(dir, pub.Key.PubKeyHex())
	if err != nil {
		return err
	}
	board := boardSpecForProject(dir, boardAuthor)
	card := rdSync.CardSpecFromItem(item, board.BoardD)
	// BLOCKED IS DERIVED, NEVER REPUBLISHED (ready-500) — the compaction branch of
	// PublishNote can republish this card, so it needs the same guard every other
	// republish hook applies.
	card.Status = rdSync.NonDerivedStatus(item)
	card.BoardAuthor = boardAuthor
	if err := setCardEnvelope(dir, pub, boardAuthor, board.BoardD, &card); err != nil {
		return err
	}
	res, err := pub.PublishNote(context.Background(), card, note, nostrNextCreatedAt(pub.Log, rdSync.ItemDriftScope(item.ID)))
	if err != nil {
		return err
	}
	if debugOutput {
		for _, ev := range res.Events {
			fmt.Fprintf(os.Stderr, "nostr: published kind %d id %s (relay-accepted=%v)\n", ev.Kind, ev.EventID, ev.AnyRelay)
		}
	}
	return nil
}
