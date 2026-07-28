package sync

// Portfolio-wide key derivation (ready-4d9).
//
// DeriveBoardKeyring answers "what can this reader decrypt on ONE board", and
// every caller so far knew which board it meant — the pinned coordinate of the
// current project directory. `rd board --portfolio --with-key` does not: the
// owner's ask is "give me a link to the WHOLE portfolio", so the CLI has to
// enumerate every board this key can read before it can gather any keys.
//
// The enumeration source is the grants themselves. A kind-39301 role grant names
// its board in a signed "a" tag, so the set of boards a key can read is exactly
// the set of coordinates appearing in owner-signed, CEK-bearing grants addressed
// to that key. No board-definition (kind-30301) lookup is needed to gather keys —
// which matters, because a reader may hold a grant for a board whose definition
// this machine has never fetched.
//
// NO AUTHORITY IS DECIDED HERE. The four checks that turn a wrapped key into a
// usable CEK (signature, owner-signed, signed p tag names the reader, the wrap
// actually opens) live in DeriveBoardKeyring and run, in full, once per candidate
// coordinate. The scan below is a candidate ENUMERATOR: it decides which
// coordinates are worth running that computation for, and every key it yields
// came out of DeriveBoardKeyring, never out of this function. Weakening the
// prefilter can therefore cost work; it cannot mint a key.
//
// The prefilter does verify and owner-check anyway, for an availability reason
// rather than an authorization one: a hostile relay can serve unlimited forged
// grants naming this reader, and an unverified prefilter would turn each one into
// a full DeriveBoardKeyring pass over the whole event set (quadratic work on
// attacker-controlled input).

import (
	"sort"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// DerivePortfolioKeyring returns ONE keyring spanning EVERY board `reader` can
// read in `events`, plus those boards' coordinates sorted ascending.
//
// A coordinate appears in the returned slice only when the reader actually holds
// at least one CEK for it — "boards I can read", not "boards I have heard of".
// A board the reader was granted membership on but whose wrap does not open
// (revoked-then-rotated, or a grant lifted out of someone else's envelope) is
// absent, which is the fail-closed direction: the emitted link then carries no
// key for it and the browser renders placeholders.
//
// Cutover and LTK entries are carried across for the boards that made the cut, so
// the returned keyring answers Cutover()/LTK() exactly as a per-board
// DeriveBoardKeyring would. Boards the reader cannot read contribute nothing at
// all — including no cutover — because this keyring exists to be SERIALIZED into
// a link, not to gate a fold; the browser rederives every cutover from the
// owner-signed grants it fetches itself (web/board/src/lib/keyring.ts).
func DerivePortfolioKeyring(events []*nostr.Event, reader *nostr.Key) (*BoardKeyring, []string) {
	out := &BoardKeyring{ceks: map[string]map[int][32]byte{}, ltks: map[string][32]byte{}, cutover: map[string]int64{}}
	if reader == nil {
		return out, nil
	}
	readerPub := reader.PubKeyHex()

	candidates := map[string]bool{}
	for _, e := range events {
		if e == nil || e.Kind != KindRoleGrant {
			continue
		}
		if e.Verify() != nil {
			continue
		}
		g, ok := parseRoleGrant(e)
		if !ok {
			continue
		}
		// Mirrors DeriveBoardKeyring's CHECK 2 + CHECK 3 as a cheap prefilter.
		// DeriveBoardKeyring re-runs both (and CHECK 1 and CHECK 4) below.
		if g.Signer != g.BoardOwner || g.WrappedCEK == "" || g.CEKEpoch < 1 {
			continue
		}
		if g.Grantee != readerPub {
			continue
		}
		candidates[BoardCoord(g.BoardOwner, g.BoardD)] = true
	}

	coords := make([]string, 0, len(candidates))
	for coord := range candidates {
		owner, boardD, ok := parseBoardCoord(coord)
		if !ok {
			continue
		}
		kr := DeriveBoardKeyring(events, reader, owner, boardD)
		if len(kr.ceks[coord]) == 0 {
			continue
		}
		out.ceks[coord] = kr.ceks[coord]
		if ltk, held := kr.ltks[coord]; held {
			out.ltks[coord] = ltk
		}
		if at, seen := kr.cutover[coord]; seen {
			out.cutover[coord] = at
		}
		coords = append(coords, coord)
	}
	sort.Strings(coords)
	return out, coords
}
