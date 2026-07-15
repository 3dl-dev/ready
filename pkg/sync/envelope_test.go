package sync

// Encrypted write-path tests (ready-e63). These assert on the BUILT event, which
// is byte-for-byte what a relay stores and returns from a raw REQ — so proving
// the wire shape here proves what a non-member REQ would see. A gated live-relay
// end-to-end variant lives in envelope_live_relay_test.go.

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// testEnvelope builds a deterministic injected Envelope. withLTK toggles label
// tokenization. The CEK/LTK bytes are fixed so tests are reproducible; real
// boards use crypto/rand keys distributed via the owner-signed grant (keydist).
func testEnvelope(epoch int, withLTK bool) *Envelope {
	var cek [32]byte
	for i := range cek {
		cek[i] = byte(i + 1)
	}
	env := &Envelope{CEK: cek, Epoch: epoch}
	if withLTK {
		var ltk [32]byte
		for i := range ltk {
			ltk[i] = byte(0xA0 + i)
		}
		env.LTK = &ltk
	}
	return env
}

// fullSpec is a fully-populated confidential-capable CardSpec (routing fields +
// free text + labels). enc is applied by the caller.
func fullSpec(assignee string) CardSpec {
	return CardSpec{
		ItemID:      "ready-enc1",
		Title:       "Rotate the leaked pager secret",
		Status:      state.StatusActive,
		Priority:    "p1",
		Assignee:    assignee,
		Type:        "task",
		Context:     "The pager secret leaked in a screenshot; rotate and audit access.",
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
}

// TestEncryptedCardWireShape: a confidential card drops the clear title/waiting_on
// tags, tokenizes labels, adds enc/cek_epoch markers, keeps every routing tag
// clear, and carries opaque base64 Content with no plaintext free-text leakage.
func TestEncryptedCardWireShape(t *testing.T) {
	k := testKey(t)
	env := testEnvelope(1, true)
	spec := fullSpec(k.PubKeyHex())
	spec.Enc = env

	ce, err := BuildCardEvent(k, spec, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}

	// (a) NO clear free-text tags.
	if v, ok := findTag(ce.Tags, "title"); ok {
		t.Fatalf("confidential card leaked a clear title tag: %q", v)
	}
	if v, ok := findTag(ce.Tags, "waiting_on"); ok {
		t.Fatalf("confidential card leaked a clear waiting_on tag: %q", v)
	}

	// (b) markers present and correct.
	if v, _ := findTag(ce.Tags, "enc"); v != "1" {
		t.Fatalf("enc marker = %q, want \"1\"", v)
	}
	if v, _ := findTag(ce.Tags, "cek_epoch"); v != "1" {
		t.Fatalf("cek_epoch marker = %q, want \"1\"", v)
	}

	// (c) every routing tag stays clear and readable.
	for _, want := range []struct{ key, val string }{
		{"d", "ready-enc1"},
		{"s", state.StatusActive},
		{"priority", "p1"},
		{"itype", "task"},
		{"p", k.PubKeyHex()},
		{"gate", "security"},
		{"waiting_type", "gate"},
		{"eta", "2026-07-18T00:00:00Z"},
		{"level", "1"},
		{"for", "baron@3dl.dev"},
		{"parent", "ready-parent"},
		{"due", "2026-07-17T00:00:00Z"},
	} {
		if v, ok := findTag(ce.Tags, want.key); !ok || v != want.val {
			t.Fatalf("routing tag %q = %q (present=%v), want %q", want.key, v, ok, want.val)
		}
	}

	// (d) labels are tokenized, not plaintext.
	var labelVals []string
	for _, tg := range ce.Tags {
		if len(tg) >= 2 && tg[0] == "l" {
			labelVals = append(labelVals, tg[1])
		}
	}
	if len(labelVals) != 2 {
		t.Fatalf("expected 2 l tags, got %d (%v)", len(labelVals), labelVals)
	}
	for i, plain := range []string{"security", "urgent"} {
		if labelVals[i] == plain {
			t.Fatalf("label %q emitted in PLAINTEXT on a confidential card", plain)
		}
		if want := labelToken(*env.LTK, plain); labelVals[i] != want {
			t.Fatalf("label token for %q = %q, want %q", plain, labelVals[i], want)
		}
	}

	// (e) Content is opaque base64 with no plaintext substring leakage.
	if strings.Contains(ce.Content, spec.Title) || strings.Contains(ce.Content, spec.Context) || strings.Contains(ce.Content, spec.WaitingOn) {
		t.Fatalf("Content leaks a plaintext free-text substring: %q", ce.Content)
	}
	raw, err := base64.StdEncoding.DecodeString(ce.Content)
	if err != nil {
		t.Fatalf("Content is not valid base64: %v", err)
	}
	if strings.Contains(string(raw), spec.Title) || strings.Contains(string(raw), "leaked") {
		t.Fatalf("decoded ciphertext leaks plaintext")
	}
}

// TestEncryptedCardRoundTrip: a holder of the CEK recovers the exact free text.
func TestEncryptedCardRoundTrip(t *testing.T) {
	k := testKey(t)
	env := testEnvelope(1, true)
	spec := fullSpec(k.PubKeyHex())
	spec.Enc = env

	ce, err := BuildCardEvent(k, spec, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	pt, err := openContent(env.CEK, ce.Content)
	if err != nil {
		t.Fatalf("openContent: %v", err)
	}
	var pl cardPayload
	if err := json.Unmarshal(pt, &pl); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if pl.Title != spec.Title {
		t.Fatalf("title: got %q want %q", pl.Title, spec.Title)
	}
	if pl.Context != spec.Context {
		t.Fatalf("context: got %q want %q", pl.Context, spec.Context)
	}
	if pl.WaitingOn != spec.WaitingOn {
		t.Fatalf("waiting_on: got %q want %q", pl.WaitingOn, spec.WaitingOn)
	}
	if strings.Join(pl.Labels, ",") != strings.Join(spec.Labels, ",") {
		t.Fatalf("labels: got %v want %v", pl.Labels, spec.Labels)
	}
	// Wrong CEK fails closed (no panic, error returned).
	var wrong [32]byte
	if _, err := openContent(wrong, ce.Content); err == nil {
		t.Fatal("openContent with wrong CEK unexpectedly succeeded")
	}
}

// TestClearTagStructuralParity: the relay-indexed ROUTING tags are byte-identical
// between plaintext and confidential mode. Only the free-text tags differ (title
// + waiting_on dropped, l tokenized, enc/cek_epoch added, Content sealed) — so
// latest-wins dedupe (keyed on d) and every REQ filter behave identically.
func TestClearTagStructuralParity(t *testing.T) {
	k := testKey(t)
	spec := fullSpec(k.PubKeyHex())

	plain, err := BuildCardEvent(k, spec, 1_700_000_000)
	if err != nil {
		t.Fatalf("plaintext BuildCardEvent: %v", err)
	}
	specEnc := spec
	specEnc.Enc = testEnvelope(1, true)
	enc, err := BuildCardEvent(k, specEnc, 1_700_000_000)
	if err != nil {
		t.Fatalf("encrypted BuildCardEvent: %v", err)
	}

	// The routing tags that dedupe/filtering depend on — everything except the
	// free-text-affected keys.
	freeTextKeys := map[string]bool{"title": true, "waiting_on": true, "l": true, "enc": true, "cek_epoch": true}
	routing := func(tags [][]string) [][]string {
		var out [][]string
		for _, tg := range tags {
			if len(tg) == 0 || freeTextKeys[tg[0]] {
				continue
			}
			out = append(out, tg)
		}
		return out
	}
	pr, er := routing(plain.Tags), routing(enc.Tags)
	if len(pr) != len(er) {
		t.Fatalf("routing tag COUNT differs: plaintext %d vs encrypted %d\n plain=%v\n enc=%v", len(pr), len(er), pr, er)
	}
	for i := range pr {
		if strings.Join(pr[i], "\x00") != strings.Join(er[i], "\x00") {
			t.Fatalf("routing tag %d differs: plaintext %v vs encrypted %v", i, pr[i], er[i])
		}
	}

	// The d dedupe key is identical (the addressable projection key is untouched).
	pd, _ := findTag(plain.Tags, "d")
	ed, _ := findTag(enc.Tags, "d")
	if pd != ed || pd == "" {
		t.Fatalf("d tag differs between modes: %q vs %q", pd, ed)
	}

	// Label STRUCTURE preserved: same count of l tags, same key, value tokenized.
	countL := func(tags [][]string) int {
		n := 0
		for _, tg := range tags {
			if len(tg) >= 1 && tg[0] == "l" {
				n++
			}
		}
		return n
	}
	if countL(plain.Tags) != countL(enc.Tags) {
		t.Fatalf("l tag count differs: %d vs %d", countL(plain.Tags), countL(enc.Tags))
	}
}

// TestEncryptedStatusEventRoundTrip: the live status path
// (BuildStatusEventWithIssueRoot) seals the reason, keeps clear routing tags, and
// round-trips under the CEK.
func TestEncryptedStatusEventRoundTrip(t *testing.T) {
	k := testKey(t)
	env := testEnvelope(1, false)
	reason := "Closed: rotated the secret and revoked the leaked grant."

	se, err := BuildStatusEventWithIssueRoot(k, "ready-enc1", state.StatusDone, "cardid", "", "", reason, 1_700_000_100, env)
	if err != nil {
		t.Fatalf("BuildStatusEventWithIssueRoot: %v", err)
	}
	if se.Content == reason {
		t.Fatal("status Content is the PLAINTEXT reason — not sealed")
	}
	if strings.Contains(se.Content, "rotated") {
		t.Fatalf("status Content leaks plaintext: %q", se.Content)
	}
	if v, _ := findTag(se.Tags, "enc"); v != "1" {
		t.Fatalf("status enc marker = %q, want \"1\"", v)
	}
	if v, _ := findTag(se.Tags, "cek_epoch"); v != "1" {
		t.Fatalf("status cek_epoch = %q, want \"1\"", v)
	}
	for _, key := range []string{"d", "a", "status"} {
		if _, ok := findTag(se.Tags, key); !ok {
			t.Fatalf("status event missing clear routing tag %q", key)
		}
	}
	pt, err := openContent(env.CEK, se.Content)
	if err != nil {
		t.Fatalf("openContent(status): %v", err)
	}
	var pl statusPayload
	if err := json.Unmarshal(pt, &pl); err != nil {
		t.Fatalf("unmarshal status payload: %v", err)
	}
	if pl.Reason != reason {
		t.Fatalf("reason: got %q want %q", pl.Reason, reason)
	}
}

// TestLabelTokenSemantics: equality-preserving under a fixed LTK, board-scoped
// (different LTK ⇒ different token), and never the plaintext.
func TestLabelTokenSemantics(t *testing.T) {
	var ltkA, ltkB [32]byte
	for i := range ltkA {
		ltkA[i] = byte(0xA0 + i)
		ltkB[i] = byte(0x0B + i)
	}
	if labelToken(ltkA, "urgent") != labelToken(ltkA, "urgent") {
		t.Fatal("same label + same LTK produced different tokens")
	}
	if labelToken(ltkA, "urgent") == labelToken(ltkB, "urgent") {
		t.Fatal("different LTK produced the SAME token — cross-board correlation")
	}
	if labelToken(ltkA, "urgent") == "urgent" {
		t.Fatal("token equals plaintext label")
	}
	if labelToken(ltkA, "urgent") == labelToken(ltkA, "security") {
		t.Fatal("different labels collided to the same token")
	}
}

// TestPlaintextModeUnchanged: with Enc=nil the card is the exact pre-existing
// shape — clear title/waiting_on/plaintext labels, plaintext Content, no markers.
func TestPlaintextModeUnchanged(t *testing.T) {
	k := testKey(t)
	spec := fullSpec(k.PubKeyHex())
	ce, err := BuildCardEvent(k, spec, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	if v, ok := findTag(ce.Tags, "title"); !ok || v != spec.Title {
		t.Fatalf("plaintext title tag = %q (present=%v), want %q", v, ok, spec.Title)
	}
	if v, ok := findTag(ce.Tags, "waiting_on"); !ok || v != spec.WaitingOn {
		t.Fatalf("plaintext waiting_on tag = %q (present=%v), want %q", v, ok, spec.WaitingOn)
	}
	if v, _ := findTag(ce.Tags, "l"); v != "security" {
		t.Fatalf("plaintext first label = %q, want \"security\"", v)
	}
	if ce.Content != spec.Context {
		t.Fatalf("plaintext Content = %q, want %q", ce.Content, spec.Context)
	}
	if _, ok := findTag(ce.Tags, "enc"); ok {
		t.Fatal("plaintext card unexpectedly carries an enc marker")
	}
}
