// Nostr role-grant events + graded operator-level derivation (ready-8ce / BP-2).
//
// This is the nostr port of pkg/provenance/checker.go. Operator trust is no
// longer a stored list or a flat allowlist — it is a PURE FUNCTION of signed
// events, recomputed each run (design docs/design/nostr-identity-model.md §3,§4).
//
// WIRE MAPPING — kind 39301 "rd role-grant" (addressable / parameterized-
// replaceable, deliberately away from NIP-100's 30301/30302 to avoid collision):
//
//	kind    39301
//	d       "<boardD>:<granteePubkeyHex>"  -> one addressable slot per (board,
//	                                          grantee) => latest-wins per grantee
//	p       <granteePubkeyHex>             -> the subject of the grant
//	a       "30301:<ownerPubkey>:<boardD>" -> binds the grant into the pinned
//	                                          board's authority chain
//	role    owner | maintainer | contributor | revoked
//	from    (optional) unix seconds        -> absent = prospective (effective at
//	                                          created_at); present = retroactive
//	                                          repudiation from T (compromise case)
//	content optional human label           -> replaces the hand-kept pubkey->label map
//
// LEVEL MAPPING — checker.go:34-46 verbatim: maintainer->2, contributor->1,
// revoked->0, no-grant->1. "owner" is NOT a 4th numeric level — it is the identity
// of the board author (the bootstrap level-2 trust root); a role=owner grant is
// numerically level 2, but only the board AUTHOR (the boardAuthor param) may mint
// maintainers/owners (the escalation cap below).
//
// This file is PURE (BP-2 scope): build/parse + DeriveLevels only. No CLI, no
// relay, no projection wiring — that is BP-3 / BP-5.
package sync

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// KindRoleGrant is the addressable "rd role-grant" event kind. It is purely
// additive: with zero 39301 events present, DeriveLevels falls back to the
// bootstrap rule (board author = level 2), so already-migrated items need no
// re-migration (design §6, backward compatibility).
const KindRoleGrant = 39301

// Role strings carried in a 39301 event's "role" tag.
const (
	RoleOwner       = "owner"
	RoleMaintainer  = "maintainer"
	RoleContributor = "contributor"
	RoleRevoked     = "revoked"
)

// LevelMaintainer / LevelContributor / LevelRevoked are the graded operator
// levels (checker.go:34-46). LevelDefault is the no-grant fallback (contributor).
const (
	LevelRevoked     = 0
	LevelContributor = 1
	LevelMaintainer  = 2
	LevelDefault     = LevelContributor
)

// authoritativeForever is the "+infinity" sentinel for a non-revoked key's
// authoritative-until timestamp: every event created_at is < this, so a
// non-revoked key's events are always honored (design §3, prospective revocation).
const authoritativeForever = int64(math.MaxInt64)

// RoleGrantSpec is the caller's view of a role-grant to build a 39301 event.
type RoleGrantSpec struct {
	// BoardD is the board (project) "d" identifier the grant is scoped to.
	BoardD string
	// BoardAuthor is the owner pubkey hex that authored the 30301 board — it forms
	// the "a" authority coordinate "30301:<BoardAuthor>:<BoardD>". The grant binds
	// into THAT board's authority chain, not the signer's own board.
	BoardAuthor string
	// Grantee is the subject pubkey hex the role is assigned to.
	Grantee string
	// Role is one of owner|maintainer|contributor|revoked.
	Role string
	// From, when > 0, is the effective-from unix-seconds timestamp. Absent (0) =
	// prospective (effective at created_at); present = retroactive repudiation.
	From int64
	// Label is an optional human label carried in content.
	Label string
	// Claim, when non-empty, is the one-use invite CLAIM-NONCE this grant consumes
	// (ready-ce0 self-mint model). The owner binds a joiner's self-minted pubkey to
	// the claim-nonce it presented; derivation enforces one-claim-nonce-per-pubkey
	// (single-use), so a leaked claim admitted to a SECOND pubkey is rejected.
	Claim string

	// WrappedCEK, when non-empty, is the per-board content-encryption key for
	// CEKEpoch, NIP-44-v2-wrapped to Grantee (confidential boards, epic ready-216).
	// It rides INSIDE this owner-signed grant so one signed action confers write
	// authority AND the read key. The grantee is bound by the SIGNED p tag, never a
	// payload field (NIP-44 v2 has no AAD) — see keydist.go WrapKey/DeriveBoardKeyring.
	WrappedCEK string
	// CEKEpoch is the epoch id WrappedCEK belongs to (>=1); cards sealed under that
	// epoch carry a matching cek_epoch marker. Emitted only when WrappedCEK is set.
	CEKEpoch int
	// WrappedLTK, when non-empty, is the per-board Label Token Key (stable across CEK
	// epochs, ruling (b)) NIP-44-v2-wrapped to Grantee — lets the member tokenize
	// labels before REQ and is epoch-independent.
	WrappedLTK string
}

// BuildRoleGrantEvent constructs and signs a kind-39301 role-grant. createdAt MUST
// be seconds (NIP-01) — the caller supplies it so id derivation is deterministic
// and testable. The event is signed by k (the GRANTING key); DeriveLevels enforces
// the escalation cap against k's derived level, so a signer can never grant above
// its own tier regardless of what it builds here.
func BuildRoleGrantEvent(k *nostr.Key, spec RoleGrantSpec, createdAt int64) (*nostr.Event, error) {
	if spec.BoardD == "" {
		return nil, fmt.Errorf("sync: role-grant: empty board d")
	}
	if spec.BoardAuthor == "" {
		return nil, fmt.Errorf("sync: role-grant: empty board author")
	}
	if spec.Grantee == "" {
		return nil, fmt.Errorf("sync: role-grant: empty grantee")
	}
	switch spec.Role {
	case RoleOwner, RoleMaintainer, RoleContributor, RoleRevoked:
	default:
		return nil, fmt.Errorf("sync: role-grant: invalid role %q", spec.Role)
	}
	tags := [][]string{
		{"d", roleGrantD(spec.BoardD, spec.Grantee)},
		{"p", spec.Grantee},
		{"a", BoardCoord(spec.BoardAuthor, spec.BoardD)},
		{"role", spec.Role},
	}
	if spec.From > 0 {
		tags = append(tags, []string{"from", strconv.FormatInt(spec.From, 10)})
	}
	if spec.Claim != "" {
		tags = append(tags, []string{"claim", spec.Claim})
	}
	// Confidential-board key material (ready-216): the wrapped CEK (+ its epoch) and
	// the wrapped LTK ride as ADDITIVE signed tags. The owner's Schnorr signature
	// covers them and the grantee is bound by the p tag above — a captured wrap
	// cannot be retargeted (re-signing needs the owner key; the wrap is ECDH-bound to
	// the original grantee). A plaintext grant emits none of these (zero drift).
	if spec.WrappedCEK != "" {
		tags = append(tags, []string{"cek", spec.WrappedCEK})
		tags = append(tags, []string{"cek_epoch", strconv.Itoa(spec.CEKEpoch)})
	}
	if spec.WrappedLTK != "" {
		tags = append(tags, []string{"ltk", spec.WrappedLTK})
	}
	e := &nostr.Event{
		Kind:      KindRoleGrant,
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   spec.Label,
	}
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign role-grant event: %w", err)
	}
	return e, nil
}

// roleGrantD returns the addressable "d" identifier for a (board, grantee) grant
// slot: "<boardD>:<granteePubkeyHex>". One slot per (board, grantee) gives
// latest-wins per grantee for free (via the addressable-event replaceable rule).
func roleGrantD(boardD, grantee string) string {
	return boardD + ":" + grantee
}

// roleGrant is the parsed, semantically-meaningful view of a verified 39301 event.
type roleGrant struct {
	// Signer is the pubkey hex that signed the grant (e.PubKey).
	Signer string
	// Grantee is the subject pubkey hex ("p" tag).
	Grantee string
	// Role is the granted role string ("role" tag).
	Role string
	// BoardOwner and BoardD are the owner/d parsed from the "a" authority
	// coordinate "30301:<owner>:<d>".
	BoardOwner string
	BoardD     string
	// From is the effective-from unix seconds ("from" tag), or 0 when absent.
	From int64
	// CreatedAt / ID come from the event and drive latest-wins ordering.
	CreatedAt int64
	ID        string
	// Label is the optional content label.
	Label string
	// Claim is the one-use invite claim-nonce this grant consumes ("claim" tag), or
	// "" when the grant is not bound to an invite claim.
	Claim string
	// Confidential-board key material (ready-216), empty on a plaintext grant:
	// WrappedCEK ("cek" tag) is the NIP-44-wrapped per-board CEK for CEKEpoch
	// ("cek_epoch" tag); WrappedLTK ("ltk" tag) is the wrapped label-token key.
	WrappedCEK string
	CEKEpoch   int
	WrappedLTK string
}

// parseRoleGrant extracts a roleGrant from a kind-39301 event. It returns ok=false
// when the event is not a well-formed role-grant (wrong kind, missing grantee/role,
// unrecognized role, or an "a" coordinate that does not name a 30301 board). It
// does NOT verify the signature — DeriveLevels does that before calling.
func parseRoleGrant(e *nostr.Event) (roleGrant, bool) {
	if e == nil || e.Kind != KindRoleGrant {
		return roleGrant{}, false
	}
	grantee := tagValue(e, "p")
	role := tagValue(e, "role")
	if grantee == "" || role == "" {
		return roleGrant{}, false
	}
	switch role {
	case RoleOwner, RoleMaintainer, RoleContributor, RoleRevoked:
	default:
		return roleGrant{}, false
	}
	owner, boardD, ok := parseBoardCoord(tagValue(e, "a"))
	if !ok {
		return roleGrant{}, false
	}
	var from int64
	if f := tagValue(e, "from"); f != "" {
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil || v < 0 {
			return roleGrant{}, false
		}
		from = v
	}
	cekEpoch := 0
	if s := tagValue(e, "cek_epoch"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			cekEpoch = v
		}
	}
	return roleGrant{
		Signer:     e.PubKey,
		Grantee:    grantee,
		Role:       role,
		BoardOwner: owner,
		BoardD:     boardD,
		From:       from,
		CreatedAt:  e.CreatedAt,
		ID:         e.ID,
		Label:      e.Content,
		Claim:      tagValue(e, "claim"),
		WrappedCEK: tagValue(e, "cek"),
		CEKEpoch:   cekEpoch,
		WrappedLTK: tagValue(e, "ltk"),
	}, true
}

// ParseBoardCoord splits a pinned "30301:<owner>:<d>" board authority coordinate
// into its owner pubkey and board "d" identifier, ok=false when malformed. Exported
// so cmd/rd can resolve the OWNER pubkey from the pinned board (BP-4): an agent
// signing a card sets the card's board-membership "a" to the owner's coordinate.
func ParseBoardCoord(a string) (owner, boardD string, ok bool) {
	return parseBoardCoord(a)
}

// parseBoardCoord splits a "30301:<owner>:<d>" board authority coordinate. It
// returns ok=false for any coordinate that is not a well-formed 30301 board coord.
func parseBoardCoord(a string) (owner, boardD string, ok bool) {
	// Use SplitN=3 so a boardD that itself contains ':' survives intact.
	parts := splitN3(a, ':')
	if len(parts) != 3 {
		return "", "", false
	}
	if parts[0] != strconv.Itoa(KindBoard) {
		return "", "", false
	}
	if parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// splitN3 splits s on sep into at most 3 fields (the final field keeps any
// remaining separators). Local helper to avoid importing strings just for this.
func splitN3(s string, sep byte) []string {
	out := make([]string, 0, 3)
	for i := 0; i < 2; i++ {
		idx := indexByte(s, sep)
		if idx < 0 {
			break
		}
		out = append(out, s[:idx])
		s = s[idx+1:]
	}
	out = append(out, s)
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// roleToLevel maps a role string to a numeric operator level — checker.go:34-46
// verbatim for the three graded roles, plus owner. "owner" is numerically level 2
// (>= maintainer); its distinguishing power lives in the escalation cap, not the
// level number.
func roleToLevel(role string) int {
	switch role {
	case RoleOwner, RoleMaintainer:
		return LevelMaintainer
	case RoleContributor:
		return LevelContributor
	case RoleRevoked:
		return LevelRevoked
	default:
		// Unknown roles default to contributor (level 1) — checker.go default.
		return LevelDefault
	}
}

// DeriveLevels computes the graded operator-level map and each key's
// authoritative-until timestamp from a set of signed events, for the authority
// chain rooted at the FULL board coordinate 30301:<boardAuthor>:<boardD>. It is the
// nostr port of NewStoreChecker (checker.go) and is PURE: same inputs -> same
// outputs, no I/O, no clock.
//
// Semantics (design §3, §4):
//
//   - Bootstrap: boardAuthor (the 30301 board author / owner) = level 2, the
//     self-certifying trust anchor (checker.go:102-104).
//   - Only verified 39301 events whose "a" coordinate names the PINNED board
//     coordinate EXACTLY — owner == boardAuthor AND d == boardD (GAP-2, ready-885) —
//     are replayed. Binding by owner alone let a grant on 30301:<owner>:<other-d>
//     be honored on the pinned board (cross-board grant bleed, contradicting design
//     §3 "bound per board coordinate 30301:<author>:<d>"); the full-coordinate match
//     closes it. An empty boardD matches no grant (grants always carry a non-empty
//     boardD), so a caller that cannot resolve the pinned d derives only the
//     bootstrap owner — fail-closed, never cross-board.
//   - Grants are replayed in (created_at, id) order; latest per grantee wins via
//     the same tie-break as card projection (newerThan, nostrproject.go:392).
//   - ESCALATION CAP: only boardAuthor may sign a role=maintainer or role=owner
//     grant; a level-2 maintainer (not the board author) may sign only
//     contributor/revoked; any lower signer may grant nothing. A grant violating
//     the cap is IGNORED — a signer can never grant above its own tier. The
//     signer's level is evaluated against state replayed so far, so a maintainer's
//     authority must be established (an earlier valid grant) before it can delegate.
//   - authoritative-until = +infinity if the grantee's winning grant is not
//     revoked, else the revoking grant's "from" (or its created_at when "from" is
//     absent) — PROSPECTIVE revocation: a departed key's past events stay honored,
//     completed items do not reopen (design §3, A1).
//
// The returned level map contains boardAuthor and every explicitly-granted key;
// keys absent from the map are level 1 (default contributor), matching
// checker.go's Level() fallback — callers must apply that default, not read a
// missing key as level 0. The until map is populated in lockstep with levels.
func DeriveLevels(events []*nostr.Event, boardAuthor, boardD string) (levels map[string]int, until map[string]int64) {
	levels, until, _, _ = deriveGrants(events, boardAuthor, boardD)
	return levels, until
}

// ClaimGrantee reports the grantee pubkey an invite claim-nonce is currently bound
// to under the cap-valid winning grants for the board 30301:<boardAuthor>:<boardD>,
// and whether any binding exists (ready-ce0). It is the single-use lookup the owner's
// `rd grant --claim` uses to FAIL FAST when a claim-nonce has already admitted a
// different self-minted key — the same one-claim-nonce-per-pubkey rule deriveGrants
// enforces at the projection seam (defense in depth). Pure of I/O and clock.
func ClaimGrantee(events []*nostr.Event, boardAuthor, boardD, claim string) (grantee string, bound bool) {
	if claim == "" {
		return "", false
	}
	_, _, _, claimedBy := deriveGrants(events, boardAuthor, boardD)
	g, ok := claimedBy[claim]
	return g, ok
}

// DeriveReadTrust returns the READ-TRUST membership set implied by the signed
// role-grants for the pinned board coordinate 30301:<boardAuthor>:<boardD> (GAP-1,
// ready-7c1): the board author (the bootstrap level-2 trust root — ALWAYS present,
// which is what makes the bootstrap non-circular: owner-signed grants are admitted
// because the owner is self-rooting from the pin) UNIONED with every grantee that
// holds a cap-valid winning grant.
//
// A revoked grantee (level 0) IS included: its PAST authoritative events must stay
// admissible so a completed item does not reopen (prospective revocation, design §3
// A1). Dropping a revoked key from membership entirely would erase its past events —
// the very bug the point-in-time authoritative-until gate exists to avoid. Future
// events of a revoked key are dropped by that until gate at projection (and it is
// pruned from the relay write-allowlist), so including it here is fail-closed.
//
// Fail-closed for the case that matters: a key that never received an owner-rooted
// grant is ABSENT from the returned set, so an ungranted foreign key is never
// admitted by grant-derivation — only self and the operator's bootstrap
// Config.TrustedPubkeys can admit it, exactly as before.
func DeriveReadTrust(events []*nostr.Event, boardAuthor, boardD string) map[string]bool {
	levels, _, _, _ := deriveGrants(events, boardAuthor, boardD)
	out := make(map[string]bool, len(levels))
	for pk := range levels {
		out[pk] = true
	}
	return out
}

// deriveGrants is the shared core of DeriveLevels and DeriveAllowlist: it replays
// the cap-valid winning grant per grantee and returns the level map, the
// authoritative-until map, AND the winning roleGrant per grantee (which carries the
// human label needed to regenerate the relay write-allowlist, BP-5). Keeping ONE
// replay means the graded read-trust set and the coarse relay allowlist derive from
// exactly the same signed source — the drift the runbook warns about is closed
// structurally (design §4, §6, A3), not by keeping two derivations in step by hand.
func deriveGrants(events []*nostr.Event, boardAuthor, boardD string) (levels map[string]int, until map[string]int64, winning map[string]roleGrant, claimedBy map[string]string) {
	levels = make(map[string]int)
	until = make(map[string]int64)
	claimedBy = make(map[string]string)

	// Bootstrap: the board author is the implicit level-2 trust root.
	if boardAuthor != "" {
		levels[boardAuthor] = LevelMaintainer
		until[boardAuthor] = authoritativeForever
	}

	// Collect verified, well-formed grants bound to boardAuthor's board.
	var grants []roleGrant
	for _, e := range events {
		if e == nil || e.Kind != KindRoleGrant {
			continue
		}
		// Only signed, internally-consistent events count (schnorr Verify) — mirrors
		// the read-side gate; a forged/tampered grant cannot influence levels.
		if err := e.Verify(); err != nil {
			continue
		}
		g, ok := parseRoleGrant(e)
		if !ok {
			continue
		}
		// Bind to the pinned board's FULL authority coordinate (GAP-2, ready-885): the
		// grant's "a" must name owner==boardAuthor AND d==boardD. Binding by owner alone
		// honored a grant on 30301:<owner>:<any-other-d> on the pinned board — cross-board
		// grant bleed. Requiring the full coordinate closes it; an empty boardD matches
		// no grant (fail-closed), never every board.
		if boardAuthor == "" || boardD == "" || g.BoardOwner != boardAuthor || g.BoardD != boardD {
			continue
		}
		grants = append(grants, g)
	}

	// Replay oldest-first. Ascending (created_at, id): grant i is older than j iff
	// j would REPLACE i under newerThan.
	sort.SliceStable(grants, func(i, j int) bool {
		return newerGrant(grants[j], grants[i])
	})

	// winning[grantee] = the newest CAP-VALID grant applied for that grantee. We
	// process ascending and overwrite, so the last valid grant applied wins.
	winning = make(map[string]roleGrant)
	for _, g := range grants {
		if !signerMayGrant(levels, boardAuthor, g.Signer, g.Grantee, g.Role) {
			continue // escalation-cap violation — ignored.
		}
		// SINGLE-USE CLAIM BINDING (ready-ce0, owner-scoped ready-557): a grant that
		// consumes an invite claim-nonce binds that nonce to EXACTLY ONE grantee,
		// first-cap-valid-wins. We process ascending, so the earliest OWNER grant for a
		// claim owns it; a later owner grant reusing the SAME claim for a DIFFERENT
		// grantee is ignored — a leaked claim-nonce can never admit a second self-minted
		// key. The same grantee may re-grant under its own claim (e.g. later revoke), so
		// the guard only fires on a grantee MISMATCH.
		//
		// OWNER-ONLY CONSUMPTION (ready-557): only the board OWNER may consume a
		// claim-nonce. The mint-and-ship flow has the owner sign the claim-binding grant
		// (nostr_invite.go), so a claim tag on a NON-owner (maintainer) grant is inert —
		// it neither binds nor drops the grant, which still applies as an ordinary
		// contributor grant a maintainer is already entitled to publish. Without this
		// scope a rogue/compromised maintainer could pre-consume an owner's outstanding
		// claim-nonce for a grantee of its choosing, forcing the legit joiner to
		// re-request a fresh invite (griefing, not escalation — single-use still held).
		// This is the real, owner-enforced single-use the deleted kind-39303 relay
		// marker only pretended to provide.
		if g.Claim != "" && g.Signer == boardAuthor {
			if bound, ok := claimedBy[g.Claim]; ok && bound != g.Grantee {
				continue
			}
			claimedBy[g.Claim] = g.Grantee
		}
		levels[g.Grantee] = roleToLevel(g.Role)
		winning[g.Grantee] = g
	}

	// Compute authoritative-until from each grantee's winning grant.
	for grantee, g := range winning {
		if g.Role == RoleRevoked {
			if g.From > 0 {
				until[grantee] = g.From
			} else {
				until[grantee] = g.CreatedAt
			}
		} else {
			until[grantee] = authoritativeForever
		}
	}

	return levels, until, winning, claimedBy
}

// signerMayGrant applies the escalation cap. levels reflects state replayed so
// far; boardAuthor is the trust root; grantee is the subject of the grant.
//
//   - Granting maintainer/owner: only boardAuthor may.
//   - Granting contributor/revoked: boardAuthor or a level-2 maintainer may,
//     SUBJECT to the owner-lockout / peer-maintainer protections below.
//   - Any lower signer: may grant nothing.
//
// OWNER-LOCKOUT (HIGH-1, the serious privilege-escalation): a non-owner signer
// may NEVER revoke or downgrade the board author. Without this, any level-2
// maintainer the owner appointed could publish role=revoked (or role=contributor)
// TARGETING the boardAuthor; deriveGrants would drop the owner to level 0/1 and
// the projection seam (nostrproject.go until-gate) would then discard the owner's
// own events — the self-certifying trust anchor becomes revocable by a key it
// appointed. The owner is IRREVOCABLE: its level is fixed by the bootstrap and no
// grant targeting it from any non-owner signer takes effect.
//
// PEER-MAINTAINER PROTECTION (HIGH-1): only the owner may revoke or demote a
// MAINTAINER. A non-owner signer's contributor/revoked grant targeting a key that
// is CURRENTLY a maintainer (level >= 2) is ignored, so one maintainer cannot
// unseat a peer (or itself) — the maintainer set changes only by the owner's hand.
func signerMayGrant(levels map[string]int, boardAuthor, signer, grantee, role string) bool {
	isBoardAuthor := signer != "" && signer == boardAuthor
	switch role {
	case RoleMaintainer, RoleOwner:
		return isBoardAuthor
	case RoleContributor, RoleRevoked:
		if isBoardAuthor {
			return true // the owner may revoke/demote anyone (including a maintainer).
		}
		if levels[signer] < LevelMaintainer {
			return false // only a maintainer may grant contributor/revoked at all.
		}
		// The board author (owner) is irrevocable by any non-owner signer.
		if grantee == boardAuthor {
			return false
		}
		// Only the owner may revoke/demote a current maintainer (peer protection).
		if levels[grantee] >= LevelMaintainer {
			return false
		}
		return true
	default:
		return false
	}
}

// MayGrant is the exported, client-side mirror of the read-side escalation cap
// (signerMayGrant): it derives the operator levels for the authority chain rooted
// at 30301:<boardAuthor>:<boardD> from the signed events, then reports whether
// signer may publish a grant of role to grantee under that state. `rd nostr
// grant/revoke` calls it to FAIL FAST with the SAME rule DeriveLevels enforces at
// the projection seam, so a cap-violating grant (a contributor delegating, a
// maintainer revoking the owner or a peer maintainer) is rejected client-side as a
// clear early error rather than being silently ignored only at derive (MED-6,
// defense in depth). It is a pure function of the events — no I/O, no clock.
func MayGrant(events []*nostr.Event, boardAuthor, boardD, signer, grantee, role string) bool {
	levels, _, _, _ := deriveGrants(events, boardAuthor, boardD)
	return signerMayGrant(levels, boardAuthor, signer, grantee, role)
}

// newerGrant reports whether grant a should REPLACE grant b under the deterministic
// latest-wins order (newerThan, nostrproject.go:392): newer created_at wins; on a
// created_at TIE the LOWEST id wins.
func newerGrant(a, b roleGrant) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID < b.ID
}
