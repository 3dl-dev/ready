package foldvectors

// cases_epochmodel.go — the CONFIDENTIAL-ENVELOPE EPOCH MODEL (ready-882),
// spec §11.10-§11.14.
//
// WHY THESE VECTORS NEEDED A NEW OPTION SHAPE. Every confidential vector before
// this file handed the fold its key material as literal data — `options.decryptor`
// lists (coordinate, epoch) -> CEK, `options.encrypted_boards` states the cutover
// outright. That shape can pin what a reader DOES with keys (§11.6-§11.9) but
// says nothing about where the keys came from, and the epoch model is entirely
// about where they came from:
//
//	§11.10 an epoch is an integer >= 1; a grant below that contributes NEITHER a
//	       key NOR a cutover.
//	§11.11 a rotation is a fresh, unrelated key at OldEpoch + 1.
//	§11.12 derivation retains ALL epochs — it is NOT latest-wins.
//	§11.13 the cutover is the EARLIEST owner CEK grant, whoever it is addressed to.
//	§11.14 the current epoch (what a write seals under) is the HIGHEST one held.
//
// So these vectors use `options.keyring` (see KeyringSpec): the reader is a
// SECRET, the grants are ordinary signed events in the log, and the client has to
// run the derivation to get anything at all. Feed one of these vectors to an
// implementation that ignores the field and it folds with no keys and no cutover,
// which every expectation here distinguishes.
//
// WHERE THE EXPECTATIONS COME FROM. Each one is read off the clause, and each is
// chosen so a plausible WRONG derivation is visible in the projected items, not
// only in the directly-asserted `expect.keyring`:
//
//	latest-wins keyring        -> the pre-rotation card renders "[encrypted]".
//	cutover from MY grants     -> a plaintext card the board should have
//	                              quarantined renders in clear.
//	epoch 0 accepted           -> a card sealed under a bogus epoch decrypts, and
//	                              a legitimate plaintext card vanishes behind a
//	                              cutover that should not exist.
//
// `expect.keyring` then pins the same three answers directly, because §11.14's
// current-epoch selection has no item-level consequence on a READ at all — it is
// the epoch a WRITE seals under, and a browser that picks it wrong publishes
// cards its own board cannot read.

import (
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// Epoch fixture keys. Distinct 32-byte values, and epoch 2's is UNRELATED to
// epoch 1's on purpose: §11.11 mints a rotation key from crypto/rand, never from
// its predecessor, so a client that tried to derive the successor (rather than
// unwrap the grant that carries it) produces the wrong bytes and fails the AEAD.
const (
	cekEpoch1Hex   = "1111000022220000333300004444000055550000666600007777000088880000"
	cekEpoch2Hex   = "9e9e0f0f8d8d1e1e7c7c2d2d6b6b3c3c5a5a4b4b49495a5a38386969272778f8"
	cekBogusEpoch  = "abababababababababababababababababababababababababababababababab"
	cekSharedEpoch = "5c5c5c5c4d4d4d4d3e3e3e3e2f2f2f2f10101010212121213232323243434343"
)

// cekGrant builds the owner-signed kind-39301 grant that CARRIES a wrapped CEK —
// the only kind of event that can make a board confidential (§11.12: only the
// board author mints CEKs) and the only source of a cutover (§11.13).
func (b *builder) cekGrant(granteePub, cekHex string, epoch int, label string, createdAt int64) (*nostr.Event, error) {
	cek, err := hexKey(cekHex)
	if err != nil {
		return nil, err
	}
	wrapped, err := rdsync.WrapKey(b.owner, granteePub, cek)
	if err != nil {
		return nil, err
	}
	return rdsync.BuildRoleGrantEvent(b.owner, rdsync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: b.ownerPub, Grantee: granteePub,
		Role: rdsync.RoleContributor, Label: label,
		WrappedCEK: wrapped, CEKEpoch: epoch,
	}, createdAt)
}

// readerKeyring is the derived-keyring option for a reader identified by its
// SECRET (the fixture secrets are published in the file's `keys`).
func (b *builder) readerKeyring(readerSecret string) Options {
	return Options{
		Trusted: trust(b.ownerPub),
		Keyring: &KeyringSpec{ReaderSecret: readerSecret, BoardAuthor: b.ownerPub, BoardD: boardD},
	}
}

// §11.10's sentence — "it contributes neither a key nor a cutover" — is pinned by
// TWO vectors, and the split is not cosmetic. THIS IS THE ready-882 REWORK; read
// this before merging them back.
//
// The two halves fail in opposite directions, so the obvious fixture puts one of
// each in a single vector:
//
//	accepting the KEY     -> a card sealed under the bogus epoch decrypts, so the
//	                         reader renders content the epoch model says it cannot
//	                         hold.
//	accepting the CUTOVER -> the board is treated as confidential from that grant
//	                         on, so a legitimate plaintext card written afterwards
//	                         is quarantined (§11.3) and DISAPPEARS.
//
// Round 1 did exactly that, and the combined vector was UNSATISFIABLE BY THE
// SHIPPED BROWSER. A sealed card plus no derived cutover is the one board shape on
// which the two conformant readers diverge:
//
//	rd's Go fold applies §11.13 alone — no cutover derived, so Cutover() reports
//	    the board plaintext, the quarantine gate is inert, and the plaintext card
//	    folds. §11.13a states outright that "the Go reader does not yet apply
//	    §11.13a" (ready-9a6).
//	The browser applies §11.13a on top: a derived cutover is a LOWER BOUND, and a
//	    verified sealed card with NO served owner grant is the "no-grant" arm of
//	    the unestablished state — state "unknown", gate ON, cutover 0 — so the
//	    plaintext card is quarantined and the item never appears.
//
// WHICH SIDE IS WRONG, ESTABLISHED FROM THE CLAUSE TEXT AND NOT FROM RUNNING THE
// FOLD: neither. §11.10 is a statement about DERIVATION and both readers obey it
// identically (both reject the grant, so no key and no cutover). §11.13a then
// says a reader "MUST treat a derived cutover as unusable when the board's own
// snapshot CONTRADICTS it", "fails closed exactly as for 'no grant at all' — gate
// ON, cutover 0", and names confidentialityOf as its reference implementation; the
// same clause records that the Go reader does not implement it. So the divergence
// is SANCTIONED BY THE SPEC, and the thing at fault is the FIXTURE: a conformance
// vector is the shared contract, so its expectations must hold for every
// conformant reader, and this one could only ever hold for one of them.
//
// The fix is therefore to keep both halves of §11.10 falsifiable while keeping each
// fixture OUT of the divergence zone:
//
//	no key     -> sealed card only. Both readers agree, because a well-formed
//	              sealed envelope is never quarantined by either gate state
//	              (§11.3), and it still renders "[encrypted]" iff the epoch-0 CEK
//	              was rejected.
//	no cutover -> plaintext card only, and NO sealed card, so §11.13a's witnesses
//	              have nothing to testify about and both readers see a plaintext
//	              board. The card still vanishes the moment epoch 0 mints a cutover.
//
// web/board/src/lib/fold.vectors.test.ts's "projects identically under §11.13 and
// under §11.13a" test is what keeps the zone empty from here on, and it folds with
// the browser's REAL adapter rather than a local restatement of it.

// vKeyringEpochZeroYieldsNoKey pins the first half of §11.10: a grant whose
// cek_epoch is below 1 is rejected outright, so it binds NO key, so a card sealed
// under that epoch cannot be read (§11.6 refuses to resolve a key the keyring
// never bound; §11.7 fails closed to the placeholder).
func (b *builder) vKeyringEpochZeroYieldsNoKey() error {
	cek, err := hexKey(cekBogusEpoch)
	if err != nil {
		return err
	}
	// An owner-signed grant carrying a real wrapped key at epoch 0.
	grant, err := b.cekGrant(b.maintPub, cekBogusEpoch, 0, "epoch-0 key material", t0-300)
	if err != nil {
		return err
	}
	sealed, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v51a", Title: "sealed under epoch zero", Status: state.StatusActive, Priority: "p1", Type: "task",
		Context: "body sealed with the epoch-0 key", Enc: &rdsync.Envelope{CEK: cek, Epoch: 0},
	}, t0-200)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v51a", MsgID: sealed.ID,
			Title: "[encrypted]", Context: "[encrypted]", Description: "[encrypted]",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0 - 200), UpdatedAt: nanos(t0 - 200),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_epoch_zero_grant_yields_no_key",
		SpecClauses: []string{"11.10", "11.6", "11.7", "12.1"},
		Note: "The board's ONLY CEK-bearing grant is owner-signed, carries a genuine NIP-44 wrap of a " +
			"real 32-byte key, and names cek_epoch 0. §11.10 rejects it outright — epochs are integers " +
			">= 1 — so it binds NO key: ready-v51a, sealed under epoch 0 with exactly that CEK, must " +
			"render the \"[encrypted]\" placeholder (§11.6 refuses to resolve a key the keyring never " +
			"bound, §11.7 fails closed). An implementation that accepted epoch 0 reads a card it must " +
			"not, and the title it would print is in this vector's own event log. §12.1 is the sibling " +
			"half of the same guard — an UNPARSEABLE cek_epoch coerces to 0, landing in this same " +
			"rejection rather than defaulting to a real epoch. The companion vector " +
			"keyring_epoch_zero_grant_yields_no_cutover pins the other half of §11.10's sentence; the " +
			"two are separate because a sealed card with no derived cutover is the one shape on which " +
			"§11.13 and §11.13a legitimately disagree (see cases_epochmodel.go).",
		Options: b.readerKeyring(secMaint),
		Events:  []*nostr.Event{grant, sealed},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v51a"}, "work": {"ready-v51a"}, "focus": {"ready-v51a"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: false, Cutover: 0, CurrentEpoch: 0},
		},
	})
}

// vKeyringEpochZeroYieldsNoCutover pins the second half of §11.10: the rejected
// grant contributes no CUTOVER either, so the board is not confidential (§11.13:
// Cutover reporting ok=true is exactly "this board is confidential"), the fold
// gate is inert, and a plaintext card written AFTER that grant folds in clear
// instead of being quarantined (§11.3).
//
// There is deliberately no sealed card here: with none, §11.13a's two witnesses
// have nothing to testify about and every conformant reader sees a plaintext
// board, which is what makes this expectation a shared contract rather than a
// statement about one reader.
func (b *builder) vKeyringEpochZeroYieldsNoCutover() error {
	// An owner-signed grant carrying a real wrapped key at epoch 0.
	grant, err := b.cekGrant(b.maintPub, cekBogusEpoch, 0, "epoch-0 key material", t0-300)
	if err != nil {
		return err
	}
	// AFTER the epoch-0 grant. If that grant had set a cutover this card would be
	// post-cutover cleartext and quarantined (§11.3); it must fold.
	plain, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v51b", Title: "plaintext after the epoch-0 grant", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0-100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v51b", MsgID: plain.ID, Title: "plaintext after the epoch-0 grant",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0 - 100), UpdatedAt: nanos(t0 - 100),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_epoch_zero_grant_yields_no_cutover",
		SpecClauses: []string{"11.10", "11.13", "11.3", "11.4", "12.1"},
		Note: "The same owner-signed epoch-0 grant as keyring_epoch_zero_grant_yields_no_key, and the " +
			"other half of §11.10's sentence: it contributes no CUTOVER. expect.keyring.confidential is " +
			"FALSE — §11.13 makes \"a cutover was derived\" exactly \"this board is confidential\" — so " +
			"the fold gate is inert and ready-v51b, plaintext and written AFTER the grant, folds in " +
			"clear. An implementation that accepted epoch 0 sets the cutover at the grant's created_at, " +
			"which is EARLIER than this card, so §11.4 cannot grandfather it and §11.3 drops it: the " +
			"item disappears entirely and the vector goes red on a missing item, not on a field. The " +
			"board carries no sealed card on purpose, so nothing here depends on whether a reader also " +
			"applies §11.13a (see cases_epochmodel.go).",
		Options: b.readerKeyring(secMaint),
		Events:  []*nostr.Event{grant, plain},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v51b"}, "work": {"ready-v51b"}, "focus": {"ready-v51b"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: false, Cutover: 0, CurrentEpoch: 0},
		},
	})
}

// vKeyringRetainsEveryEpoch pins §11.12 (all epochs retained, NOT latest-wins),
// §11.11 (a rotation is a fresh unrelated key at OldEpoch + 1, arriving as a
// SECOND grant rather than as a replacement) and §11.14 (the current epoch is the
// highest held, which is what a write must seal under).
//
// §11.11a is the reason this must hold: a rotation re-keys the FUTURE and never
// re-seals history, so every card sealed under an earlier epoch stays sealed
// under it forever. A keyring that kept only the newest epoch would render the
// entire pre-rotation board as placeholders — the exact failure measured on the
// live relay when the epoch grants shared one addressable slot (§16.10).
func (b *builder) vKeyringRetainsEveryEpoch() error {
	cek1, err := hexKey(cekEpoch1Hex)
	if err != nil {
		return err
	}
	cek2, err := hexKey(cekEpoch2Hex)
	if err != nil {
		return err
	}
	g1, err := b.cekGrant(b.maintPub, cekEpoch1Hex, 1, "bootstrap epoch 1", t0-300)
	if err != nil {
		return err
	}
	cardE1, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v52a", Title: "sealed before the rotation", Status: state.StatusActive, Priority: "p1", Type: "task",
		Context: "epoch 1 body", Enc: &rdsync.Envelope{CEK: cek1, Epoch: 1},
	}, t0-200)
	if err != nil {
		return err
	}
	// THE ROTATION (§11.11): a fresh key at OldEpoch + 1, published as another
	// owner grant. It does not replace g1 in the log and does not re-seal
	// ready-v52a (§11.11a).
	g2, err := b.cekGrant(b.maintPub, cekEpoch2Hex, 2, "rotation to epoch 2", t0-100)
	if err != nil {
		return err
	}
	cardE2, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v52b", Title: "sealed after the rotation", Status: state.StatusActive, Priority: "p1", Type: "task",
		Context: "epoch 2 body", Enc: &rdsync.Envelope{CEK: cek2, Epoch: 2},
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v52a", MsgID: cardE1.ID, Title: "sealed before the rotation",
			Context: "epoch 1 body", Description: "epoch 1 body",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0 - 200), UpdatedAt: nanos(t0 - 200),
		},
		&state.Item{
			ID: "ready-v52b", MsgID: cardE2.ID, Title: "sealed after the rotation",
			Context: "epoch 2 body", Description: "epoch 2 body",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_retains_every_epoch_across_a_rotation",
		SpecClauses: []string{"11.12", "11.11", "11.14", "11.13", "11.6", "11.9"},
		Note: "Two owner-signed CEK grants to the same reader: epoch 1, then a rotation to epoch 2 " +
			"carrying a FRESH, unrelated key (§11.11 — the successor is never derived from its " +
			"predecessor, so a client that computed it instead of unwrapping the grant fails the AEAD " +
			"and renders ready-v52b as a placeholder). Derivation scans EVERY historical grant rather " +
			"than latest-wins (§11.12), so BOTH cards decrypt: the rotation closed the future, it did " +
			"not recall the past (§11.11a — history is never re-sealed). A latest-wins keyring renders " +
			"the pre-rotation ready-v52a as \"[encrypted]\", which is the whole board on any project " +
			"that has ever rotated. expect.keyring pins the two derived scalars: cutover is the FIRST " +
			"grant's created_at, not the rotation's (§11.13), and current_epoch is 2 — the HIGHEST held " +
			"(§11.14), which is the epoch a new write must seal under, so a client that reported 1 here " +
			"would publish cards under a superseded key.",
		Options: b.readerKeyring(secMaint),
		Events:  []*nostr.Event{g1, cardE1, g2, cardE2},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v52a", "ready-v52b"}, "work": {"ready-v52a", "ready-v52b"},
				"focus": {"ready-v52a", "ready-v52b"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: true, Cutover: t0 - 300, CurrentEpoch: 2},
		},
	})
}

// vKeyringCutoverIgnoresGrantee pins §11.13: the cutover is the earliest
// created_at of ANY owner-signed CEK-bearing grant, "tracked regardless of who it
// is addressed to".
//
// The discriminating case is a member ADMITTED LATER than the board went
// confidential — the ordinary shape of every board that ever adds a second
// member. Derive the cutover from the grants addressed to YOU and it lands at
// your own admission, and every post-cutover cleartext card written before you
// joined renders in clear: a plaintext card on a confidential board is either an
// unsealed leak or a forgery, and §11.3 exists to withhold it.
func (b *builder) vKeyringCutoverIgnoresGrantee() error {
	cek, err := hexKey(cekSharedEpoch)
	if err != nil {
		return err
	}
	// THE EARLIEST CEK GRANT — addressed to the AGENT, not to the reader. This is
	// the instant the board went confidential.
	gAgent, err := b.cekGrant(b.agentPub, cekSharedEpoch, 1, "bootstrap, first member", t0-300)
	if err != nil {
		return err
	}
	// Legitimately grandfathered: plaintext, BEFORE the true cutover (§11.4).
	grandfathered, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v53a", Title: "plaintext before the board went confidential", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0-400)
	if err != nil {
		return err
	}
	// THE DISCRIMINATOR: plaintext, AFTER the true cutover but BEFORE the reader's
	// own grant. Quarantined under §11.13 + §11.3; rendered by a client that reads
	// the cutover off its own grants only.
	afterCutover, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v53b", Title: "cleartext leaked after the cutover", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0-200)
	if err != nil {
		return err
	}
	// The reader is admitted later, to the SAME epoch-1 key.
	gMaint, err := b.cekGrant(b.maintPub, cekSharedEpoch, 1, "second member admitted later", t0-100)
	if err != nil {
		return err
	}
	sealed, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v53c", Title: "sealed and readable", Status: state.StatusActive, Priority: "p1", Type: "task",
		Context: "epoch 1 body", Enc: &rdsync.Envelope{CEK: cek, Epoch: 1},
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v53a", MsgID: grandfathered.ID, Title: "plaintext before the board went confidential",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0 - 400), UpdatedAt: nanos(t0 - 400),
		},
		&state.Item{
			ID: "ready-v53c", MsgID: sealed.ID, Title: "sealed and readable",
			Context: "epoch 1 body", Description: "epoch 1 body",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "keyring_cutover_is_the_earliest_owner_grant_whoever_it_names",
		SpecClauses: []string{"11.13", "11.3", "11.4", "11.12", "11.14"},
		Note: "The board went confidential at t0-300 with a grant addressed to the AGENT; the reader " +
			"(the maintainer) is admitted only at t0-100 to that same epoch-1 key. §11.13 derives the " +
			"cutover from the EARLIEST owner CEK-bearing grant regardless of grantee, so it is t0-300 — " +
			"asserted directly in expect.keyring and enforced on the items: ready-v53b is plaintext at " +
			"t0-200, i.e. AFTER the true cutover and BEFORE the reader's own grant, and is quarantined " +
			"(§11.3), producing NO item at all. A client that derived the cutover from the grants " +
			"addressed to ITSELF gets t0-100 and renders ready-v53b in clear — on a confidential board " +
			"a post-cutover plaintext card is an unsealed leak or a forgery, which is exactly what the " +
			"gate exists to withhold. The two controls bracket it: ready-v53a (plaintext at t0-400, " +
			"genuinely pre-cutover) is grandfathered and folds (§11.4), and ready-v53c decrypts, so the " +
			"absence of ready-v53b is the cutover firing and not a missing key.",
		Options: b.readerKeyring(secMaint),
		Events:  []*nostr.Event{gAgent, grandfathered, afterCutover, gMaint, sealed},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v53a", "ready-v53c"}, "work": {"ready-v53a", "ready-v53c"},
				"focus": {"ready-v53a", "ready-v53c"},
			}),
			Keyring: &KeyringFacts{BoardCoord: b.boardCoord, Confidential: true, Cutover: t0 - 300, CurrentEpoch: 1},
		},
	})
}
