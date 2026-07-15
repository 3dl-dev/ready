// CEK distribution + epoch rotation for confidential boards (epic ready-216,
// keydist item ready-a8a).
//
// Board membership carries the read key: the per-board content-encryption key
// (CEK) is NIP-44-v2-wrapped to each grantee and rides INSIDE the owner-signed
// kind-39301 role grant (see rolegrant.go RoleGrantSpec.WrappedCEK), so one signed
// action confers write authority AND the read key. On revoke the owner mints a new
// epoch CEK and re-wraps it to the REMAINING members only — a revoked member never
// receives the new-epoch wrap, so it cannot read cards authored after its
// revocation (forward secrecy, a capability dontguess deliberately lacks).
//
// Anti-replay: NIP-44 v2 has NO AAD, so the wrap alone does not bind the recipient.
// Binding rests on the SIGNED grant envelope — the grantee is the grant's p tag,
// covered by the owner's Schnorr signature — AND on the ECDH recipient-binding of
// the wrap itself. DeriveBoardKeyring enforces BOTH: it only unwraps grants whose p
// tag names the reader AND whose wrap actually opens for the reader's key, so a
// captured wrapped-CEK moved into a grant p-tagged to a different member is
// unusable (it will not open for that member, and re-signing the grant needs the
// owner key).
package sync

import (
	"crypto/rand"
	"fmt"

	"github.com/3dl-dev/ready/pkg/nip44"
	"github.com/3dl-dev/ready/pkg/nostr"
)

// MintKey returns a fresh random 32-byte key (a CEK or an LTK) from crypto/rand.
// Keys are NEVER content-derived — a content-derived key recreates a
// convergent-encryption equality/guess-confirmation oracle (spec §4, §6).
func MintKey() ([32]byte, error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return k, fmt.Errorf("sync: keydist: mint key: %w", err)
	}
	return k, nil
}

// WrapKey NIP-44-v2-wraps a 32-byte key to granteePubHex, sealed by owner. The
// result is carried as the grant's "cek"/"ltk" tag.
func WrapKey(owner *nostr.Key, granteePubHex string, key [32]byte) (string, error) {
	return nip44.Seal(owner, granteePubHex, key[:])
}

// unwrapKey opens a NIP-44 wrap that counterparty (the owner) sealed to reader.
func unwrapKey(reader *nostr.Key, counterpartyPubHex, wrapped string) ([32]byte, error) {
	var out [32]byte
	pt, err := nip44.Open(reader, counterpartyPubHex, wrapped)
	if err != nil {
		return out, err
	}
	if len(pt) != 32 {
		return out, fmt.Errorf("sync: keydist: unwrapped key is %d bytes, want 32", len(pt))
	}
	copy(out[:], pt)
	return out, nil
}

// BoardKeyring is a reader's derived confidential-board key material. It implements
// BoardDecryptor (CEK per epoch) and EncryptedBoardSet (Cutover), so it wires
// straight into ProjectOptions.{Decryptor, EncryptedBoards}.
type BoardKeyring struct {
	ceks    map[string]map[int][32]byte // boardCoord -> epoch -> CEK
	ltks    map[string][32]byte         // boardCoord -> LTK
	cutover map[string]int64            // boardCoord -> first-epoch cutover (unix seconds)
}

// CEK implements BoardDecryptor.
func (kr *BoardKeyring) CEK(coord string, epoch int) ([32]byte, bool) {
	var z [32]byte
	if kr == nil {
		return z, false
	}
	m, ok := kr.ceks[coord]
	if !ok {
		return z, false
	}
	c, ok := m[epoch]
	return c, ok
}

// LTK returns the reader's label-token key for a board (used by the tokenize-
// before-REQ query path, ready-c83), ok=false if the reader holds none.
func (kr *BoardKeyring) LTK(coord string) ([32]byte, bool) {
	var z [32]byte
	if kr == nil {
		return z, false
	}
	l, ok := kr.ltks[coord]
	return l, ok
}

// Cutover implements EncryptedBoardSet: the board-global created_at of the first
// CEK epoch, ok=true iff the board is confidential (has any CEK-bearing grant).
func (kr *BoardKeyring) Cutover(coord string) (int64, bool) {
	if kr == nil {
		return 0, false
	}
	c, ok := kr.cutover[coord]
	return c, ok
}

// CurrentEpoch returns the HIGHEST CEK epoch the reader holds for the board, its
// CEK, and ok=false if the reader holds none. The write path seals new cards under
// this epoch. LTK (if held) is returned via LTK(coord). A member that missed a
// rotation returns its highest-held epoch — which is stale; the owner (who minted
// the rotation and self-wrapped it) always holds the true current epoch.
func (kr *BoardKeyring) CurrentEpoch(coord string) (epoch int, cek [32]byte, ok bool) {
	if kr == nil {
		return 0, cek, false
	}
	m, present := kr.ceks[coord]
	if !present || len(m) == 0 {
		return 0, cek, false
	}
	for ep, c := range m {
		if ep > epoch {
			epoch, cek, ok = ep, c, true
		}
	}
	return epoch, cek, ok
}

// DeriveBoardKeyring scans the log for owner-signed kind-39301 grants and builds
// the reader's key material for board (boardAuthor, boardD):
//
//   - every CEK epoch wrapped to the reader that the reader can actually OPEN
//     (ECDH-bound — a wrap addressed to someone else, even inside a grant p-tagged
//     to the reader, fails to open and is skipped: the anti-retarget guard);
//   - the LTK, if wrapped to the reader;
//   - the board-global cutover = the earliest created_at of ANY owner-signed
//     CEK-bearing grant (marks when the board went confidential), independent of
//     which epochs the reader holds.
//
// ALL historical grants are scanned (NOT latest-wins): a member keeps the
// old-epoch CEKs it was given, so historical reads survive; a revoked member simply
// never receives a wrap for the new epoch (forward secrecy). Only grants SIGNED BY
// THE OWNER (boardAuthor, the authz root) contribute CEKs.
func DeriveBoardKeyring(events []*nostr.Event, reader *nostr.Key, boardAuthor, boardD string) *BoardKeyring {
	coord := BoardCoord(boardAuthor, boardD)
	kr := &BoardKeyring{ceks: map[string]map[int][32]byte{}, ltks: map[string][32]byte{}, cutover: map[string]int64{}}
	readerPub := reader.PubKeyHex()
	for _, e := range events {
		if e == nil || e.Kind != KindRoleGrant {
			continue
		}
		if err := e.Verify(); err != nil {
			continue
		}
		g, ok := parseRoleGrant(e)
		if !ok {
			continue
		}
		if g.BoardOwner != boardAuthor || g.BoardD != boardD {
			continue
		}
		// Only the OWNER mints CEKs — the authz root. A CEK "wrapped" by anyone else
		// is ignored (a non-owner cannot introduce a board key).
		if g.Signer != boardAuthor || g.WrappedCEK == "" {
			continue
		}
		// A valid CEK epoch is >= 1 (cards seal under Epoch >= 1). parseRoleGrant
		// coerces an unparseable cek_epoch tag to 0; reject such a grant's CEK
		// entirely rather than binding a key to a bogus epoch or setting the board
		// cutover from a malformed grant.
		if g.CEKEpoch < 1 {
			continue
		}
		// Board-global cutover: earliest owner CEK-bearing grant (public created_at,
		// tracked regardless of who the grant is addressed to).
		if cur, seen := kr.cutover[coord]; !seen || g.CreatedAt < cur {
			kr.cutover[coord] = g.CreatedAt
		}
		// Only grants addressed to the reader (signed p tag) can yield the reader's
		// keys, and only if the wrap actually opens for the reader's key.
		if g.Grantee != readerPub {
			continue
		}
		if cek, err := unwrapKey(reader, g.Signer, g.WrappedCEK); err == nil {
			if kr.ceks[coord] == nil {
				kr.ceks[coord] = map[int][32]byte{}
			}
			kr.ceks[coord][g.CEKEpoch] = cek
		}
		if g.WrappedLTK != "" {
			if ltk, err := unwrapKey(reader, g.Signer, g.WrappedLTK); err == nil {
				kr.ltks[coord] = ltk
			}
		}
	}
	return kr
}
