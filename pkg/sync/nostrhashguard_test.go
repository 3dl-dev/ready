package sync

// No-plaintext-hash guardrail (ready-aea, epic ready-216).
//
// CANONICAL INVARIANT (frozen spec §6, docs/design/confidential-boards-envelope.md):
// rd MUST NEVER emit a plaintext content-hash tag on a card or status event. An
// unsalted sha256(title)/sha256(description) tag is a guess-confirmation oracle
// (any passive relay REQ-er hashes a guessed plaintext and confirms it for free,
// defeating the AEAD) plus a cross-card correlation oracle (identical hashes
// reveal identical content). rd avoids it BY CONSTRUCTION — addressable
// latest-wins dedupe is (author pubkey, kind, d-tag); the d tag alone is the
// dedupe key, so no content-hash tag ever has a legitimate reason to appear.
//
// This is a STANDING invariant: it passes on the plaintext tree today and must
// keep passing after the encrypted write path lands. The label token added by
// ruling (b) is NOT a violation — it is a KEYED HMAC of a ROUTING tag value under
// a secret per-board key, not an unsalted hash of a free-text field, so it does
// not appear in the free-text digest scan below (which is deliberately UNSALTED
// sha256/sha1 of the free-text fields — the actual oracle shape).

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
)

// forbiddenHashTagKeys are tag KEYS that must never appear on a card or status
// event — a content hash smuggled under an obvious name.
var forbiddenHashTagKeys = map[string]bool{
	"content_hash":           true,
	"plaintext_content_hash": true,
	"content-hash":           true,
	"title_hash":             true,
	"desc_hash":              true,
	"description_hash":       true,
	"hash":                   true,
	"h":                      true,
	"x":                      true,
}

// digestForms returns every wire form a plaintext hash of s could take: sha256
// and sha1, each as lowercase hex and standard base64. Scanning tag VALUES
// against this set catches a hash smuggled under an innocuous tag name.
func digestForms(s string) []string {
	sum256 := sha256.Sum256([]byte(s))
	sum1 := sha1.Sum([]byte(s))
	return []string{
		hex.EncodeToString(sum256[:]),
		base64.StdEncoding.EncodeToString(sum256[:]),
		hex.EncodeToString(sum1[:]),
		base64.StdEncoding.EncodeToString(sum1[:]),
	}
}

// scanForContentHash is a PURE detector (no *testing.T) so the guardrail can be
// unit-tested for non-vacuity. It returns a violation message for (a) any tag
// whose KEY is in forbiddenHashTagKeys, and (b) any tag VALUE equal to an
// unsalted sha256/sha1 (hex or base64) of any provided free-text field. Empty
// result == clean. freeText maps field label → plaintext; the scan is a loop, so
// future free-text fields extend it trivially.
func scanForContentHash(evTags [][]string, freeText map[string]string) []string {
	forbiddenValues := map[string]string{} // digest value -> field that produced it
	for field, plain := range freeText {
		if plain == "" {
			continue
		}
		for _, d := range digestForms(plain) {
			forbiddenValues[d] = field
		}
	}

	var violations []string
	for _, tg := range evTags {
		if len(tg) == 0 {
			continue
		}
		key := tg[0]
		if forbiddenHashTagKeys[key] {
			violations = append(violations, fmt.Sprintf("forbidden content-hash tag key %q (tag=%v)", key, tg))
		}
		for _, val := range tg[1:] {
			if field, bad := forbiddenValues[val]; bad {
				violations = append(violations, fmt.Sprintf("tag %q carries a plaintext hash of free-text field %q (value=%q)", key, field, val))
			}
		}
	}
	return violations
}

// TestCardsCarryNoContentHash asserts BuildCardEvent never emits a plaintext
// content-hash tag for a fully-populated item.
func TestCardsCarryNoContentHash(t *testing.T) {
	k := testKey(t)
	spec := CardSpec{
		ItemID:      "ready-guard1",
		Title:       "Rotate the on-call pager secret before Friday",
		Status:      "active",
		Priority:    "p1",
		Assignee:    k.PubKeyHex(),
		Type:        "task",
		Context:     "The pager secret in vault leaked in a screenshot; rotate it and audit access.",
		BoardD:      "ready",
		Deps:        []string{"ready-aaa", "ready-bbb"},
		Gate:        "security",
		WaitingType: "gate",
		WaitingOn:   "ready-ccc",
		Labels:      []string{"security", "urgent"},
		ETA:         "2026-07-18T00:00:00Z",
		Level:       "1",
		For:         "baron@3dl.dev",
		ParentID:    "ready-parent",
		Due:         "2026-07-17T00:00:00Z",
	}
	ce, err := BuildCardEvent(k, spec, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	if v := scanForContentHash(ce.Tags, map[string]string{
		"title":      spec.Title,
		"context":    spec.Context,
		"waiting_on": spec.WaitingOn,
	}); len(v) != 0 {
		t.Fatalf("card carries a content hash: %v", v)
	}
}

// TestStatusEventsCarryNoContentHash asserts BuildStatusEvent never emits a
// plaintext hash of the close/change reason.
func TestStatusEventsCarryNoContentHash(t *testing.T) {
	k := testKey(t)
	reason := "Closed: rotated the secret and revoked the leaked screenshot's grants."
	se, err := BuildStatusEvent(k, "ready-guard1", "done", "", reason, 1_700_000_100)
	if err != nil {
		t.Fatalf("BuildStatusEvent: %v", err)
	}
	if v := scanForContentHash(se.Tags, map[string]string{"reason": reason}); len(v) != 0 {
		t.Fatalf("status event carries a content hash: %v", v)
	}
}

// TestContentHashScannerIsNonVacuous proves the detector actually fires on a
// smuggled hash — under a forbidden key AND under an innocuous key in both hex
// and base64 — and stays quiet on clean tags. Without this, a broken scanner
// would let the two guardrail tests above pass vacuously.
func TestContentHashScannerIsNonVacuous(t *testing.T) {
	title := "some secret title"
	sum := sha256.Sum256([]byte(title))
	hexSum := hex.EncodeToString(sum[:])
	b64Sum := base64.StdEncoding.EncodeToString(sum[:])
	freeText := map[string]string{"title": title}

	fireCases := []struct {
		name string
		tags [][]string
	}{
		{"forbidden-key", [][]string{{"d", "ready-x"}, {"content_hash", hexSum}}},
		{"innocuous-key-hex", [][]string{{"d", "ready-x"}, {"ref", hexSum}}},
		{"innocuous-key-b64", [][]string{{"d", "ready-x"}, {"ref", b64Sum}}},
	}
	for _, tc := range fireCases {
		if v := scanForContentHash(tc.tags, freeText); len(v) == 0 {
			t.Fatalf("case %q: scanner did NOT catch smuggled hash — guardrail is vacuous", tc.name)
		}
	}

	// Clean tags (including a plausible HMAC-tokenized label that is NOT a
	// free-text digest) must NOT fire.
	clean := [][]string{{"d", "ready-x"}, {"l", "0badc0ffee1122334455667788990011223344556677889900aabbccddeeff00"}, {"title", title}}
	if v := scanForContentHash(clean, freeText); len(v) != 0 {
		t.Fatalf("scanner false-positived on clean tags: %v", v)
	}
}
