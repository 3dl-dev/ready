package sync

// BoardKeyring.Epochs lives in its OWN file, not in keydist.go, for a
// mechanical reason: internal/foldvectors/citations_test.go pins several
// keydist.go funcs (Cutover, CurrentEpoch, DeriveBoardKeyring) to their exact
// current declaration LINE NUMBERS, so adding even one import line to
// keydist.go turns the spec-citation suite red. A new file adds the method to
// the same type with zero line movement in the cited file.

import "sort"

// Epochs lists, ascending, every CEK epoch this reader actually HOLDS for a
// board. CurrentEpoch answers a different question — the single highest epoch,
// which is what the WRITE path seals new cards under — and cannot enumerate.
//
// The distinction is load-bearing. DeriveBoardKeyring scans ALL historical
// grants (not latest-wins), so a reader keeps every epoch it was ever granted
// and older cards open under older epochs. Any caller that has to hand over the
// reader's whole read capability rather than seal one new card needs the full
// set, or every pre-rotation card silently fails to decrypt for the recipient.
// `rd board --with-key` (cmd/rd/board.go) is that caller: it embeds each held
// epoch's CEK in the board link's fragment so the browser board can open
// pre-rotation and post-rotation cards alike.
//
// Mirrors web/board/src/lib/keyring.ts's BoardKeyring.epochs(), which is the
// browser port of this same keyring.
func (kr *BoardKeyring) Epochs(coord string) []int {
	if kr == nil {
		return nil
	}
	m, ok := kr.ceks[coord]
	if !ok || len(m) == 0 {
		return nil
	}
	out := make([]int, 0, len(m))
	for ep := range m {
		out = append(out, ep)
	}
	sort.Ints(out)
	return out
}
