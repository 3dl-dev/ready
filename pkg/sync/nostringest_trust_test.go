// Deterministic ingestion-seam proof for ready-5fd: rd refuses a non-trusted
// author's event AT INGESTION, reading its trust set from rdconfig.TrustedPubkeys.
//
// WHY THIS TEST EXISTS. The shared strfry relays used to carry a portfolio-wide
// write-allowlist writePolicy (ready-266) that rejected any non-admitted pubkey at
// the relay edge. That fence was REMOVED (ready-5fd, scripts/unlock-relays.sh): a
// relay is a dumb pipe, and trust belongs in the product. Probed 2026-07-30, the LAN
// strfry relays (ws://192.168.2.40:7777, ws://192.168.2.41:7777) accept a write from
// a freshly generated, never-granted key. So NOTHING upstream of rd stops a foreign
// key's events from being SERVED to rd — the client-side gate in reconcile() is the
// whole defence for those relays, not a redundant second layer.
//
// The gate itself was already covered at PROJECTION (nostrtrust_test.go), at the
// git-JSONL degrade floor (TestMergeFrom_TrustGate_RejectsUntrustedAuthor) and at
// negentropy admission (TestAdmitDownloaded_TrustGate). The RELAY ingestion seam —
// the one that actually replaced the removed edge fence, and the path every
// `rd ready` auto-reconcile takes — was covered ONLY by the env-gated live-relay
// test (TestLiveRelay_TrustGate), i.e. not at all in CI. That is the hole this
// closes.
//
// TWO THINGS ARE PINNED HERE, not one:
//  1. an untrusted author's validly-signed event served by an OPEN relay never
//     reaches the local authoritative log; and
//  2. the set that decides this is derived from rdconfig.Config.TrustedPubkeys —
//     adding the same key to that field, and changing nothing else, admits it.
//
// The relay is an in-process NIP-01 stub that serves EVERY event it holds regardless
// of the filter. That is not a stand-in for the code under test (rd's gate) — it is
// the adversarial input source, modelled maximally permissive on purpose: a hostile
// or merely unfenced relay ignores filters and serves foreign-authored events.
package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
)

// openRelay starts an in-process NIP-01 relay that answers every REQ with all of
// `events` (filter ignored) followed by EOSE. It models an UNFENCED relay: it
// happily serves events authored by keys rd has never admitted.
func openRelay(t *testing.T, events []*nostr.Event) string {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if err := json.Unmarshal(data, &frame); err != nil || len(frame) < 2 {
				return
			}
			var typ, sub string
			_ = json.Unmarshal(frame[0], &typ)
			_ = json.Unmarshal(frame[1], &sub)
			if typ != "REQ" {
				continue // CLOSE and anything else: nothing to serve.
			}
			for _, e := range events {
				payload, merr := json.Marshal([]any{"EVENT", sub, e})
				if merr != nil {
					t.Errorf("stub relay: marshal EVENT: %v", merr)
					return
				}
				if werr := conn.WriteMessage(websocket.TextMessage, payload); werr != nil {
					return
				}
			}
			eose, _ := json.Marshal([]any{"EOSE", sub})
			if werr := conn.WriteMessage(websocket.TextMessage, eose); werr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// logHasAuthor reports whether any event in the local log was authored by pubkey.
func logHasAuthor(t *testing.T, log *NostrLog, pubkey string) bool {
	t.Helper()
	evs, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read local log: %v", err)
	}
	for _, e := range evs {
		if e.PubKey == pubkey {
			return true
		}
	}
	return false
}

// TestReconcile_TrustGate_RefusesUntrustedAuthorFromOpenRelay is the ready-5fd
// done condition. An open relay serves three validly-signed cards for the pinned
// board: one from self, one from a key listed in rdconfig.Config.TrustedPubkeys,
// and one from an intruder key listed nowhere. Reconcile must merge the first two
// and REFUSE the third — it must never reach the local authoritative log, and it
// must never project into an item.
//
// The trust set is built exactly as production builds it (cmd/rd/nostr.go's
// nostrTrustSet → rdconfig.Config.TrustSet), so this pins the config field as the
// decision input, not a hand-rolled map that happens to agree with it.
func TestReconcile_TrustGate_RefusesUntrustedAuthorFromOpenRelay(t *testing.T) {
	self := mustKey(t)
	admitted := mustKey(t)
	intruder := mustKey(t)
	for _, pair := range [][2]string{
		{self.PubKeyHex(), admitted.PubKeyHex()},
		{self.PubKeyHex(), intruder.PubKeyHex()},
		{admitted.PubKeyHex(), intruder.PubKeyHex()},
	} {
		if pair[0] == pair[1] {
			t.Fatalf("test keys collided: %s", pair[0])
		}
	}

	boardD := "ready"
	coord := BoardCoord(self.PubKeyHex(), boardD)
	card := func(k *nostr.Key, itemID, title string, ts int64) *nostr.Event {
		t.Helper()
		e, err := BuildCardEvent(k, CardSpec{
			ItemID: itemID, Title: title, Status: state.StatusActive,
			Priority: "p1", Type: "task", BoardD: boardD, BoardAuthor: self.PubKeyHex(),
		}, ts)
		if err != nil {
			t.Fatalf("build card for %s: %v", itemID, err)
		}
		// Precondition: the intruder's event is not junk — it is a real, correctly
		// signed nostr event. Only AUTHORSHIP separates it from the admitted ones,
		// so a rejection can only be the trust gate's doing.
		if verr := e.Verify(); verr != nil {
			t.Fatalf("precondition: %s card must verify: %v", itemID, verr)
		}
		return e
	}

	selfCard := card(self, "ready-5fd-self", "self item", 1700000000)
	admittedCard := card(admitted, "ready-5fd-admitted", "admitted item", 1700000010)
	intruderCard := card(intruder, "ready-5fd-intruder", "INTRUDER item", 1700000020)

	relay := openRelay(t, []*nostr.Event{selfCard, admittedCard, intruderCard})

	// --- The gate: trust set derived from rdconfig, intruder absent from it. ---
	cfg := &rdconfig.Config{TrustedPubkeys: []string{admitted.PubKeyHex()}}
	trusted := cfg.TrustSet(self.PubKeyHex())
	if trusted[intruder.PubKeyHex()] {
		t.Fatalf("precondition: intruder must NOT be in the rdconfig-derived trust set")
	}

	log := NewNostrLog(t.TempDir() + "/nostr-log.jsonl")
	res, err := ReconcileBoard(context.Background(), []string{relay}, log, coord, trusted, 5*time.Second)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(res.RelayErrors) != 0 {
		t.Fatalf("stub relay errored, so nothing was actually ingested: %v", res.RelayErrors)
	}
	if res.Fetched != 2 || res.Added != 2 {
		t.Errorf("reconcile admitted %d/%d (fetched/added) of 3 served events, want 2/2 — self + the rdconfig-admitted key only", res.Fetched, res.Added)
	}

	// (1) The intruder's event must not be in the local authoritative log AT ALL.
	if logHasAuthor(t, log, intruder.PubKeyHex()) {
		t.Errorf("an untrusted author's event was merged into the local authoritative log — with the relay edge unfenced (ready-5fd) this gate is the ONLY thing standing between an open relay and rd's source of truth")
	}
	// The legitimate events did land — the gate is not simply rejecting everything.
	if !logHasAuthor(t, log, self.PubKeyHex()) {
		t.Errorf("self-authored event was not ingested")
	}
	if !logHasAuthor(t, log, admitted.PubKeyHex()) {
		t.Errorf("an rdconfig.TrustedPubkeys-admitted author's event was not ingested — the config field does not admit")
	}

	// (2) And it must not project into a phantom item either.
	evs, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	items := ProjectItems(evs, ProjectOptions{Trusted: trusted, PinnedBoard: coord})
	if _, ok := items["ready-5fd-intruder"]; ok {
		t.Errorf("untrusted author produced a projected item")
	}
	if _, ok := items["ready-5fd-self"]; !ok {
		t.Errorf("self item missing from the projection")
	}
	if _, ok := items["ready-5fd-admitted"]; !ok {
		t.Errorf("rdconfig-admitted author's item missing from the projection")
	}

	// --- POSITIVE CONTROL: rdconfig.TrustedPubkeys is what decides. ---
	// Same relay, same three events, same everything — only the config field gains
	// the intruder's pubkey. If it now merges, the earlier refusal was the trust
	// gate reading that field, not some incidental rejection (bad filter, kind
	// mismatch, board pin, dedup).
	cfg2 := &rdconfig.Config{TrustedPubkeys: []string{admitted.PubKeyHex(), intruder.PubKeyHex()}}
	log2 := NewNostrLog(t.TempDir() + "/nostr-log.jsonl")
	res2, err := ReconcileBoard(context.Background(), []string{relay}, log2, coord, cfg2.TrustSet(self.PubKeyHex()), 5*time.Second)
	if err != nil {
		t.Fatalf("reconcile (admitted intruder): %v", err)
	}
	if res2.Added != 3 {
		t.Errorf("after adding the same key to rdconfig.TrustedPubkeys, reconcile merged %d event(s), want 3 — the refusal above was not (only) the trust gate", res2.Added)
	}
	if !logHasAuthor(t, log2, intruder.PubKeyHex()) {
		t.Errorf("key admitted via rdconfig.TrustedPubkeys still refused at ingestion")
	}
}

// TestReconcile_TrustGate_EmptyConfigIsSelfOnly pins the fail-closed default that
// the removed relay fence used to backstop: a single-machine install with NO
// trusted_pubkeys entries trusts only its own key. An unfenced relay serving a
// foreign key's events into such an install must change nothing locally.
func TestReconcile_TrustGate_EmptyConfigIsSelfOnly(t *testing.T) {
	self := mustKey(t)
	foreign := mustKey(t)
	boardD := "ready"
	coord := BoardCoord(self.PubKeyHex(), boardD)

	foreignCard, err := BuildCardEvent(foreign, CardSpec{
		ItemID: "ready-5fd-foreign", Title: "foreign", Status: state.StatusActive,
		Priority: "p0", Type: "task", BoardD: boardD, BoardAuthor: self.PubKeyHex(),
	}, 1700000000)
	if err != nil {
		t.Fatalf("build foreign card: %v", err)
	}
	relay := openRelay(t, []*nostr.Event{foreignCard})

	// An EMPTY TrustedPubkeys list — the default rd.json — must not mean "trust
	// everything". TrustSet returns a non-nil set containing only self, and a
	// non-nil set ENFORCES.
	trusted := (&rdconfig.Config{}).TrustSet(self.PubKeyHex())

	log := NewNostrLog(t.TempDir() + "/nostr-log.jsonl")
	res, err := ReconcileBoard(context.Background(), []string{relay}, log, coord, trusted, 5*time.Second)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(res.RelayErrors) != 0 {
		t.Fatalf("stub relay errored: %v", res.RelayErrors)
	}
	if res.Added != 0 {
		t.Errorf("empty trusted_pubkeys admitted %d foreign event(s) — an unconfigured install must be self-only, not open", res.Added)
	}
	evs, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("local authoritative log has %d event(s) after a foreign-only reconcile, want 0", len(evs))
	}
}
