package foldvectors

// cases_confidentialsince.go — §11.13a's OWNER-SIGNED CUTOVER ASSERTION
// (ready-475): `confidential_since` on the board's own kind-30301 definition.
//
// WHY THESE VECTORS EXIST AT ALL. The assertion changes what BOTH readers derive
// from the same events — pkg/sync/keydist.go and
// web/board/src/lib/confidentiality.ts — and a rule two independent readers apply
// is exactly what the vector file is for. Without a vector, one of them could
// start honouring an assertion the other ignores and every suite would stay
// green while the browser and `rd` showed different boards.
//
// EVERY CASE HERE IS DELIBERATELY OUTSIDE THE DIVERGENCE ZONE (§4's rule, see
// cases_epochmodel.go). None of them contains a CONTRADICTED board: a
// contradiction is precisely where the two readers report the cutover
// differently (Go's Cutover applies the withheld flag, the browser's keyring
// reports the raw derived instant and its DECISION layer applies §11.13a), so a
// vector asserting expect.keyring on such a board could not be satisfied by
// both. What is pinned instead is the assertion's own arithmetic, which both
// readers must agree on to the second:
//
//	honoured   -> the effective cutover is the ASSERTED instant, and it is EARLIER
//	              than the grants alone derive, so it quarantines a card the
//	              grant-minimum would have grandfathered.
//	min()      -> an assertion LATER than the earliest served grant does NOT
//	              apply. It can only ever move the instant earlier.
//	foreign    -> an assertion on a STRANGER's board definition is not this
//	              board's, and changes nothing.
//	absent     -> today's answer exactly.
//	no grants  -> the assertion establishes the INSTANT with zero grants served,
//	              and confers NO read access. See that case's own doc: it is the
//	              one shape here that would otherwise BE the divergence zone, and
//	              the assertion is what takes it out.
//
// EACH IS FALSIFIABLE IN THE ITEMS, not only in expect.keyring: every case turns
// on one plaintext card that folds under one reading and is quarantined under the
// other. A client that ignores `confidential_since` folds a card the first vector
// says is absent; a client that honours a foreign one loses a card the third
// vector says is present.

import (
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// The instants these four cases share. The gap between the TRUTH and what the
// served grants derive is the whole subject: it is the window §11.13's minimum
// gets wrong whenever the earliest grants are not in the answer, and the window
// an owner's assertion closes.
const (
	csTrueCutover  = t0 - 500 // what the owner ASSERTS: when the board really went confidential
	csOldPlaintext = t0 - 600 // before the truth: grandfathered under every reading here
	csGapPlaintext = t0 - 400 // AFTER the truth, BEFORE the only served grant
	csServedGrant  = t0 - 300 // the one owner CEK grant this answer carries
	csLateAssert   = t0 - 200 // an assertion later than the served grant: must not apply
	csSealedCard   = t0 - 250 // a sealed card, for the no-grant case's reader to NOT read
)

// csBoard builds the board's own kind-30301 definition, optionally carrying the
// assertion, signed by k. Signed by a NAMED key rather than always the owner
// because one case's whole point is a definition the owner did not sign.
func (b *builder) csBoard(k *nostr.Key, since int64) (*nostr.Event, error) {
	return rdsync.BuildBoardEventWithConfidentialSince(k, rdsync.BoardSpec{
		BoardD: boardD, Title: "ready",
	}, since, t0-1000)
}

// csEvents is the event log all four cases share apart from the board
// definition(s): ONE owner CEK grant at csServedGrant — a member admitted after
// the board went confidential, which is the ordinary way a grant set ends up
// with nothing older in it — plus the two plaintext cards the readings differ
// about.
//
// There is deliberately NO sealed card. A sealed card older than the derived
// cutover would fire §11.13a's TIME witness and put the board in the contradicted
// state, which is the divergence zone; here the assertion is measured on its own.
func (b *builder) csEvents(defs ...*nostr.Event) ([]*nostr.Event, error) {
	grant, err := b.cekGrant(b.maintPub, cekEpoch1Hex, 1, "epoch-1 key material", csServedGrant)
	if err != nil {
		return nil, err
	}
	old, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v75a", Title: "plaintext from before the board went confidential",
		Status: state.StatusActive, Priority: "p1", Type: "task",
	}, csOldPlaintext)
	if err != nil {
		return nil, err
	}
	gap, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v75b", Title: "plaintext from the window the grants understate",
		Status: state.StatusActive, Priority: "p1", Type: "task",
	}, csGapPlaintext)
	if err != nil {
		return nil, err
	}
	return append(append([]*nostr.Event{}, defs...), grant, old, gap), nil
}

// csOldItem is ready-v75a as projected: the pre-cutover plaintext card, which
// every case here grandfathers (§11.4).
func csOldItem(ev []*nostr.Event) *state.Item {
	return &state.Item{
		ID: "ready-v75a", MsgID: ev[len(ev)-2].ID,
		Title: "plaintext from before the board went confidential",
		Type:  "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(csOldPlaintext), UpdatedAt: nanos(csOldPlaintext),
	}
}

// csGapItem is ready-v75b as projected: the card in the window between the truth
// and the served grant. Present only when the cutover in force is the GRANT's.
func csGapItem(ev []*nostr.Event) *state.Item {
	return &state.Item{
		ID: "ready-v75b", MsgID: ev[len(ev)-1].ID,
		Title: "plaintext from the window the grants understate",
		Type:  "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(csGapPlaintext), UpdatedAt: nanos(csGapPlaintext),
	}
}

// vConfidentialSinceEstablishesCutover pins the rule itself: an owner-signed
// `confidential_since` is the cutover, in preference to the minimum over served
// grants.
//
// The served grant is at csServedGrant and the assertion is 200 seconds EARLIER,
// so honouring it QUARANTINES ready-v75b — a card §11.4 would otherwise
// grandfather. That direction is chosen on purpose: it makes the vector
// falsifiable by a client that ignores the tag (the card appears), and it is the
// direction that cannot fail open.
func (b *builder) vConfidentialSinceEstablishesCutover() error {
	def, err := b.csBoard(b.owner, csTrueCutover)
	if err != nil {
		return err
	}
	ev, err := b.csEvents(def)
	if err != nil {
		return err
	}
	items, err := itemsJSON(csOldItem(ev))
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_confidential_since_establishes_the_cutover",
		SpecClauses: []string{"11.13", "11.3", "11.4"},
		Note: "The board's own kind-30301 definition carries an owner-signed \"confidential_since\" tag " +
			"naming an instant 200 seconds EARLIER than the only owner CEK grant in this log. §11.13a " +
			"honours the assertion in preference to the grant minimum, so the cutover in force is the " +
			"asserted instant: ready-v75b, plaintext and authored between the two, is post-cutover " +
			"cleartext and §11.3 quarantines it. A client that ignores the tag derives the grant's " +
			"instant instead, §11.4 grandfathers that card, and it appears — this vector's own event " +
			"log carries the title it would print. ready-v75a, older than both readings, folds either " +
			"way and is here so the case cannot pass by quarantining everything. expect.keyring.cutover " +
			"pins the asserted instant directly.",
		Options: b.readerKeyring(secMaint),
		Events:  ev,
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v75a"}, "work": {"ready-v75a"}, "focus": {"ready-v75a"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: true, Cutover: csTrueCutover, CurrentEpoch: 1},
		},
	})
}

// vConfidentialSinceNeverMovesTheCutoverLater pins the min(): an assertion LATER
// than the earliest served grant does not apply. Without it, an owner (or a relay
// replaying a stale definition) could grandfather cleartext the served grants
// alone quarantine — the one direction this mechanism must never move.
func (b *builder) vConfidentialSinceNeverMovesTheCutoverLater() error {
	def, err := b.csBoard(b.owner, csLateAssert)
	if err != nil {
		return err
	}
	ev, err := b.csEvents(def)
	if err != nil {
		return err
	}
	items, err := itemsJSON(csOldItem(ev), csGapItem(ev))
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_confidential_since_never_moves_the_cutover_later",
		SpecClauses: []string{"11.13", "11.4"},
		Note: "The same board, asserting an instant 100 seconds LATER than its own earliest served CEK " +
			"grant. The effective cutover is min(asserted, derived), so the assertion does not apply " +
			"and the grant's instant stands: ready-v75b, authored before it, is grandfathered by §11.4 " +
			"and folds. A client that takes the asserted instant outright quarantines that card, which " +
			"is the fail-OPEN direction wearing the opposite sign — it would let an assertion " +
			"grandfather cleartext the served grants already refuse. Note this vector and " +
			"keyring_confidential_since_establishes_the_cutover differ in ONE tag value.",
		Options: b.readerKeyring(secMaint),
		Events:  ev,
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v75a", "ready-v75b"}, "work": {"ready-v75a", "ready-v75b"},
				"focus": {"ready-v75a", "ready-v75b"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: true, Cutover: csServedGrant, CurrentEpoch: 1},
		},
	})
}

// vConfidentialSinceForeignSignerIgnored pins the security property: the
// assertion is the BOARD OWNER's, and a kind-30301 signed by anybody else is a
// different board's definition, whatever its "d" tag says.
func (b *builder) vConfidentialSinceForeignSignerIgnored() error {
	own, err := b.csBoard(b.owner, 0) // the real definition, asserting nothing
	if err != nil {
		return err
	}
	foreign, err := b.csBoard(b.outsider, csTrueCutover)
	if err != nil {
		return err
	}
	ev, err := b.csEvents(own, foreign)
	if err != nil {
		return err
	}
	items, err := itemsJSON(csOldItem(ev), csGapItem(ev))
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_confidential_since_foreign_signer_is_ignored",
		SpecClauses: []string{"11.13", "4.1", "11.4"},
		Note: "A kind-30301 carrying \"confidential_since\" and this board's \"d\" tag, correctly signed " +
			"by an OUTSIDER. Its coordinate is 30301:<outsider>:ready, not this board's, so it asserts " +
			"nothing here and the cutover stays the grant minimum — ready-v75b, which the outsider's " +
			"instant would have quarantined, folds. The board's own definition is in the log too, " +
			"carrying no assertion, so the only difference between this and " +
			"keyring_confidential_since_establishes_the_cutover is WHO signed the tag. A client that " +
			"matches the definition on its \"d\" tag alone, without binding the author, loses that card " +
			"— and on a real board would let any pubkey pick another board's cutover.",
		Options: b.readerKeyring(secMaint),
		Events:  ev,
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v75a", "ready-v75b"}, "work": {"ready-v75a", "ready-v75b"},
				"focus": {"ready-v75a", "ready-v75b"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: true, Cutover: csServedGrant, CurrentEpoch: 1},
		},
	})
}

// vConfidentialSinceWithNoGrantsEstablishesTheInstantNotReadAccess is the
// ready-475 REWORK's second question, decided and pinned: what an assertion means
// to a reader holding NO KEY AT ALL.
//
// THE ANSWER. The assertion establishes the INSTANT and nothing else. The board
// is confidential from csTrueCutover — the gate is ON, plaintext older than it is
// grandfathered (§11.4) and plaintext newer than it is quarantined (§11.3) — and
// the reader's key material is exactly what it was: EMPTY. `current_epoch` is 0,
// and the sealed card renders the §11.7 placeholder. Establishing WHEN a board
// went confidential and being able to READ it are different facts, and only the
// first one is the owner's to state.
//
// THIS IS THE ONE CASE HERE THAT WOULD OTHERWISE SIT IN THE DIVERGENCE ZONE, and
// the assertion is what takes it out — which is the reason it earns a vector
// rather than only a per-reader test. "A sealed card plus no derived cutover" is
// precisely the shape cases_epochmodel.go's header records as unsatisfiable by
// both readers at once: rd's Go fold reports no cutover and lets the plaintext
// through, while the browser reads the same snapshot as §11.13a's `no-grant` arm
// and quarantines everything. Serve an owner-signed `confidential_since` and the
// question both readers were guessing at is ANSWERED, so they converge on the
// same instant, the same gate, and the same items — with no grant anywhere in the
// log. A client that only consults the assertion when it already has a derived
// cutover to compare against fails this vector in both directions at once: it
// loses ready-v75a (Go-shaped: gate inert, then quarantine-nothing is wrong the
// other way) or admits ready-v75b.
//
// AND THE ASSERTION IS DOING REAL WORK HERE RATHER THAN COINCIDING WITH THE
// DEFAULT: with no grants and no assertion this board has no cutover at all, so
// BOTH plaintext cards would fold under §11.13 (Go) or NEITHER would (browser).
// Only the assertion produces the split this vector expects.
func (b *builder) vConfidentialSinceWithNoGrantsEstablishesTheInstantNotReadAccess() error {
	def, err := b.csBoard(b.owner, csTrueCutover)
	if err != nil {
		return err
	}
	cek, err := hexKey(cekEpoch1Hex)
	if err != nil {
		return err
	}
	// Sealed under a real epoch-1 CEK whose GRANT is not in this log at all — so
	// no reader of this vector can hold it, which is the point.
	sealed, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v75c", Title: "sealed, and no grant for it was ever served",
		Status: state.StatusActive, Priority: "p1", Type: "task",
		Context: "body sealed under the epoch-1 key", Enc: &rdsync.Envelope{CEK: cek, Epoch: 1},
	}, csSealedCard)
	if err != nil {
		return err
	}
	old, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v75a", Title: "plaintext from before the board went confidential",
		Status: state.StatusActive, Priority: "p1", Type: "task",
	}, csOldPlaintext)
	if err != nil {
		return err
	}
	gap, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v75b", Title: "plaintext from the window the grants understate",
		Status: state.StatusActive, Priority: "p1", Type: "task",
	}, csGapPlaintext)
	if err != nil {
		return err
	}
	ev := []*nostr.Event{def, sealed, old, gap}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v75a", MsgID: old.ID,
			Title: "plaintext from before the board went confidential",
			Type:  "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(csOldPlaintext), UpdatedAt: nanos(csOldPlaintext),
		},
		&state.Item{
			ID: "ready-v75c", MsgID: sealed.ID,
			Title: "[encrypted]", Context: "[encrypted]", Description: "[encrypted]",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(csSealedCard), UpdatedAt: nanos(csSealedCard),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_confidential_since_with_no_grants_establishes_the_instant_not_read_access",
		SpecClauses: []string{"11.13", "11.3", "11.4", "11.7", "11.14"},
		Note: "The board's own definition asserts \"confidential_since\", and the log carries NO owner CEK " +
			"grant at all — the shape a reader who was never granted anything, or whose relay answer " +
			"omitted every grant, actually sees. The assertion ESTABLISHES the cutover on its own: the " +
			"gate is ON at the asserted instant, so ready-v75a (older) is grandfathered by §11.4 and " +
			"ready-v75b (newer) is quarantined by §11.3. It confers NO read access — " +
			"expect.keyring.current_epoch is 0 and ready-v75c, sealed under an epoch whose grant is " +
			"nowhere in this log, renders §11.7's \"[encrypted]\" placeholder. Establishing WHEN a board " +
			"went confidential and being able to READ it are different facts, and only the first is the " +
			"owner's to state. A client that consults the assertion only as a MINIMUM against an " +
			"already-derived cutover has none to compare against here and fails: it either lets " +
			"ready-v75b through (no gate) or withholds ready-v75a (fail-closed with no instant).",
		Options: b.readerKeyring(secMaint),
		Events:  ev,
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v75a", "ready-v75c"}, "work": {"ready-v75a", "ready-v75c"},
				"focus": {"ready-v75a", "ready-v75c"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: true, Cutover: csTrueCutover, CurrentEpoch: 0},
		},
	})
}

// vConfidentialSinceAbsentIsTodaysBehaviour is the control, and it is the
// property that makes this an EXTENSION: a board that asserts nothing derives
// exactly what §11.13 always derived. It is also what a relay that WITHHOLDS the
// definition leaves a reader with — the only power a relay has over an assertion
// it cannot forge — so this vector is what says omission gains nothing.
func (b *builder) vConfidentialSinceAbsentIsTodaysBehaviour() error {
	ev, err := b.csEvents() // no board definition event at all
	if err != nil {
		return err
	}
	items, err := itemsJSON(csOldItem(ev), csGapItem(ev))
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_confidential_since_absent_is_todays_behaviour",
		SpecClauses: []string{"11.13", "11.4"},
		Note: "The identical log with NO board definition event — which is also what a relay that " +
			"withholds the definition serves. Nothing asserts a cutover, so §11.13's minimum over the " +
			"served owner CEK grants stands and ready-v75b is grandfathered: a board that carries no " +
			"assertion behaves exactly as it did before §11.13a's assertion existed, and withholding " +
			"one can only ever land a reader here. A client that treats an ABSENT or malformed tag as " +
			"an assertion (at instant 0, say) quarantines both cards and fails on the missing items.",
		Options: b.readerKeyring(secMaint),
		Events:  ev,
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v75a", "ready-v75b"}, "work": {"ready-v75a", "ready-v75b"},
				"focus": {"ready-v75a", "ready-v75b"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: true, Cutover: csServedGrant, CurrentEpoch: 1},
		},
	})
}
