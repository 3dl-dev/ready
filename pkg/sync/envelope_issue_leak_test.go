package sync

// Regression guard for the kind:1621 issue-event confidentiality leak (ready-67c,
// found by the ready-216 adversarial security review). On a confidential board the
// NIP-34 issue event (subject=title, Content=description) must NOT be published —
// it would leak the two most sensitive free-text fields the card seals.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

func TestConfidentialItemPublishesNoPlaintextIssueEvent(t *testing.T) {
	k := testKey(t)
	var cek [32]byte
	for i := range cek {
		cek[i] = byte(i + 3)
	}
	env := &Envelope{CEK: cek, Epoch: 1}
	title := "SECRET rotate the leaked signing key"
	desc := "the signing key leaked in a paste; rotate and audit immediately"

	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{
		ItemID: "ready-leak1", Title: title, Context: desc, Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: "ready", Enc: env,
	}

	pub := &Publisher{Key: k, Log: NewNostrLog(filepath.Join(t.TempDir(), ".ready", NostrLogFile))}
	if _, err := pub.PublishItem(context.Background(), &board, card, 1_700_000_000); err != nil {
		t.Fatalf("publish confidential item: %v", err)
	}
	// Also exercise the status-change path (its own ensureIssueEvent call).
	if _, err := pub.PublishStatusChange(context.Background(), card, "sealed close reason", 1_700_000_100); err != nil {
		t.Fatalf("publish status change: %v", err)
	}

	events, err := pub.Log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events published")
	}
	for _, e := range events {
		if e.Kind == KindIssue {
			t.Fatalf("confidential item published a kind:1621 issue event (id=%s) — plaintext leak vector", e.ID)
		}
		// Belt-and-suspenders: NO published event may carry the plaintext title or
		// description in a clear tag value or in Content.
		for _, tg := range e.Tags {
			for _, v := range tg {
				if v == title || v == desc {
					t.Fatalf("event kind %d leaks plaintext free text in a clear tag: %v", e.Kind, tg)
				}
			}
		}
		if strings.Contains(e.Content, title) || strings.Contains(e.Content, desc) {
			t.Fatalf("event kind %d leaks plaintext free text in Content: %q", e.Kind, e.Content)
		}
	}

	// Regression guard the OTHER way: a PLAINTEXT item STILL gets its kind:1621
	// interop anchor (the fix must not over-suppress on normal boards).
	pub2 := &Publisher{Key: k, Log: NewNostrLog(filepath.Join(t.TempDir(), ".ready", NostrLogFile))}
	plainCard := CardSpec{ItemID: "ready-plain1", Title: "public title", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready"}
	if _, err := pub2.PublishItem(context.Background(), &board, plainCard, 1_700_000_000); err != nil {
		t.Fatalf("publish plaintext item: %v", err)
	}
	ev2, _ := pub2.Log.ReadAll()
	hasIssue := false
	for _, e := range ev2 {
		if e.Kind == KindIssue {
			hasIssue = true
		}
	}
	if !hasIssue {
		t.Fatal("plaintext item lost its kind:1621 issue event — the fix over-suppressed on a normal board")
	}
}
