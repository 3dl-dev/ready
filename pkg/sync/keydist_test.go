package sync

// Keydist done-condition tests (ready-a8a): (1) grant→read — a member reconstructs
// the CEK cold from the SIGNED grant alone and decrypts a card; (2) revoke→forward
// secrecy — a revoked member cannot read cards authored after its revocation, a
// remaining member can, and a retargeted wrapped-CEK is unusable by a different
// grantee.

import (
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/3dl-dev/ready/pkg/nip44"
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

func kdKey(t *testing.T) *nostr.Key {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func kdWrap(t *testing.T, owner *nostr.Key, granteePub string, key [32]byte) string {
	t.Helper()
	w, err := WrapKey(owner, granteePub, key)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	return w
}

func kdGrant(t *testing.T, owner *nostr.Key, spec RoleGrantSpec, at int64) *nostr.Event {
	t.Helper()
	e, err := BuildRoleGrantEvent(owner, spec, at)
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent: %v", err)
	}
	return e
}

func kdCard(t *testing.T, owner *nostr.Key, id, title, desc, boardD string, env *Envelope, at int64) *nostr.Event {
	t.Helper()
	e, err := BuildCardEvent(owner, CardSpec{
		ItemID: id, Title: title, Context: desc, Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: boardD, Enc: env,
	}, at)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	return e
}

// TestKeydistGrantThenRead: owner mints a CEK+LTK, grants member M inside a signed
// 39301; M reconstructs both keys cold from the grant and decrypts an epoch-1 card,
// end-to-end through the projection read path.
func TestKeydistGrantThenRead(t *testing.T) {
	owner := kdKey(t)
	m := kdKey(t)
	const boardD = "ready"
	boardCoord := BoardCoord(owner.PubKeyHex(), boardD)

	cek1, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	ltk, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey ltk: %v", err)
	}
	grant := kdGrant(t, owner, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: m.PubKeyHex(),
		Role:       RoleContributor,
		WrappedCEK: kdWrap(t, owner, m.PubKeyHex(), cek1), CEKEpoch: 1,
		WrappedLTK: kdWrap(t, owner, m.PubKeyHex(), ltk),
	}, 1_700_000_000)

	env := &Envelope{CEK: cek1, Epoch: 1, LTK: &ltk}
	card := kdCard(t, owner, "ready-k1", "secret title", "secret desc", boardD, env, 1_700_000_100)

	// M reconstructs cold from the SIGNED grant alone.
	kr := DeriveBoardKeyring([]*nostr.Event{grant}, m, owner.PubKeyHex(), boardD)
	cek, ok := kr.CEK(boardCoord, 1)
	if !ok {
		t.Fatal("M did not recover the epoch-1 CEK from the signed grant")
	}
	if cek != cek1 {
		t.Fatal("recovered CEK != minted CEK")
	}
	if l, ok := kr.LTK(boardCoord); !ok || l != ltk {
		t.Fatal("M did not recover the LTK")
	}
	if cutover, ok := kr.Cutover(boardCoord); !ok || cutover != 1_700_000_000 {
		t.Fatalf("cutover = %d (ok=%v), want the epoch-1 grant time", cutover, ok)
	}

	// End-to-end: project the card as M and confirm plaintext renders.
	board, _ := BuildBoardEvent(owner, BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{owner.PubKeyHex()}}, 1_699_000_000)
	items := ProjectItems([]*nostr.Event{board, card}, ProjectOptions{
		Trusted: map[string]bool{owner.PubKeyHex(): true}, PinnedBoard: boardCoord, Decryptor: kr,
	})
	it, ok := items["ready-k1"]
	if !ok {
		t.Fatal("card missing from M's projection")
	}
	if it.Title != "secret title" || it.Description != "secret desc" {
		t.Fatalf("M read wrong plaintext: title=%q desc=%q", it.Title, it.Description)
	}
}

// TestKeydistRevokeForwardSecrecy: after revoking M and rotating to epoch 2 (wrapped
// to remaining member N only), N reads a post-revoke card but M cannot; and a
// captured epoch-2 wrap retargeted into a grant p-tagged to M stays unusable by M.
func TestKeydistRevokeForwardSecrecy(t *testing.T) {
	owner := kdKey(t)
	m := kdKey(t) // to be revoked
	n := kdKey(t) // remains
	const boardD = "ready"
	boardCoord := BoardCoord(owner.PubKeyHex(), boardD)
	ownerPub := owner.PubKeyHex()

	// Epoch 1: grant both M and N.
	cek1, _ := MintKey()
	gM1 := kdGrant(t, owner, RoleGrantSpec{BoardD: boardD, BoardAuthor: ownerPub, Grantee: m.PubKeyHex(), Role: RoleContributor, WrappedCEK: kdWrap(t, owner, m.PubKeyHex(), cek1), CEKEpoch: 1}, 1_700_000_000)
	gN1 := kdGrant(t, owner, RoleGrantSpec{BoardD: boardD, BoardAuthor: ownerPub, Grantee: n.PubKeyHex(), Role: RoleContributor, WrappedCEK: kdWrap(t, owner, n.PubKeyHex(), cek1), CEKEpoch: 1}, 1_700_000_001)

	// Revoke M, mint epoch 2, re-wrap to N ONLY.
	cek2, _ := MintKey()
	wN2 := kdWrap(t, owner, n.PubKeyHex(), cek2)
	revM := kdGrant(t, owner, RoleGrantSpec{BoardD: boardD, BoardAuthor: ownerPub, Grantee: m.PubKeyHex(), Role: RoleRevoked}, 1_700_000_200)
	gN2 := kdGrant(t, owner, RoleGrantSpec{BoardD: boardD, BoardAuthor: ownerPub, Grantee: n.PubKeyHex(), Role: RoleContributor, WrappedCEK: wN2, CEKEpoch: 2}, 1_700_000_201)

	env2 := &Envelope{CEK: cek2, Epoch: 2}
	card2 := kdCard(t, owner, "ready-k2", "post-revoke secret", "epoch2 desc", boardD, env2, 1_700_000_300)

	all := []*nostr.Event{gM1, gN1, revM, gN2}

	// N (remaining) recovers epoch-2 CEK and decrypts the post-revoke card.
	krN := DeriveBoardKeyring(all, n, ownerPub, boardD)
	if _, ok := krN.CEK(boardCoord, 2); !ok {
		t.Fatal("remaining member N missing epoch-2 CEK")
	}
	if pl, ok := decryptCardPayload(card2, krN); !ok || pl.Title != "post-revoke secret" {
		t.Fatalf("N could not read post-revoke card: ok=%v", ok)
	}

	// M (revoked) keeps epoch-1 (historical reads) but never gets epoch-2.
	krM := DeriveBoardKeyring(all, m, ownerPub, boardD)
	if _, ok := krM.CEK(boardCoord, 1); !ok {
		t.Fatal("revoked M should retain its epoch-1 CEK for historical reads")
	}
	if _, ok := krM.CEK(boardCoord, 2); ok {
		t.Fatal("revoked M obtained the epoch-2 CEK — forward secrecy broken")
	}
	if _, ok := decryptCardPayload(card2, krM); ok {
		t.Fatal("revoked M decrypted a post-revoke card — forward secrecy broken")
	}

	// Retarget attack: move N's epoch-2 wrap into a grant p-tagged to M, re-signed by
	// the owner. The wrap is ECDH-bound to N, so it will not open for M.
	retarget := kdGrant(t, owner, RoleGrantSpec{BoardD: boardD, BoardAuthor: ownerPub, Grantee: m.PubKeyHex(), Role: RoleContributor, WrappedCEK: wN2, CEKEpoch: 2}, 1_700_000_400)
	krMr := DeriveBoardKeyring([]*nostr.Event{gM1, retarget}, m, ownerPub, boardD)
	if _, ok := krMr.CEK(boardCoord, 2); ok {
		t.Fatal("retargeted wrap opened for M — NIP-44 recipient binding broken")
	}
	if _, ok := decryptCardPayload(card2, krMr); ok {
		t.Fatal("M read the epoch-2 card via a retargeted wrap — recipient binding broken")
	}
}

// TestKeydistIgnoresNonOwnerCEK: a CEK "wrapped" in a grant signed by a NON-owner is
// ignored — only the board owner (authz root) mints board keys.
func TestKeydistIgnoresNonOwnerCEK(t *testing.T) {
	owner := kdKey(t)
	attacker := kdKey(t)
	victim := kdKey(t)
	const boardD = "ready"
	boardCoord := BoardCoord(owner.PubKeyHex(), boardD)

	badCEK, _ := MintKey()
	// Attacker signs a grant on the OWNER's board coordinate, wrapping a CEK to victim.
	bad := kdGrant(t, attacker, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: victim.PubKeyHex(),
		Role: RoleContributor, WrappedCEK: kdWrap(t, attacker, victim.PubKeyHex(), badCEK), CEKEpoch: 1,
	}, 1_700_000_000)

	kr := DeriveBoardKeyring([]*nostr.Event{bad}, victim, owner.PubKeyHex(), boardD)
	if _, ok := kr.CEK(boardCoord, 1); ok {
		t.Fatal("a CEK from a non-owner-signed grant was accepted — authz root bypassed")
	}
	if _, ok := kr.Cutover(boardCoord); ok {
		t.Fatal("a non-owner grant set the board cutover — board falsely marked confidential")
	}
}

// TestKeydistWrapPayloadIsHexForBrowserSigners pins the encoding of the NIP-44
// wrap's PLAINTEXT (ready-c4b), which is the property that decides whether a
// browser can ever hold a board key.
//
// A browser unwraps only through NIP-07's
// `window.nostr.nip44.decrypt(pubkey, ciphertext)`, and that call returns a
// STRING — extensions finish with a UTF-8 TextDecoder. A raw 32-byte CEK is
// essentially never valid UTF-8, and a non-fatal decoder replaces each malformed
// run with U+FFFD, destroying the key with no error anywhere. So the wrap
// plaintext MUST be text. This test fails if WrapKey ever goes back to sealing
// raw bytes, which would look completely fine to every Go test in the tree while
// silently making web/board's confidential path impossible.
func TestKeydistWrapPayloadIsHexForBrowserSigners(t *testing.T) {
	owner, member := kdKey(t), kdKey(t)
	cek, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}

	wrapped := kdWrap(t, owner, member.PubKeyHex(), cek)

	// Look at the wrap plaintext directly, the way a NIP-07 signer would hand it
	// to the page.
	pt, err := nip44.Open(member, owner.PubKeyHex(), wrapped)
	if err != nil {
		t.Fatalf("nip44.Open: %v", err)
	}
	if got, want := len(pt), hex.EncodedLen(32); got != want {
		t.Fatalf("wrap plaintext is %d bytes, want %d — WrapKey must seal lowercase hex, not raw key bytes (a NIP-07 signer returns a string and would mangle raw bytes)", got, want)
	}
	if want := hex.EncodeToString(cek[:]); string(pt) != want {
		t.Fatalf("wrap plaintext = %q, want %q", pt, want)
	}
	// The exact property the encoding exists for: a UTF-8 round trip through a
	// JS-style string boundary is lossless.
	if !utf8.Valid(pt) {
		t.Fatalf("wrap plaintext is not valid UTF-8 — a NIP-07 signer's string return would corrupt it")
	}

	// And it still round-trips to the same key.
	got, err := unwrapKey(member, owner.PubKeyHex(), wrapped)
	if err != nil {
		t.Fatalf("unwrapKey: %v", err)
	}
	if got != cek {
		t.Fatalf("unwrapKey returned a different key than WrapKey sealed")
	}
}

// TestKeydistUnwrapAcceptsLegacyRawPayload pins the back-compat half: grants
// minted BEFORE ready-c4b sealed the raw 32 bytes, and they are already on
// relays. Those must keep unwrapping, or enabling confidential mode again would
// be the only way to read an existing board.
func TestKeydistUnwrapAcceptsLegacyRawPayload(t *testing.T) {
	owner, member := kdKey(t), kdKey(t)
	cek, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}

	// The pre-ready-c4b wrap: seal the raw bytes.
	legacy, err := nip44.Seal(owner, member.PubKeyHex(), cek[:])
	if err != nil {
		t.Fatalf("nip44.Seal: %v", err)
	}

	got, err := unwrapKey(member, owner.PubKeyHex(), legacy)
	if err != nil {
		t.Fatalf("unwrapKey(legacy raw payload): %v", err)
	}
	if got != cek {
		t.Fatalf("legacy raw payload unwrapped to the wrong key")
	}
}

// TestKeydistUnwrapRejectsWrongLengthPayload keeps the accept-two-encodings
// branch from becoming accept-anything: a payload that is neither 32 raw bytes
// nor 64 hex characters is an error, and every caller reads an error as "the
// reader holds no key" (placeholder, never a guess).
func TestKeydistUnwrapRejectsWrongLengthPayload(t *testing.T) {
	owner, member := kdKey(t), kdKey(t)
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"too short", []byte("deadbeef")},
		{"hex length but not hex", []byte(strings.Repeat("z", 64))},
		{"33 raw bytes", make([]byte, 33)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := nip44.Seal(owner, member.PubKeyHex(), tc.payload)
			if err != nil {
				t.Fatalf("nip44.Seal: %v", err)
			}
			if _, err := unwrapKey(member, owner.PubKeyHex(), w); err == nil {
				t.Fatalf("unwrapKey accepted a %d-byte payload %q; it must fail closed", len(tc.payload), tc.payload)
			}
		})
	}
}

// TestKeydistUnwrapKeyPayloadReportsTheEncoding pins the fact `rd confidential
// rewrap` (ready-470) is built on: the SAME key comes back from both encodings, so
// the key value alone cannot tell you whether a browser can read a grant — only
// browserSafe can. If this ever reported true for a raw payload, the re-wrap
// command would decide there was nothing to do and every legacy board would stay
// [encrypted] in the browser while rd insisted it was fine.
func TestKeydistUnwrapKeyPayloadReportsTheEncoding(t *testing.T) {
	owner, member := kdKey(t), kdKey(t)
	cek, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}

	// The wrap WrapKey emits today: 64 hex characters, survives NIP-07's string return.
	current, err := WrapKey(owner, member.PubKeyHex(), cek)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	got, browserSafe, err := UnwrapKeyPayload(member, owner.PubKeyHex(), current)
	if err != nil {
		t.Fatalf("UnwrapKeyPayload(hex): %v", err)
	}
	if got != cek {
		t.Fatal("hex payload unwrapped to the wrong key")
	}
	if !browserSafe {
		t.Fatal("UnwrapKeyPayload reported WrapKey's own output as NOT browser-safe")
	}

	// The pre-ready-c4b wrap: the raw 32 bytes, still on relays today.
	legacy, err := nip44.Seal(owner, member.PubKeyHex(), cek[:])
	if err != nil {
		t.Fatalf("nip44.Seal: %v", err)
	}
	got, browserSafe, err = UnwrapKeyPayload(member, owner.PubKeyHex(), legacy)
	if err != nil {
		t.Fatalf("UnwrapKeyPayload(raw): %v", err)
	}
	if got != cek {
		t.Fatal("raw payload unwrapped to the wrong key")
	}
	if browserSafe {
		t.Fatal("UnwrapKeyPayload called a RAW payload browser-safe — a browser's UTF-8 decode destroys those bytes")
	}

	// The owner can open a wrap it sealed to a MEMBER (NIP-44's conversation key is
	// symmetric). That is what lets the re-wrap recover the member's exact key value
	// instead of substituting one, so it is a load-bearing fact, not a curiosity.
	fromOwnerSide, _, err := UnwrapKeyPayload(owner, member.PubKeyHex(), legacy)
	if err != nil {
		t.Fatalf("owner cannot open the wrap it sealed to a member: %v", err)
	}
	if fromOwnerSide != cek {
		t.Fatal("the owner-side open of a member wrap yielded different bytes")
	}
}
