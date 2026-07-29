package foldvectors

// cases_security.go — the fail-closed half of the suite: malformed input,
// forgery, read-trust, board pinning, prospective revocation, the confidential
// envelope, and the view lattice.
//
// Several of these come in PAIRS: the same events folded under different
// options, one where the gate fires and one where it does not. A rejection test
// that never sees the thing it rejects actually happen proves nothing.

import (
	"errors"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// vMalformedDropped pins §3.1 (nil event), §3.7 (no resolvable item id) and
// §2.6 (a kind the fold does not know).
func (b *builder) vMalformedDropped() error {
	valid, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23", Title: "Survivor", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// A 30302 card with no "d" tag: itemIDForEvent yields "" -> dropped.
	noD := &nostr.Event{
		Kind:      rdsync.KindCard,
		CreatedAt: t0 + 100,
		Tags:      [][]string{{"title", "card without a d tag"}, {"a", b.boardCoord}, {"s", state.StatusDone}},
		Content:   "should never fold",
	}
	if err := noD.Sign(b.owner); err != nil {
		return err
	}
	// A kind the fold does not participate in, even though it names the item.
	foreign := &nostr.Event{
		Kind:      1,
		CreatedAt: t0 + 200,
		Tags:      [][]string{{"d", "ready-v23"}, {"a", b.boardCoord}, {"s", state.StatusDone}},
		Content:   "a plain nostr note mentioning the item",
	}
	if err := foreign.Sign(b.owner); err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v23", MsgID: valid.ID, Title: "Survivor", Type: "task", Priority: "p1",
		Status: state.StatusInbox, CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "malformed_events_dropped",
		SpecClauses: []string{"3.1", "3.7", "2.6"},
		Note: "Three non-events in one log: a JSON null (the nil-event guard), a well-signed 30302 with " +
			"no `d` tag, and a well-signed kind-1 note carrying the item id. All three are newer than the " +
			"valid card and all three claim status=done; none of them touches the projection.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{nil, noD, foreign, valid},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v23"}, "focus": {"ready-v23"}}),
		},
	})
}

// vForgedSignatureDropped pins §3.3 in both flavours: a tampered signed field
// (id mismatch) and a corrupted signature (schnorr verify failure).
func (b *builder) vForgedSignatureDropped() error {
	genuine, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v24", Title: "genuine", Status: state.StatusInbox, Priority: "p1", Type: "task",
		Context: "the real card",
	}, t0)
	if err != nil {
		return err
	}
	tampered, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v24", Title: "content tampered", Status: state.StatusDone, Priority: "p3", Type: "bug",
		Context: "original",
	}, t0+100)
	if err != nil {
		return err
	}
	// Mutate a SIGNED field after signing: id no longer matches the canonical form.
	tampered.Content = "rewritten after signing"
	sigBroken, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v24", Title: "signature corrupted", Status: state.StatusCancelled, Priority: "p3", Type: "bug",
	}, t0+200)
	if err != nil {
		return err
	}
	sigBroken.Sig = flipHex(sigBroken.Sig)
	if err := verifyRejects(tampered, sigBroken); err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v24", MsgID: genuine.ID, Title: "genuine",
		Context: "the real card", Description: "the real card",
		Type: "task", Priority: "p1", Status: state.StatusInbox,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "forged_events_dropped",
		SpecClauses: []string{"3.3", "4.1"},
		Note: "Two forgeries, both NEWER than the genuine card, so latest-wins would hand them the item " +
			"if the signature gate did not fire first. ready-v24's content was rewritten after signing " +
			"(stored id no longer matches the canonical serialization); the third card's signature bytes " +
			"were corrupted. An independent client can confirm both fail verification before folding.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{genuine, tampered, sigBroken},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v24"}, "focus": {"ready-v24"}}),
		},
	})
}

// takeoverEvents is the shared fixture for the read-trust pair: an outsider
// publishes a newer card for someone else's item plus a status event closing it.
func (b *builder) takeoverEvents() (genuine, hostileCard, hostileStatus *nostr.Event, err error) {
	genuine, err = b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v25", Title: "genuine", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return nil, nil, nil, err
	}
	hostileCard, err = b.card(b.outsider, rdsync.CardSpec{
		ItemID: "ready-v25", Title: "hostile takeover", Status: state.StatusActive, Priority: "p3", Type: "bug",
	}, t0+100)
	if err != nil {
		return nil, nil, nil, err
	}
	hostileStatus, err = b.status(b.outsider, "ready-v25", state.StatusDone, "closing your item", t0+200, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	return genuine, hostileCard, hostileStatus, nil
}

// vUntrustedAuthorDropped pins §3.4: schnorr validity is not authority. The
// outsider's events verify perfectly and are still dropped.
func (b *builder) vUntrustedAuthorDropped() error {
	genuine, hostileCard, hostileStatus, err := b.takeoverEvents()
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v25", MsgID: genuine.ID, Title: "genuine", Type: "task", Priority: "p1",
		Status: state.StatusInbox, CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "untrusted_author_dropped",
		SpecClauses: []string{"3.3", "3.4", "6.4"},
		Note: "The outsider's card is newer and its events VERIFY — signature validity proves consistency, " +
			"not authority. With an enforced trust set it never reaches the winning-card contest, so it " +
			"cannot become the item author and its status event cannot close the item. Paired with " +
			"trust_gate_disabled_admits_anyone, which folds these exact events with the gate off.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{genuine, hostileCard, hostileStatus},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v25"}, "focus": {"ready-v25"}}),
		},
	})
}

// vTrustGateDisabledAdmitsAnyone is the counter-proof for the vector above:
// `trusted: null` disables the allowlist (§3.4), and the takeover succeeds. If
// this vector ever produced the SAME result as the enforced one, the rejection
// test above would be vacuous.
func (b *builder) vTrustGateDisabledAdmitsAnyone() error {
	genuine, hostileCard, hostileStatus, err := b.takeoverEvents()
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v25", MsgID: hostileCard.ID, Title: "hostile takeover", Type: "bug", Priority: "p3",
		Status: state.StatusDone, CreatedAt: nanos(t0 + 100), UpdatedAt: nanos(t0 + 200),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 200), FromStatus: "", ToStatus: state.StatusDone, ChangedBy: b.outsiderPub, Note: "closing your item"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "trust_gate_disabled_admits_anyone",
		SpecClauses: []string{"3.4", "4.1", "6.4"},
		Note: "IDENTICAL events to untrusted_author_dropped, with `trusted: null` (gate disabled — the " +
			"pre-ready-d53 behaviour retained for unconfigured/legacy callers). The outsider's newer card " +
			"now wins, which makes the outsider the item AUTHOR, which makes the outsider's own status " +
			"event authoritative: full state takeover. This is what the enforced gate prevents. " +
			"Production never passes null (cmd/rd/nostr.go always supplies a non-nil trust set).",
		Options: Options{Trusted: nil},
		Events:  []*nostr.Event{genuine, hostileCard, hostileStatus},
		Expect:  Expect{Items: items, Views: vw(nil)},
	})
}

// vBoardPinRejectsForeignBoard pins §3.8: a card whose `a` coordinate is not the
// pinned board is rejected even when its author is read-trusted.
func (b *builder) vBoardPinRejectsForeignBoard() error {
	genuine, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v26", Title: "on the pinned board", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// Same item id, newer, signed by a TRUSTED key — but bound to the signer's own
	// parallel board coordinate.
	parallel, err := rdsync.BuildCardEvent(b.agent, rdsync.CardSpec{
		ItemID: "ready-v26", Title: "parallel board escalation", Status: state.StatusDone, Priority: "p3", Type: "bug",
		BoardD: boardD, BoardAuthor: b.agentPub,
	}, t0+100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v26", MsgID: genuine.ID, Title: "on the pinned board", Type: "task", Priority: "p1",
		Status: state.StatusInbox, CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "board_pin_rejects_foreign_board_card",
		SpecClauses: []string{"3.8", "4.1", "12.4"},
		Note: "With a pinned board, a newer card carrying a DIFFERENT `a` coordinate is rejected — the " +
			"agent key is read-trusted, so only the pin stops it forking its own 30301, self-granting " +
			"maintainer on it and publishing cards for another owner's items.",
		Options: Options{Trusted: trust(b.ownerPub, b.agentPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{genuine, parallel},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v26"}, "focus": {"ready-v26"}}),
		},
	})
}

// vGrantRevocationPointInTime pins §3.5, §12.7 and §12.8 together: a grant
// admits a key to read-trust that the config never listed, and a later revoke
// drops only its FUTURE events.
func (b *builder) vGrantRevocationPointInTime() error {
	grant, err := rdsync.BuildRoleGrantEvent(b.owner, rdsync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: b.ownerPub, Grantee: b.agentPub, Role: rdsync.RoleContributor,
		Label: "admit the agent machine",
	}, t0-100)
	if err != nil {
		return err
	}
	before, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v27", Title: "authored before the revoke", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	claimed, err := b.status(b.agent, "ready-v27", state.StatusActive, "claimed before the revoke", t0+50, nil)
	if err != nil {
		return err
	}
	revoke, err := rdsync.BuildRoleGrantEvent(b.owner, rdsync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: b.ownerPub, Grantee: b.agentPub, Role: rdsync.RoleRevoked,
		Label: "machine decommissioned",
	}, t0+100)
	if err != nil {
		return err
	}
	after, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v27", Title: "post-revoke edit", Status: state.StatusDone, Priority: "p3", Type: "bug",
	}, t0+200)
	if err != nil {
		return err
	}
	closeAfter, err := b.status(b.agent, "ready-v27", state.StatusDone, "closing after revocation", t0+300, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v27", MsgID: before.ID, Title: "authored before the revoke", Type: "task", Priority: "p1",
		Status: state.StatusActive, CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 50),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 50), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.agentPub, Note: "claimed before the revoke"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "grant_admits_and_revoke_is_prospective",
		SpecClauses: []string{"3.4", "3.5", "12.1", "12.4", "12.7", "12.8", "2.5"},
		Note: "The agent key is NOT in the trust list; it is admitted solely by the owner-signed 39301 " +
			"grant, which is why its pre-revoke card and status event fold. The revoke then bounds its " +
			"authoritative-until to the revoke's created_at: the later card and the later close are " +
			"dropped, while the earlier ones REMAIN authoritative — a completed item must not reopen " +
			"because its past author was later revoked. The 39301 events themselves produce no item.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{grant, before, claimed, revoke, after, closeAfter},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v27"}, "work": {"ready-v27"}, "focus": {"ready-v27"}}),
		},
	})
}

// vClaimSingleUseAcrossRevoke pins §12.6/§12.10 (ready-55f, security sweep
// ready-348): a claim-bearing grant binds its claim-nonce to exactly ONE
// grantee, and that binding is NOT cleared by a later revoke for the SAME
// grantee — a later owner grant reusing the identical claim-nonce for a
// DIFFERENT grantee must still be ignored. Before ready-55f, a claim-bearing
// grant and a same-grantee revoke shared one addressable "d" slot
// (`roleGrantD`); a relay retains only the newest event per (kind, pubkey, d),
// so on any machine that only ever reconciles from a relay (never authors the
// claim grant itself) the revoke's arrival deleted the relay's copy of the
// original claim binding, and a second grantee reusing the same claim-nonce was
// then wrongly admitted. This vector cannot reproduce the relay-loss mechanism
// itself (a conformance vector folds one fixed, ALREADY-COMPLETE event set, not
// a relay's retained subset — see pkg/sync/rolegrant_test.go's
// TestClaimBinding_SurvivesRelayLastWriteWins for that), but it DOES pin the
// spec-level, machine-independent invariant §12.6 states: the claim binding, once
// made, survives a same-grantee revoke and rejects reuse for a different
// grantee. An independent client's own §12.6 implementation must agree.
func (b *builder) vClaimSingleUseAcrossRevoke() error {
	const inviteClaim = "ready-55f-single-use-claim"
	grantAgent, err := rdsync.BuildRoleGrantEvent(b.owner, rdsync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: b.ownerPub, Grantee: b.agentPub, Role: rdsync.RoleContributor,
		Label: "first joiner admitted by claim", Claim: inviteClaim,
	}, t0-100)
	if err != nil {
		return err
	}
	cardAgent, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v29", Title: "authored by the claim-admitted agent", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	revokeAgent, err := rdsync.BuildRoleGrantEvent(b.owner, rdsync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: b.ownerPub, Grantee: b.agentPub, Role: rdsync.RoleRevoked,
		Label: "agent machine decommissioned",
	}, t0+100)
	if err != nil {
		return err
	}
	// A later owner grant reuses the SAME claim-nonce for a DIFFERENT grantee
	// (the outsider key). §12.6 requires this be ignored outright — the claim was
	// already consumed by the agent, and that binding does not clear just because
	// the agent was later revoked (the revoke carries no claim tag of its own).
	grantOutsiderReusingClaim, err := rdsync.BuildRoleGrantEvent(b.owner, rdsync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: b.ownerPub, Grantee: b.outsiderPub, Role: rdsync.RoleContributor,
		Label: "second joiner reusing a consumed claim", Claim: inviteClaim,
	}, t0+200)
	if err != nil {
		return err
	}
	cardOutsider, err := b.card(b.outsider, rdsync.CardSpec{
		ItemID: "ready-v30", Title: "authored by the never-validly-admitted outsider", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0+300)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v29", MsgID: cardAgent.ID, Title: "authored by the claim-admitted agent", Type: "task", Priority: "p1",
		Status: state.StatusInbox, CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "claim_single_use_survives_revoke_rejects_reuse",
		SpecClauses: []string{"12.6", "12.10", "3.4"},
		Note: "The outsider's grant reuses the SAME claim-nonce the agent already consumed and MUST be " +
			"ignored regardless of the intervening revoke, so the outsider is never admitted to read-trust " +
			"and ready-v30 (authored solely by the outsider, an untrusted key) produces NO item at all — " +
			"not merely a dropped field, an absent item. ready-v29 (authored by the validly claim-admitted " +
			"agent, before its own later revoke) DOES fold, proving the claim binding admitted the agent in " +
			"the first place and the outsider's rejection is about claim single-use, not board pinning or " +
			"config trust.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{grantAgent, cardAgent, revokeAgent, grantOutsiderReusingClaim, cardOutsider},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v29"}, "focus": {"ready-v29"}}),
		},
	})
}

// confidentialEvents builds the shared confidential fixture: one sealed card
// (with tokenized labels) and one sealed status event.
func (b *builder) confidentialEvents() (card, status *nostr.Event, err error) {
	cek, err := hexKey(cekGoodHex)
	if err != nil {
		return nil, nil, err
	}
	ltk, err := hexKey(ltkHex)
	if err != nil {
		return nil, nil, err
	}
	env := &rdsync.Envelope{CEK: cek, Epoch: 1, LTK: &ltk}
	card, err = b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v28", Title: "sealed title", Status: state.StatusActive, Priority: "p0", Type: "decision",
		Context: "sealed context", Gate: "design", WaitingType: "gate", WaitingOn: "the approver",
		Labels: []string{"security"}, Enc: env,
	}, t0)
	if err != nil {
		return nil, nil, err
	}
	status, err = b.status(b.owner, "ready-v28", state.StatusActive, "sealed reason", t0+100, env)
	if err != nil {
		return nil, nil, err
	}
	return card, status, nil
}

// confidentialViews is the view membership shared by all three confidential
// vectors: the item is gate-promoted to waiting either way, because every field
// the gate reads is a CLEAR routing tag.
func confidentialViews() map[string][]string {
	return vw(map[string][]string{
		"ready": {"ready-v28"}, "focus": {"ready-v28"},
		"pending": {"ready-v28"}, "gates": {"ready-v28"},
	})
}

// vConfidentialGrantedReader pins §11.7 / §11.8 / §10.3 on the success path.
func (b *builder) vConfidentialGrantedReader() error {
	card, status, err := b.confidentialEvents()
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v28", MsgID: card.ID, Title: "sealed title",
		Context: "sealed context", Description: "sealed context",
		Type: "decision", Priority: "p0", Status: state.StatusWaiting,
		Gate: "design", WaitingOn: "the approver", WaitingType: "gate",
		WaitingSince: rfc(t0 + 100), GateMsgID: card.ID,
		Labels: []string{"security"}, CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "sealed reason"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "confidential_granted_reader_decrypts",
		SpecClauses: []string{"11.1", "11.2", "11.6", "11.7", "11.8", "11.9", "10.3", "9.4"},
		Note: "A granted member holds the CEK for (board coordinate, epoch 1), so title, context/" +
			"description, waiting_on and the plaintext labels all come out of the sealed blob, and the " +
			"status event's sealed reason renders as the history note. The clear `l` tag on the wire is " +
			"the HMAC label token, not the label. Compare with the two placeholder vectors, which fold " +
			"the SAME events.",
		Options: Options{
			Trusted:         trust(b.ownerPub),
			Decryptor:       &DecryptorSpec{Keys: []CEKEntry{{BoardCoord: b.boardCoord, Epoch: 1, CEKHex: cekGoodHex}}},
			EncryptedBoards: &EncryptedBoardsSpec{Boards: []BoardCutover{{BoardCoord: b.boardCoord, Cutover: t0 - 200}}},
		},
		Events: []*nostr.Event{card, status},
		Expect: Expect{Items: items, Views: confidentialViews()},
	})
}

// placeholderItem is the expected projection of the confidential fixture for a
// reader that CANNOT open the envelope (§11.7, §11.8).
func (b *builder) placeholderItem(card *nostr.Event) (*state.Item, error) {
	ltk, err := hexKey(ltkHex)
	if err != nil {
		return nil, err
	}
	return &state.Item{
		ID: "ready-v28", MsgID: card.ID,
		Title: "[encrypted]", Context: "[encrypted]", Description: "[encrypted]",
		Type: "decision", Priority: "p0", Status: state.StatusWaiting,
		Gate: "design", WaitingType: "gate", WaitingSince: rfc(t0 + 100), GateMsgID: card.ID,
		Labels: []string{labelTokenHex(ltk, "security")}, CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "[encrypted]"},
		},
	}, nil
}

// vConfidentialWrongCEK is the required negative: a reader holding the WRONG key
// for the right (coordinate, epoch) slot. The AEAD fails and the free text
// fail-closes to the placeholder — never to an empty title, never to ciphertext.
func (b *builder) vConfidentialWrongCEK() error {
	card, status, err := b.confidentialEvents()
	if err != nil {
		return err
	}
	want, err := b.placeholderItem(card)
	if err != nil {
		return err
	}
	if want.Title == "" {
		return errors.New("placeholder expectation must not be an empty title")
	}
	items, err := itemsJSON(want)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "confidential_wrong_cek_placeholder",
		SpecClauses: []string{"11.6", "11.7", "11.8", "11.9", "10.3"},
		Note: "The reader holds a 32-byte key for exactly the right (board coordinate, epoch) slot — the " +
			"WRONG key. cekFor succeeds, the ChaCha20-Poly1305 Open fails, and every sealed field " +
			"fail-closes: title/context/description become \"[encrypted]\" (never \"\", never raw " +
			"ciphertext), waiting_on is HIDDEN rather than placeheld, the history note becomes the " +
			"placeholder, and the labels stay opaque HMAC tokens. Every CLEAR routing field — status, " +
			"priority, type, gate, waiting_type — renders normally, which is why the item still " +
			"gate-promotes and still appears in the gates view.",
		Options: Options{
			Trusted:         trust(b.ownerPub),
			Decryptor:       &DecryptorSpec{Keys: []CEKEntry{{BoardCoord: b.boardCoord, Epoch: 1, CEKHex: cekWrongHex}}},
			EncryptedBoards: &EncryptedBoardsSpec{Boards: []BoardCutover{{BoardCoord: b.boardCoord, Cutover: t0 - 200}}},
		},
		Events: []*nostr.Event{card, status},
		Expect: Expect{Items: items, Views: confidentialViews()},
	})
}

// vConfidentialNoDecryptor is the ordinary non-member read: no keyring at all.
func (b *builder) vConfidentialNoDecryptor() error {
	card, status, err := b.confidentialEvents()
	if err != nil {
		return err
	}
	want, err := b.placeholderItem(card)
	if err != nil {
		return err
	}
	items, err := itemsJSON(want)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "confidential_no_decryptor_placeholder",
		SpecClauses: []string{"11.6", "11.7", "11.8", "11.15"},
		Note: "A read-trusted but NOT key-granted reader (`decryptor: null`) sees exactly what the " +
			"wrong-key reader sees. A missing keyring is a silent fail-closed, never an error and never " +
			"a panic.",
		Options: Options{
			Trusted:         trust(b.ownerPub),
			EncryptedBoards: &EncryptedBoardsSpec{Boards: []BoardCutover{{BoardCoord: b.boardCoord, Cutover: t0 - 200}}},
		},
		Events: []*nostr.Event{card, status},
		Expect: Expect{Items: items, Views: confidentialViews()},
	})
}

// vFoldGateQuarantine pins §3.9 / §11.3 / §11.4: on a confidential board a
// plaintext or malformed event is QUARANTINED, except a genuine pre-cutover
// plaintext card.
func (b *builder) vFoldGateQuarantine() error {
	grandfathered, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v29a", Title: "grandfathered plaintext", Status: state.StatusActive, Priority: "p1", Type: "task",
		Context: "written before the board went confidential",
	}, t0-100)
	if err != nil {
		return err
	}
	lateReason, err := b.status(b.owner, "ready-v29a", state.StatusDone, "cleartext close reason", t0+100, nil)
	if err != nil {
		return err
	}
	lateCard, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v29b", Title: "post-cutover cleartext", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0+100)
	if err != nil {
		return err
	}
	cek, err := hexKey(cekGoodHex)
	if err != nil {
		return err
	}
	malformed, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v29c", Title: "smuggled", Status: state.StatusActive, Priority: "p1", Type: "task",
		Enc: &rdsync.Envelope{CEK: cek, Epoch: 1},
	}, t0-500)
	if err != nil {
		return err
	}
	// enc-shaped but structurally invalid: Content is not base64. Re-signed so it
	// fails the FOLD GATE (§3.9) and not the signature gate (§3.3).
	malformed.Content = "!!! not base64 !!!"
	if err := malformed.Sign(b.owner); err != nil {
		return err
	}
	if err := malformed.Verify(); err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v29a", MsgID: grandfathered.ID, Title: "grandfathered plaintext",
		Context: "written before the board went confidential", Description: "written before the board went confidential",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0 - 100), UpdatedAt: nanos(t0 - 100),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "fold_gate_quarantines_plaintext_and_malformed",
		SpecClauses: []string{"3.9", "11.2", "11.3", "11.4", "11.5", "3.11"},
		Note: "The board is confidential with cutover " + rfc(t0) + ". ready-v29a's card predates the " +
			"cutover and is plaintext, so it is grandfathered and folds — but its POST-cutover plaintext " +
			"status event is quarantined, so its cleartext close reason never reaches history and the " +
			"item does not close. ready-v29b's post-cutover plaintext card is quarantined outright, so " +
			"the item does not exist at all. ready-v29c is enc-shaped but its Content is not base64: " +
			"grandfathering covers only GENUINE plaintext, never a malformed envelope, so it is " +
			"quarantined despite predating the cutover. All three quarantined events verify — this is " +
			"the fold gate firing, not the signature gate.",
		Options: Options{
			Trusted:         trust(b.ownerPub),
			EncryptedBoards: &EncryptedBoardsSpec{Boards: []BoardCutover{{BoardCoord: b.boardCoord, Cutover: t0}}},
		},
		Events: []*nostr.Event{grandfathered, lateReason, lateCard, malformed},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v29a"}, "work": {"ready-v29a"}, "focus": {"ready-v29a"}}),
		},
	})
}

// vViewsLattice pins §13.3–§13.12: one event set spread across the lattice so
// every view predicate has both a member and a non-member.
func (b *builder) vViewsLattice() error {
	type spec struct {
		id   string
		card rdsync.CardSpec
	}
	specs := []spec{
		{"ready-v30a", rdsync.CardSpec{Title: "Mine, scoped to the agent", Status: state.StatusActive, Priority: "p1", Type: "task",
			Assignee: b.ownerPub, For: b.agentPub, Labels: []string{"security"}}},
		{"ready-v30b", rdsync.CardSpec{Title: "Delegated to the agent", Status: state.StatusActive, Priority: "p1", Type: "task",
			Assignee: b.agentPub, For: b.ownerPub}},
		{"ready-v30c", rdsync.CardSpec{Title: "Mine, scoped to me", Status: state.StatusActive, Priority: "p2", Type: "task",
			Assignee: b.ownerPub, For: b.ownerPub}},
		{"ready-v30d", rdsync.CardSpec{Title: "Overdue", Status: state.StatusInbox, Priority: "p1", Type: "task",
			ETA: "2020-01-01T00:00:00Z"}},
		{"ready-v30e", rdsync.CardSpec{Title: "Overdue but finished", Status: state.StatusDone, Priority: "p1", Type: "task",
			Assignee: b.ownerPub, ETA: "2020-01-01T00:00:00Z"}},
		{"ready-v30f", rdsync.CardSpec{Title: "Gated", Status: state.StatusActive, Priority: "p0", Type: "decision",
			Gate: "review", WaitingType: "gate", WaitingOn: "a reviewer"}},
		{"ready-v30g", rdsync.CardSpec{Title: "Blocked", Status: state.StatusActive, Priority: "p2", Type: "task",
			Deps: []string{"ready-v30a"}}},
		{"ready-v30h", rdsync.CardSpec{Title: "Cancelled", Status: state.StatusCancelled, Priority: "p3", Type: "task",
			ETA: "2020-01-01T00:00:00Z"}},
	}
	var events []*nostr.Event
	ids := map[string]string{}
	for _, s := range specs {
		cs := s.card
		cs.ItemID = s.id
		e, err := b.card(b.owner, cs, t0)
		if err != nil {
			return err
		}
		events = append(events, e)
		ids[s.id] = e.ID
	}
	items, err := itemsJSON(
		&state.Item{ID: "ready-v30a", MsgID: ids["ready-v30a"], Title: "Mine, scoped to the agent",
			Type: "task", For: b.agentPub, By: b.ownerPub, Priority: "p1", Status: state.StatusActive,
			Blocks: []string{"ready-v30g"}, Labels: []string{"security"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0)},
		&state.Item{ID: "ready-v30b", MsgID: ids["ready-v30b"], Title: "Delegated to the agent",
			Type: "task", For: b.ownerPub, By: b.agentPub, Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0)},
		&state.Item{ID: "ready-v30c", MsgID: ids["ready-v30c"], Title: "Mine, scoped to me",
			Type: "task", For: b.ownerPub, By: b.ownerPub, Priority: "p2", Status: state.StatusActive,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0)},
		&state.Item{ID: "ready-v30d", MsgID: ids["ready-v30d"], Title: "Overdue",
			Type: "task", Priority: "p1", Status: state.StatusInbox, ETA: "2020-01-01T00:00:00Z",
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0)},
		&state.Item{ID: "ready-v30e", MsgID: ids["ready-v30e"], Title: "Overdue but finished",
			Type: "task", By: b.ownerPub, Priority: "p1", Status: state.StatusDone, ETA: "2020-01-01T00:00:00Z",
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0)},
		&state.Item{ID: "ready-v30f", MsgID: ids["ready-v30f"], Title: "Gated",
			Type: "decision", Priority: "p0", Status: state.StatusWaiting,
			Gate: "review", WaitingType: "gate", WaitingOn: "a reviewer",
			WaitingSince: rfc(t0), GateMsgID: ids["ready-v30f"],
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0)},
		&state.Item{ID: "ready-v30g", MsgID: ids["ready-v30g"], Title: "Blocked",
			Type: "task", Priority: "p2", Status: state.StatusBlocked, BlockedBy: []string{"ready-v30a"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0)},
		&state.Item{ID: "ready-v30h", MsgID: ids["ready-v30h"], Title: "Cancelled",
			Type: "task", Priority: "p3", Status: state.StatusCancelled, ETA: "2020-01-01T00:00:00Z",
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0)},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "views_lattice",
		SpecClauses: []string{"13.1", "13.2", "13.3", "13.4", "13.5", "13.6", "13.7", "13.8", "13.10", "13.11", "13.12"},
		Note: "Eight items spread across the lattice, evaluated for the owner identity. ready: everything " +
			"non-terminal and non-blocked (the gated item included — waiting is READY). work: status " +
			"exactly `active`. pending: waiting + blocked. overdue: past ETA and non-terminal, so the " +
			"finished and cancelled past-ETA items are excluded. delegated: for=me, by=someone else, " +
			"active. my-work: by=me and non-terminal, so the finished one drops out and the item merely " +
			"SCOPED to me (for=me, by=someone else) is not mine. gates: waiting + waiting_type=gate + " +
			"gate_msg_id. focus with no gate type is exactly ready (that is what Named wires). NOTE: no " +
			"`scheduled` item appears anywhere in this suite — spec §15.1 records that no writer produces " +
			"that status and rules a conformance vector out of order until it is resolved, so the " +
			"scheduled conjunct of ready/pending is deliberately uncovered.",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		Events:   events,
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready":     {"ready-v30a", "ready-v30b", "ready-v30c", "ready-v30d", "ready-v30f"},
				"focus":     {"ready-v30a", "ready-v30b", "ready-v30c", "ready-v30d", "ready-v30f"},
				"work":      {"ready-v30a", "ready-v30b", "ready-v30c"},
				"pending":   {"ready-v30f", "ready-v30g"},
				"overdue":   {"ready-v30d"},
				"delegated": {"ready-v30b"},
				"my-work":   {"ready-v30a", "ready-v30c"},
				"gates":     {"ready-v30f"},
			}),
			LabelViews: map[string][]string{"security": {"ready-v30a"}},
		},
	})
}

// flipHex returns s with its first hex digit changed to a different one, so the
// value stays syntactically hex but is no longer the signature that was made.
func flipHex(s string) string {
	if s == "" {
		return s
	}
	repl := byte('a')
	if s[0] == 'a' {
		repl = 'b'
	}
	return string(repl) + s[1:]
}

// verifyRejects asserts each event genuinely fails verification, so a "forged"
// fixture cannot silently become a valid one.
func verifyRejects(events ...*nostr.Event) error {
	for _, e := range events {
		if err := e.Verify(); err == nil {
			return errors.New("forgery fixture unexpectedly verifies")
		}
	}
	return nil
}
