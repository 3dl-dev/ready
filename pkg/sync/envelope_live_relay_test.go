package sync

// Live end-to-end proof for the encrypted write path (ready-e63). Gated on
// RD_NOSTR_LIVE_RELAY=1 with an allowlisted key (RD_NOSTR_TEST_SECRET_HEX /
// RD_NOSTR_TEST_KEY_PATH / ~/.cf/nostr-identity.json). It publishes a
// confidential card to a real strfry relay and REQs it back as a non-member,
// proving: (1) the stored Content is opaque, the title tag is absent, and the
// enc/cek_epoch markers are present; (2) a #d-filtered REQ still matches (clear
// routing tags untouched); (3) a CEK holder recovers the plaintext.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

func TestLiveRelayEncryptedCard(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable, allowlisted strfry relay) to run the encrypted-card live proof")
	}
	relay := os.Getenv("RD_NOSTR_RELAY_URL")
	if relay == "" {
		t.Skip("set RD_NOSTR_RELAY_URL to the relay ws:// URL")
	}
	k := liveRelayKey(t)

	var cek [32]byte
	for i := range cek {
		cek[i] = byte(i*7 + 3)
	}
	var ltk [32]byte
	for i := range ltk {
		ltk[i] = byte(i*5 + 11)
	}
	env := &Envelope{CEK: cek, Epoch: 1, LTK: &ltk}

	itemID := fmt.Sprintf("ready-enc-live-%d", time.Now().UnixNano())
	title := "CONFIDENTIAL rotate the leaked pager secret"
	desc := "the pager secret leaked in a screenshot, rotate and audit"
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{
		ItemID: itemID, Title: title, Status: state.StatusActive, Priority: "p1",
		Type: "task", Context: desc, BoardD: "ready", Labels: []string{"security"},
		WaitingOn: "ready-blocker", Enc: env,
	}
	now := time.Now().Unix()

	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(t.TempDir(), ".ready", NostrLogFile)),
		WriteRelays: []string{relay},
		PendingPath: filepath.Join(t.TempDir(), ".ready", NostrPendingFile),
	}
	res, err := pub.PublishItem(context.Background(), &board, card, now)
	if err != nil {
		t.Fatalf("publish encrypted card: %v", err)
	}
	for _, ev := range res.Events {
		if !ev.AnyRelay {
			t.Fatalf("event kind %d reached no relay (allowlist? acks=%+v)", ev.Kind, ev.Acks)
		}
	}
	time.Sleep(1 * time.Second)

	// Non-member raw REQ by clear routing filter — proves the clear tags still
	// select the card AND that the stored payload is opaque.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := nostr.FetchMany(ctx, relay, map[string]any{
		"kinds": []int{KindCard}, "authors": []string{k.PubKeyHex()}, "#d": []string{itemID},
	})
	if err != nil {
		t.Fatalf("REQ: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("clear #d REQ returned no card — clear-tag filtering broke")
	}
	ev := got[0]

	if _, ok := findTag(ev.Tags, "title"); ok {
		t.Fatal("relay-stored confidential card carries a clear title tag")
	}
	if v, _ := findTag(ev.Tags, "enc"); v != "1" {
		t.Fatalf("relay-stored enc marker = %q, want \"1\"", v)
	}
	if strings.Contains(ev.Content, title) || strings.Contains(ev.Content, desc) {
		t.Fatalf("relay-stored Content leaks plaintext: %q", ev.Content)
	}
	if _, err := base64.StdEncoding.DecodeString(ev.Content); err != nil {
		t.Fatalf("relay-stored Content is not opaque base64: %v", err)
	}
	// A label tag is present but tokenized, not the plaintext "security".
	if v, ok := findTag(ev.Tags, "l"); !ok || v == "security" {
		t.Fatalf("label tag = %q (present=%v): expected a token, not plaintext", v, ok)
	}

	// Member view: the CEK holder recovers the exact plaintext.
	pt, err := openContent(cek, ev.Content)
	if err != nil {
		t.Fatalf("member openContent: %v", err)
	}
	var pl cardPayload
	if err := json.Unmarshal(pt, &pl); err != nil {
		t.Fatalf("unmarshal recovered payload: %v", err)
	}
	if pl.Title != title || pl.Context != desc || pl.WaitingOn != "ready-blocker" {
		t.Fatalf("member did not recover exact plaintext: %+v", pl)
	}
	t.Logf("LIVE PROOF: relay %s stored opaque Content (%d b64 bytes), clear #d filter matched, member recovered plaintext", relay, len(ev.Content))
}
