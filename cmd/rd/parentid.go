package main

import (
	"fmt"
	"strings"
)

// parentIDNone is the sentinel spelling — case-insensitive, surrounding
// whitespace ignored — that means "no parent" on both `rd update --parent-id`
// and `rd create --parent-id`. ready-ca3(b): before this fix the literal
// string "none" was stored as Item.ParentID (rd show then printed
// `Parent:   none`, visually identical to having no parent at all, but it was
// a dangling pointer to a nonexistent item). ready-ca3(c): there was also no
// OTHER way to clear a parent once set (an empty --parent-id value falls
// through to update.go's "no fields to update" error, since an empty flag is
// indistinguishable from the flag not being passed). This spelling fixes both:
// it is the value a user reaching for "no parent" already tries, so it costs
// no new vocabulary, and it is documented on both commands' --parent-id flag
// help text.
const parentIDNone = "none"

// isParentIDNone reports whether raw is the clear-parent sentinel.
func isParentIDNone(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), parentIDNone)
}

// resolveParentIDField interprets a raw --parent-id flag value against
// existing, the set of item ids the caller must have read from the LIVE nostr
// projection on THIS call (never a cached/stale snapshot — see
// nostrExistingIDs, which re-reads the whole log on every invocation), and
// returns the value to store on Item.ParentID.
//
//   - "" (flag not given) passes through unchanged — the caller is expected to
//     gate this function on its own hasFieldUpdate-style check; this function
//     never decides whether the field was touched, only what it resolves to.
//   - the clear sentinel (isParentIDNone) resolves to "" — ready-ca3(b)/(c).
//   - any other non-empty value MUST name an id present in existing, or the
//     call is rejected with an error naming the missing id — ready-ca3(a).
//     This is the fix for the silent-orphan-improvement defect: accepting an
//     unknown parent left an item's ParentID pointing at nothing, which reads
//     as "adopted" to anything counting ParentID != "" while the item is
//     actually orphaned worse than before (its real parent, if any, was
//     overwritten). Rejecting outright means the orphan count can only move
//     when the write actually adopts the item somewhere real.
//
// Shared by rd create and rd update so the two commands validate --parent-id
// identically instead of diverging (rd create previously did not validate at
// all — cmd/rd/create.go's parentID flag flowed straight into the item struct
// with no existence check).
func resolveParentIDField(raw string, existing map[string]struct{}) (string, error) {
	if raw == "" {
		return "", nil
	}
	if isParentIDNone(raw) {
		return "", nil
	}
	if _, ok := existing[raw]; !ok {
		return "", fmt.Errorf("--parent-id %q: no such item in this project", raw)
	}
	return raw, nil
}
