package sync

// End-to-end label-tokenization test (ready-c83, ruling (b)). Proves the full
// outcome: a confidential board's labels are HMAC tokens at rest (a non-member REQ
// cannot read them), while a granted member's projection renders the human labels
// and `rd list --label X` matches. rd filters labels CLIENT-SIDE (cmd/rd/list.go
// applies views.LabelFilter over the projected Item.Labels — it never pushes an #l
// filter to the relay, it syncs whole boards by coordinate), so there is no
// relay-side tokenize-before-REQ to do: the token protects the AT-REST relay
// representation, and querying works on the member's decrypted labels. See the
// frozen spec §7.

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// hasLabel mirrors views.LabelFilter / `rd list --label` exactly: membership of an
// atom in the projected item's Labels. Reproduced here to avoid a test-only import
// cycle; the logic is one line and identical to the CLI's.
func hasLabel(item *state.Item, atom string) bool {
	for _, l := range item.Labels {
		if l == atom {
			return true
		}
	}
	return false
}

func TestLabelTokenizationEndToEnd(t *testing.T) {
	owner := kdKey(t)
	member := kdKey(t)
	const boardD = "ready"
	boardCoord := BoardCoord(owner.PubKeyHex(), boardD)

	cek, _ := MintKey()
	ltk, _ := MintKey()
	env := &Envelope{CEK: cek, Epoch: 1, LTK: &ltk}

	// Owner grants the member: wraps BOTH the CEK and the LTK inside the signed grant.
	grant := kdGrant(t, owner, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: member.PubKeyHex(),
		Role:       RoleContributor,
		WrappedCEK: kdWrap(t, owner, member.PubKeyHex(), cek), CEKEpoch: 1,
		WrappedLTK: kdWrap(t, owner, member.PubKeyHex(), ltk),
	}, 1_700_000_000)

	board, _ := BuildBoardEvent(owner, BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{owner.PubKeyHex()}}, 1_699_000_000)
	spec := CardSpec{
		ItemID: "ready-tok1", Title: "secret", Status: state.StatusActive, Priority: "p1",
		Type: "task", BoardD: boardD, Context: "hidden", Labels: []string{"urgent", "security"}, Enc: env,
	}
	card, err := BuildCardEvent(owner, spec, 1_700_000_100)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}

	// AT REST: the card's l tags are HMAC tokens, not the plaintext labels.
	var lVals []string
	for _, tg := range card.Tags {
		if len(tg) >= 2 && tg[0] == "l" {
			lVals = append(lVals, tg[1])
		}
	}
	if len(lVals) != 2 {
		t.Fatalf("want 2 l tags, got %v", lVals)
	}
	for i, plain := range []string{"urgent", "security"} {
		if lVals[i] == plain {
			t.Fatalf("label %q is PLAINTEXT at rest on a confidential board", plain)
		}
		if lVals[i] != labelToken(ltk, plain) {
			t.Fatalf("l tag %d = %q, not the expected token", i, lVals[i])
		}
	}

	events := []*nostr.Event{board, grant, card}
	base := func(dec BoardDecryptor) ProjectOptions {
		return ProjectOptions{Trusted: map[string]bool{owner.PubKeyHex(): true}, PinnedBoard: boardCoord, Decryptor: dec}
	}

	// GRANTED MEMBER: keyring unwraps the CEK, projection renders human labels, and
	// `rd list --label urgent` (client-side LabelFilter) matches.
	kr := DeriveBoardKeyring(events, member, owner.PubKeyHex(), boardD)
	asMember := ProjectItems(events, base(kr))["ready-tok1"]
	if asMember == nil {
		t.Fatal("member: card missing from projection")
	}
	if !hasLabel(asMember, "urgent") || !hasLabel(asMember, "security") {
		t.Fatalf("member: rd list --label failed to match on decrypted labels: %v", asMember.Labels)
	}
	// The member also holds the LTK (for any future relay-side tokenized query).
	if l, ok := kr.LTK(boardCoord); !ok || l != ltk {
		t.Fatal("member keyring missing the LTK")
	}

	// NON-MEMBER: labels are opaque tokens, so a human-label filter cannot match.
	asNon := ProjectItems(events, base(nil))["ready-tok1"]
	if asNon == nil {
		t.Fatal("non-member: card missing from projection")
	}
	if hasLabel(asNon, "urgent") {
		t.Fatal("non-member matched a plaintext label — tokenization bypassed")
	}
	if len(asNon.Labels) != 2 {
		t.Fatalf("non-member: label count = %d, want 2 opaque tokens", len(asNon.Labels))
	}

	// CROSS-BOARD: the same label under a different board's LTK yields a different
	// token — tokens do not correlate across boards.
	var ltk2 [32]byte
	for i := range ltk2 {
		ltk2[i] = ltk[i] ^ 0xFF
	}
	if labelToken(ltk, "urgent") == labelToken(ltk2, "urgent") {
		t.Fatal("same label under different board LTKs produced the same token")
	}
}
