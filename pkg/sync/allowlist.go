// Relay write-allowlist regeneration from signed role-grants (ready-84e / BP-5).
//
// The relay write-allowlist (ready-266, scripts/relay-policy/write-allowlist.json)
// is the COARSE spam/DoS gate: a binary {pubkey:label} map, mtime-reload,
// fail-closed. Its CONTRACT is unchanged — only its FEED changes here. Before BP-5
// the file was hand-edited and kept consistent with rd's client-side trust set BY
// HAND (the drift docs/relay-runbook.md warns about). Now both the graded read-trust
// set and this coarse relay file derive from ONE signed source: the kind-39301
// role-grants (design docs/design/nostr-identity-model.md §4, §6, adversary A3).
//
// SAFETY — this file feeds a change to the LIVE, LOCKED relays. A wrong
// regeneration that dropped a currently-admitted writer would lock rd out of its own
// relays. The invariant enforced here (PlanAllowlist) is therefore:
//
//	A key is REMOVED from the relay allowlist IF AND ONLY IF it has an explicit
//	role=revoked grant. A currently-admitted key with NO grant at all is PRESERVED
//	(carried forward), never silently dropped.
//
// This is exactly the design mandate "revocation = publish role=revoked": there is
// no un-grant, only revoke. It means adding a key requires a grant (level >= 1) and
// removing one requires a revoke — and a live third-party tenant key that shares the
// relay but has no rd grant (e.g. a separate exchange operator) can never be locked
// out by a regeneration. Such unmanaged-but-preserved keys are surfaced LOUDLY
// (AllowlistPlan.Preserved) so the drift is visible, not silent.
package sync

import (
	"sort"

	"github.com/campfire-net/ready/pkg/nostr"
)

// DeriveAllowlist regenerates the relay write-allowlist {pubkey:label} from the
// signed role-grants in events, for the authority chain rooted at boardAuthor. The
// admitted set is { boardAuthor } ∪ { grantee : winning grant is non-revoked
// (level >= 1) }. Revoked grantees (level 0) are pruned. This is the design's
// "regenerate from {level >= 1, non-revoked}" (§4) — a PURE function of the signed
// log, no baseline, no I/O.
//
// Labels come from each winning grant's content (the human label that replaces the
// hand-kept pubkey->label map). boardAuthorLabel labels the bootstrap owner key
// (which has no grant of its own); when a later grant also names boardAuthor, that
// grant's label wins.
func DeriveAllowlist(events []*nostr.Event, boardAuthor, boardAuthorLabel string) map[string]string {
	_, _, winning := deriveGrants(events, boardAuthor)

	out := make(map[string]string)
	if boardAuthor != "" {
		out[boardAuthor] = boardAuthorLabel // bootstrap owner — always admitted.
	}
	for grantee, g := range winning {
		if g.Role == RoleRevoked {
			delete(out, grantee) // pruned: an explicit revoke removes the key.
			continue
		}
		out[grantee] = g.Label
	}
	return out
}

// revokedSet returns the set of grantees whose WINNING grant is role=revoked, for
// the authority chain rooted at boardAuthor. A key is in this set iff it has an
// explicit, currently-effective revoke — the only condition under which
// PlanAllowlist permits removing a currently-admitted key.
func revokedSet(events []*nostr.Event, boardAuthor string) map[string]bool {
	_, _, winning := deriveGrants(events, boardAuthor)
	out := make(map[string]bool)
	for grantee, g := range winning {
		if g.Role == RoleRevoked {
			out[grantee] = true
		}
	}
	return out
}

// AllowlistPlan is the reviewable result of reconciling the derived allowlist
// against the current baseline (the live relay allowlist, or the on-disk file). It
// is what `rd nostr sync-allowlist --dry-run` prints and what the apply path acts
// on. Final is the allowlist that WOULD be written; the slices explain the diff.
type AllowlistPlan struct {
	// Final is the {pubkey:label} allowlist to write. It is:
	//   derived{level>=1, non-revoked} ∪ preserve{baseline keys with no revoke grant}.
	Final map[string]string
	// Added are pubkeys in Final not in the baseline (newly granted).
	Added []string
	// Removed are pubkeys in the baseline not in Final. By construction EVERY
	// removed key has an explicit revoke grant (see LockoutViolations).
	Removed []string
	// Preserved are baseline pubkeys carried into Final that have NO grant at all
	// (neither add nor revoke) — unmanaged keys that must not be silently dropped.
	// Surfaced so the operator can see (and choose to formally grant or revoke) them.
	Preserved []string
	// LockoutViolations are baseline pubkeys that would be dropped WITHOUT an
	// explicit revoke grant. Under this plan's preserve-semantics this is ALWAYS
	// empty — it is a defense-in-depth assertion. A non-empty value means a bug in
	// the derivation, and the apply path MUST fail closed rather than lock the key out.
	LockoutViolations []string
}

// PlanAllowlist reconciles the grant-derived allowlist against baseline (the current
// live/on-disk {pubkey:label} allowlist) under the no-lockout invariant: a key is
// removed IFF it has an explicit role=revoked grant; every other baseline key is
// preserved. Pure and total — the CLI does the I/O (fetch baseline, write, push);
// this decides the change so it is unit-testable without a relay.
func PlanAllowlist(events []*nostr.Event, boardAuthor, boardAuthorLabel string, baseline map[string]string) AllowlistPlan {
	derived := DeriveAllowlist(events, boardAuthor, boardAuthorLabel)
	revoked := revokedSet(events, boardAuthor)

	final := make(map[string]string, len(derived)+len(baseline))
	for pk, label := range derived {
		final[pk] = label
	}

	var preserved, lockout []string
	for pk, label := range baseline {
		if _, inFinal := final[pk]; inFinal {
			continue
		}
		if revoked[pk] {
			continue // explicit revoke — legitimately dropped.
		}
		// Currently admitted, not re-derived, and NOT explicitly revoked: preserve it
		// (never silently lock out). Keep the baseline label.
		final[pk] = label
		preserved = append(preserved, pk)
	}

	var added, removed []string
	for pk := range final {
		if _, inBase := baseline[pk]; !inBase {
			added = append(added, pk)
		}
	}
	for pk := range baseline {
		if _, inFinal := final[pk]; !inFinal {
			removed = append(removed, pk)
			if !revoked[pk] {
				// Should be unreachable given the preserve loop above — assert it.
				lockout = append(lockout, pk)
			}
		}
	}

	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(preserved)
	sort.Strings(lockout)
	return AllowlistPlan{
		Final:             final,
		Added:             added,
		Removed:           removed,
		Preserved:         preserved,
		LockoutViolations: lockout,
	}
}
