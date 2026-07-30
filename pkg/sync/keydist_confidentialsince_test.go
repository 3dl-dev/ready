package sync

// §11.13a's OWNER-SIGNED CUTOVER ASSERTION (ready-475) — `confidential_since` on
// the board's own kind-30301 definition, honoured by this reader in preference to
// the grant-minimum heuristic.
//
// THE LIVE SHAPE THIS EXISTS FOR. The `ready` board carries three kind-1630
// status events sealed under a TEST-LOCAL CEK that was never a ready-board CEK
// (envelope_live_relay_test.go's fixtures, written to the production board before
// the ready-fce guard existed). Kind 1630 is a REGULAR event, so they can never
// be superseded. They are older than the board's true cutover, so §11.13a's TIME
// witness fires on them forever and both readers withhold 167 of the board's 536
// cards from its own owner — on a board that is otherwise entirely healthy.
// gwFixture's `sealed1` plays exactly that part below: a verified sealed event
// older than the derived instant, which the witness cannot tell from real
// evidence and which the owner's own signature can.
//
// THE THREE PROPERTIES THAT MAKE THIS AN EXTENSION AND NOT A WEAKENING, one test
// each, and each verified RED with its own guard removed and green with it back
// (one at a time — see each test's GUARD note):
//
//  1. A board with NO assertion behaves exactly as today.
//  2. An assertion signed by anyone but the board owner is IGNORED.
//  3. A relay OMITTING the assertion yields today's behaviour, not a wider one.
//
// The browser's half of the same contract is
// web/board/src/lib/confidentiality.test.ts, and the shared conformance vectors
// (internal/foldvectors/cases_confidentialsince.go) keep the two from drifting.

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// csAssertedBoard rebuilds f's board definition carrying `confidential_since`,
// signed by the board's real owner. Everything else about the definition is
// unchanged, so the only difference between this board event and f.board is the
// one tag.
func csAssertedBoard(t *testing.T, f *gwFixture, since int64) *nostr.Event {
	t.Helper()
	e, err := BuildBoardEventWithConfidentialSince(f.owner, BoardSpec{
		BoardD: f.boardD, Title: f.boardD, Maintainers: []string{f.owner.PubKeyHex()},
	}, since, gwT0-1000)
	if err != nil {
		t.Fatalf("BuildBoardEventWithConfidentialSince: %v", err)
	}
	return e
}

// csPollutedAnswer is the live `ready` board's shape, minimised: a sealed event
// OLDER than the board's earliest served grant (the foreign pollution), a
// plaintext card from before the true cutover that the board must still show, a
// plaintext card from after it that must stay quarantined, and the grant that
// derives the too-late instant.
//
// Without an assertion the TIME witness fires on `sealed1` and Cutover reports
// the fail-closed (0, true): NOTHING is grandfathered and `preClear` disappears.
func csPollutedAnswer(f *gwFixture, board *nostr.Event) []*nostr.Event {
	return []*nostr.Event{board, f.sealed1, f.preClear, f.leak, f.grant2}
}

// TestConfidentialSinceRestoresGrandfatheredPlaintext is ready-475's done
// condition at this layer: on the polluted answer, the owner's own signed
// assertion establishes the cutover the witness could only refute, so the board's
// genuinely pre-cutover plaintext folds again — and the post-cutover plaintext
// does NOT, which is the fail-closed path still intact.
func TestConfidentialSinceRestoresGrandfatheredPlaintext(t *testing.T) {
	f := gwNewFixture(t)

	// CONTROL — the identical answer with no assertion is fully withheld today.
	items, kr := f.project(t, csPollutedAnswer(f, f.board))
	if cut, ok := kr.Cutover(f.coord); !ok || cut != 0 {
		t.Fatalf("no assertion: cutover = %d (ok=%v), want the fail-closed (0,true)", cut, ok)
	}
	if items["ready-gw-old"] != nil {
		t.Fatal("no assertion: the contradicted cutover grandfathered a card — the control is not the shape this test needs")
	}

	// THE ASSERTION — the owner states the instant the witness cannot establish.
	items, kr = f.project(t, csPollutedAnswer(f, csAssertedBoard(t, f, gwT0)))
	if cut, ok := kr.Cutover(f.coord); !ok || cut != gwT0 {
		t.Fatalf("asserted: cutover = %d (ok=%v), want (%d,true)", cut, ok, gwT0)
	}
	if items["ready-gw-old"] == nil {
		t.Fatal("asserted: the board's genuinely pre-cutover plaintext card is STILL withheld — the whole point of the assertion")
	}
	if it := items["ready-gw-leak"]; it != nil {
		t.Fatalf("FAIL-OPEN: the assertion grandfathered a post-cutover plaintext card: title=%q", it.Title)
	}
	if items["ready-gw-sealed"] == nil {
		t.Fatal("asserted: a well-formed sealed card was dropped")
	}
}

// TestConfidentialSinceNeverGrandfathersMoreThanTheGrants pins the min() half:
// an assertion LATER than the board's own earliest served grant does not apply,
// so it can never widen what §11.13 alone would show. Without min() the leak at
// t0+300 would be grandfathered by an assertion at t0+400.
//
// GUARD: the `since < at` comparison in DeriveBoardKeyring. Assign the assertion
// unconditionally and this goes RED on exactly that leak. Verified.
func TestConfidentialSinceNeverGrandfathersMoreThanTheGrants(t *testing.T) {
	f := gwNewFixture(t)
	// A complete, healthy answer: derived cutover is the truth, gwT0.
	full := []*nostr.Event{csAssertedBoard(t, f, gwT0+400), f.grant1, f.sealed1, f.preClear, f.leak, f.grant2}
	items, kr := f.project(t, full)
	if cut, ok := kr.Cutover(f.coord); !ok || cut != gwT0 {
		t.Fatalf("cutover = %d (ok=%v), want the DERIVED %d — an assertion may only move it earlier", cut, ok, gwT0)
	}
	if it := items["ready-gw-leak"]; it != nil {
		t.Fatalf("FAIL-OPEN: a late assertion grandfathered %q, which the served grants alone quarantine", it.Title)
	}
}

// TestNoAssertionBehavesExactlyAsToday is PROPERTY 1. Both arms are asserted,
// because "as today" has two halves and a change could break either: a healthy
// board still derives its real cutover and grandfathers its real history, and a
// contradicted board still fails closed.
//
// GUARD: BoardConfidentialSince's `v <= 0` rejection (boardconfidential.go) —
// i.e. "an absent or malformed tag is NOT an assertion". Make it report
// (0, true) for a board carrying no tag and the healthy arm goes RED: the
// effective cutover becomes min(0, derived) = 0 and the pre-cutover card vanishes.
// Verified red with that one change, green with it restored.
func TestNoAssertionBehavesExactlyAsToday(t *testing.T) {
	f := gwNewFixture(t)

	// HEALTHY — nothing contradicts the derivation, so §11.13's instant stands.
	full := []*nostr.Event{f.board, f.grant1, f.sealed1, f.preClear, f.leak, f.grant2}
	items, kr := f.project(t, full)
	if cut, ok := kr.Cutover(f.coord); !ok || cut != gwT0 {
		t.Fatalf("healthy: cutover = %d (ok=%v), want the derived %d", cut, ok, gwT0)
	}
	if items["ready-gw-old"] == nil {
		t.Fatal("healthy: the genuinely pre-cutover plaintext card was not grandfathered")
	}
	if items["ready-gw-leak"] != nil {
		t.Fatal("healthy: a post-cutover plaintext card folded")
	}

	// CONTRADICTED — the witnesses are untouched for a board with no assertion.
	items, kr = f.project(t, csPollutedAnswer(f, f.board))
	if cut, ok := kr.Cutover(f.coord); !ok || cut != 0 {
		t.Fatalf("contradicted: cutover = %d (ok=%v), want the fail-closed (0,true)", cut, ok)
	}
	if it := items["ready-gw-leak"]; it != nil {
		t.Fatalf("contradicted: FAIL-OPEN, %q folded in clear", it.Title)
	}
	if items["ready-gw-old"] != nil {
		t.Fatal("contradicted: a cutover this reader cannot trust must grandfather nothing")
	}
}

// TestForeignAssertionIsIgnored is PROPERTY 2, in its two reachable forms:
//
//   - ANOTHER KEY'S board definition, correctly signed, naming the same "d" tag.
//     Its coordinate is 30301:<stranger>:ready, not this board's, so it asserts
//     nothing here.
//   - THIS OWNER'S definition with the tag TAMPERED IN AFTER SIGNING. The id and
//     signature no longer cover the tags, so Verify() rejects it.
//
// Either one, believed, hands a relay the power to set another board's cutover.
//
// GUARD: the coordinate comparison in AssertedConfidentialSince (which IS the
// author check — a kind-30301's coordinate embeds its author) and the Verify()
// call beside it. Weaken the first to a bare `d`-tag match and the stranger's
// board is honoured; drop the second and the tampered one is. Each removed
// separately, each turning this test RED, each restored.
func TestForeignAssertionIsIgnored(t *testing.T) {
	f := gwNewFixture(t)
	stranger := kdKey(t)
	strangerBoard, err := BuildBoardEventWithConfidentialSince(stranger, BoardSpec{
		BoardD: f.boardD, Title: f.boardD,
	}, gwT0, gwT0-1000)
	if err != nil {
		t.Fatalf("BuildBoardEventWithConfidentialSince: %v", err)
	}
	// The owner's own definition, signed WITHOUT the tag, then tampered.
	tampered, err := BuildBoardEvent(f.owner, BoardSpec{
		BoardD: f.boardD, Title: f.boardD, Maintainers: []string{f.owner.PubKeyHex()},
	}, gwT0-1000)
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	tampered.Tags = append(tampered.Tags, []string{TagConfidentialSince, "1800000000"})
	if tampered.Verify() == nil {
		t.Fatal("the tampered definition still verifies — the fixture proves nothing")
	}

	for _, tc := range []struct {
		name  string
		board *nostr.Event
	}{
		{"a stranger's board definition", strangerBoard},
		{"the owner's definition with the tag added after signing", tampered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			served := append(csPollutedAnswer(f, f.board), tc.board)
			items, kr := f.project(t, served)
			if cut, ok := kr.Cutover(f.coord); !ok || cut != 0 {
				t.Fatalf("cutover = %d (ok=%v), want the fail-closed (0,true) — %s must assert NOTHING",
					cut, ok, tc.name)
			}
			if it := items["ready-gw-leak"]; it != nil {
				t.Fatalf("FAIL-OPEN: %s established a cutover and %q folded in clear", tc.name, it.Title)
			}
			if items["ready-gw-old"] != nil {
				t.Fatalf("FAIL-OPEN: %s grandfathered a card on a board whose cutover is still unestablished", tc.name)
			}
		})
	}
}

// csProjectAs is gwFixture.project with the READER named explicitly, because the
// question below is specifically about a reader who holds NOTHING — a stranger
// who was never granted anything — rather than the board's owner.
func csProjectAs(t *testing.T, f *gwFixture, served []*nostr.Event, reader *nostr.Key) (map[string]*state.Item, *BoardKeyring) {
	t.Helper()
	kr := DeriveBoardKeyring(served, reader, f.owner.PubKeyHex(), f.boardD)
	return ProjectItems(served, ProjectOptions{
		Trusted:         map[string]bool{f.owner.PubKeyHex(): true},
		PinnedBoard:     f.coord,
		Decryptor:       kr,
		EncryptedBoards: kr,
	}), kr
}

// TestAssertionWithNoGrantsEstablishesTheInstantNotReadAccess answers the
// ready-475 REWORK's second question and pins the answer: routing the assertion
// through kr.cutover means Cutover() can now report ok=true from the ASSERTION
// ALONE, with zero grants. What does that mean for a reader holding no key at all?
//
// THE RULING: it establishes the INSTANT and nothing else. `ok=true` is not a
// claim to have read the board — it is the fold gate's "this board is
// confidential, apply §11.3/§11.4 at this instant". So the gate comes ON, the
// board's pre-assertion plaintext is grandfathered, its post-assertion plaintext
// is quarantined, and the reader's key material is EXACTLY what it was: empty. No
// CEK, no LTK, no current epoch, and every sealed card still renders §11.7's
// placeholder. A board this reader cannot read stays a board it cannot read.
//
// WHY THAT IS THE RIGHT ANSWER AND NOT A FAIL-OPEN. Against this reader's
// alternative — today's Go answer with no grants, `ok=false`, the gate INERT —
// the assertion is strictly TIGHTENING: the control arm below folds the
// post-cutover leak in clear and the asserted arm withholds it. The only input
// that can produce this state is the board owner's own signature over their own
// coordinate; a relay cannot forge one (no key), edit one (the tag is inside the
// signed id), re-author one (the coordinate embeds the author), or post-date one
// (AssertedConfidentialSince takes the minimum). So the widening this DOES cause
// — the browser's `no-grant` arm would have withheld the pre-cutover card, and
// with an assertion it shows it — can only ever be authorised by the one key that
// is already the board's authz root for every CEK it ever minted (§11.12).
//
// GUARD: the `!derived` half of DeriveBoardKeyring's `if at, derived :=
// kr.cutover[coord]; !derived || since < at` — i.e. the assertion applying when
// NOTHING was derived. Require a derived cutover to compare against and this goes
// RED on the leak: with no grant there is nothing to compare against, the cutover
// stays unset, and the board reads as plaintext again.
func TestAssertionWithNoGrantsEstablishesTheInstantNotReadAccess(t *testing.T) {
	f := gwNewFixture(t)
	stranger := kdKey(t)
	// NO GRANT of any kind — not the epoch-1 one, not the rotation. A sealed card
	// is here so the reader has something it demonstrably cannot read.
	noGrants := func(board *nostr.Event) []*nostr.Event {
		return []*nostr.Event{board, f.sealed1, f.preClear, f.leak}
	}

	// CONTROL — the same events with no assertion. This is today's Go answer for a
	// grantless board: no cutover, gate inert, and the post-cutover plaintext the
	// board's real cutover would quarantine folds IN CLEAR.
	items, kr := csProjectAs(t, f, noGrants(f.board), stranger)
	if cut, ok := kr.Cutover(f.coord); ok || cut != 0 {
		t.Fatalf("no assertion, no grants: cutover = %d (ok=%v), want (0,false) — the fixture is not the shape this test needs", cut, ok)
	}
	if items["ready-gw-leak"] == nil {
		t.Fatal("no assertion, no grants: the leak did not fold, so the asserted arm below cannot show the assertion TIGHTENED anything")
	}

	// THE ASSERTION, ALONE. The gate comes on at the instant the owner stated.
	items, kr = csProjectAs(t, f, noGrants(csAssertedBoard(t, f, gwT0)), stranger)
	if cut, ok := kr.Cutover(f.coord); !ok || cut != gwT0 {
		t.Fatalf("asserted, no grants: cutover = %d (ok=%v), want (%d,true)", cut, ok, gwT0)
	}
	if it := items["ready-gw-leak"]; it != nil {
		t.Fatalf("FAIL-OPEN: post-cutover plaintext folded under an assertion with no grants: title=%q", it.Title)
	}
	if items["ready-gw-old"] == nil {
		t.Fatal("asserted, no grants: the owner stated the instant, so genuinely pre-cutover plaintext must still fold")
	}

	// AND IT CONFERRED NOTHING TO READ WITH. Establishing WHEN the board went
	// confidential is not being able to read it, and this is the half that would
	// make the ruling wrong if it failed.
	if _, ok := kr.CEK(f.coord, 1); ok {
		t.Fatal("the assertion handed a reader with no grant a CEK")
	}
	if _, ok := kr.LTK(f.coord); ok {
		t.Fatal("the assertion handed a reader with no grant an LTK")
	}
	if ep, _, ok := kr.CurrentEpoch(f.coord); ok {
		t.Fatalf("the assertion gave a reader with no grant a current epoch (%d) to seal writes under", ep)
	}
	sealed := items["ready-gw-sealed"]
	if sealed == nil {
		t.Fatal("asserted, no grants: the well-formed sealed card was dropped by the gate")
	}
	if sealed.Title != placeholderText {
		t.Fatalf("the assertion REVEALED a sealed card's free text: title = %q, want %q", sealed.Title, placeholderText)
	}
}

// TestOmittedAssertionYieldsTodaysBehaviour is PROPERTY 3, and it is the one that
// matters against an untrusted relay: the assertion is the only new input, and a
// relay's only power over it is to WITHHOLD it. Withholding must land the reader
// on today's behaviour — which withholds MORE, not less — so omission gains an
// attacker nothing.
//
// GUARD: the witness path DeriveBoardKeyring still runs when no assertion is
// found — i.e. the fact that the assertion's early return is conditional at all.
// Delete that path (make the assertion branch's "the witnesses have nothing left
// to establish" apply unconditionally) and the omitted arm goes RED: the
// manufactured cutover t0+600 is believed and the leak folds in clear. Verified
// red with that one change, green with it restored.
func TestOmittedAssertionYieldsTodaysBehaviour(t *testing.T) {
	f := gwNewFixture(t)
	asserted := csAssertedBoard(t, f, gwT0)

	// SERVED — the reader sees the board as its owner means it to be seen.
	items, kr := f.project(t, csPollutedAnswer(f, asserted))
	if cut, ok := kr.Cutover(f.coord); !ok || cut != gwT0 {
		t.Fatalf("served: cutover = %d (ok=%v), want (%d,true)", cut, ok, gwT0)
	}
	if items["ready-gw-old"] == nil {
		t.Fatal("served: the assertion did not restore the board's pre-cutover history")
	}

	// OMITTED — the relay drops the definition event entirely. Exactly today's
	// answer: the witness fires, the cutover is unusable, nothing is grandfathered.
	served := []*nostr.Event{f.sealed1, f.preClear, f.leak, f.grant2}
	items, kr = f.project(t, served)
	if cut, ok := kr.Cutover(f.coord); !ok || cut != 0 {
		t.Fatalf("omitted: cutover = %d (ok=%v), want the fail-closed (0,true)", cut, ok)
	}
	if it := items["ready-gw-leak"]; it != nil {
		t.Fatalf("FAIL-OPEN: dropping the definition WIDENED the board — %q folded in clear", it.Title)
	}
	if items["ready-gw-old"] != nil {
		t.Fatal("omitted: dropping the definition grandfathered a card that today's reader withholds")
	}
}
