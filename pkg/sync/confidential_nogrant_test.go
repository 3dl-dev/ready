package sync

// Security-verification test (ready-592). DECISIVE PROOF that confidential READ is
// gated by the owner-grant-borne CEK — not merely by the write-side grant check.
//
// During the dogfood the surface key (780bf45d) reported reading confidential item
// BODIES on a board where it had no write grant. Either (a) it held a CACHED read
// key from prior setup (benign) or (b) confidential read is not gated (CRITICAL).
// This test constructs a genuinely-confidential board (CEK-sealed card + status,
// owner self-grant setting the cutover) and a FRESH key with NO grant and NO cached
// read key, then asserts the fresh key CANNOT recover plaintext at any layer:
// derivation, raw AEAD, on-wire content, and the end-to-end projection. It gets
// ciphertext / a decrypt failure / the [encrypted] placeholder — never plaintext.
//
// If this test passes, the dogfood observation was a cached key (benign) and this is
// the durable proof. If a fresh key ever DID recover plaintext, this test fails —
// exposing the CRITICAL confidentiality break.

import (
	"strings"
	"testing"
)

func TestConfidentialReadRequiresGrant(t *testing.T) {
	owner := kdKey(t)
	fresh := kdKey(t) // a stranger key: no grant p-tagged to it, no wrap it can open
	const boardD = "ready"
	boardCoord := BoardCoord(owner.PubKeyHex(), boardD)

	cek1, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey cek: %v", err)
	}
	ltk, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey ltk: %v", err)
	}
	env := &Envelope{CEK: cek1, Epoch: 1, LTK: &ltk}

	const title = "acquire victimproj rootkey"
	const desc = "exfil plan body that must never render for a non-granted key"
	const reason = "closed after exfil rehearsal"
	// buildConfidentialItem returns [board, card, status], all sealed under env.
	events := buildConfidentialItem(t, owner, "ready-592a", title, desc, "ready-blk", reason, env)
	card := events[1]
	status := events[2]

	// Owner self-grant carrying the epoch-1 CEK+LTK wrapped to the OWNER ONLY. This is
	// what makes the board genuinely confidential (it sets the board cutover in the
	// log). Crucially it is NOT addressed to, and cannot be opened by, the fresh key —
	// so the board is confidential AND the fresh key legitimately holds no key.
	selfGrant := kdGrant(t, owner, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: owner.PubKeyHex(),
		Role: RoleOwner, WrappedCEK: kdWrap(t, owner, owner.PubKeyHex(), cek1), CEKEpoch: 1,
		WrappedLTK: kdWrap(t, owner, owner.PubKeyHex(), ltk),
	}, 1_699_500_000)
	events = append(events, selfGrant)

	// Layer 1 — DERIVATION. Scanning the WHOLE log, the fresh key recovers NOTHING.
	// The board IS confidential (cutover set by the owner self-grant), so the read
	// gate is genuinely exercised; the fresh key simply holds no CEK for the epoch.
	kr := DeriveBoardKeyring(events, fresh, owner.PubKeyHex(), boardD)
	if cek, ok := kr.CEK(boardCoord, 1); ok {
		t.Fatalf("CRITICAL: fresh un-granted key recovered a CEK (%x) — confidential read is NOT grant-gated", cek)
	}
	if _, ok := kr.LTK(boardCoord); ok {
		t.Fatal("CRITICAL: fresh un-granted key recovered the LTK")
	}
	if _, ok := kr.Cutover(boardCoord); !ok {
		t.Fatal("test setup invalid: board not seen as confidential (no cutover) — the read gate is not being exercised")
	}

	// Layer 2 — RAW AEAD. Decrypting the sealed card/status with the fresh key's
	// (empty) keyring fails closed: ok=false, no plaintext escapes.
	if pl, ok := decryptCardPayload(card, kr); ok {
		t.Fatalf("CRITICAL: fresh un-granted key decrypted the card body: title=%q", pl.Title)
	}
	if r, ok := decryptStatusReason(status, kr); ok {
		t.Fatalf("CRITICAL: fresh un-granted key decrypted the status reason: %q", r)
	}

	// Layer 3 — ON-WIRE. The sealed Content is genuine ciphertext; the plaintext body
	// appears nowhere in the event a relay (or the fresh key) actually sees.
	if strings.Contains(card.Content, title) || strings.Contains(card.Content, desc) {
		t.Fatal("CRITICAL: card Content leaks plaintext — the body was not sealed")
	}
	if strings.Contains(status.Content, reason) {
		t.Fatal("CRITICAL: status Content leaks the plaintext reason")
	}

	// Layer 4 — END-TO-END PROJECTION. Rendering the log AS the fresh key (its own
	// keyring wired into both the decryptor and the fail-closed fold gate) yields the
	// fixed [encrypted] placeholder for every free-text field, never the real body,
	// while clear routing fields still render.
	items := ProjectItems(events, ProjectOptions{
		Trusted:         map[string]bool{owner.PubKeyHex(): true},
		PinnedBoard:     boardCoord,
		Decryptor:       kr,
		EncryptedBoards: kr,
	})
	it, ok := items["ready-592a"]
	if !ok {
		t.Fatal("item missing from the fresh key's projection")
	}
	if it.Title == title || it.Description == desc {
		t.Fatalf("CRITICAL: fresh key rendered the exact secret plaintext: title=%q desc=%q", it.Title, it.Description)
	}
	if it.Title != placeholderText || it.Description != placeholderText {
		t.Fatalf("fresh key free text not placeholdered: title=%q desc=%q", it.Title, it.Description)
	}
	// The close reason must not leak into history either.
	if len(it.History) == 0 || it.History[len(it.History)-1].Note != placeholderText {
		t.Fatalf("CRITICAL: fresh key close reason not placeholdered: history=%+v", it.History)
	}
	for _, h := range it.History {
		if h.Note == reason {
			t.Fatalf("CRITICAL: fresh key read the plaintext close reason via history: %q", h.Note)
		}
	}

	// POSITIVE CONTROL. Over the IDENTICAL event log and the IDENTICAL read path, the
	// OWNER (who holds the grant-borne CEK via its self-grant) DOES recover the exact
	// plaintext. Only the key differs — proving the grant, not a broken setup, is what
	// gates the read: granted → plaintext, non-granted → placeholder.
	ownerKR := DeriveBoardKeyring(events, owner, owner.PubKeyHex(), boardD)
	if _, ok := ownerKR.CEK(boardCoord, 1); !ok {
		t.Fatal("positive control broken: owner did not recover its own epoch-1 CEK from the self-grant")
	}
	ownerItems := ProjectItems(events, ProjectOptions{
		Trusted:         map[string]bool{owner.PubKeyHex(): true},
		PinnedBoard:     boardCoord,
		Decryptor:       ownerKR,
		EncryptedBoards: ownerKR,
	})
	oit, ok := ownerItems["ready-592a"]
	if !ok {
		t.Fatal("positive control broken: item missing from the owner's projection")
	}
	if oit.Title != title || oit.Description != desc {
		t.Fatalf("positive control broken: owner did not render plaintext: title=%q desc=%q", oit.Title, oit.Description)
	}
}
