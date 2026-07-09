package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	"github.com/campfire-net/ready/pkg/state"
)

func liveRelayURL(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("RD_NOSTR_RELAY_URL"); r != "" {
		return r
	}
	var cfg rdconfig.Config
	urls := cfg.WriteRelayURLs()
	if len(urls) == 0 {
		t.Fatal("no write relays configured")
	}
	return urls[0]
}

// TestLiveRelay_TwoMachineConvergence proves the machine<->relay convergence layer
// against a LIVE strfry relay, two independent local logs standing in for two
// machines that share one portfolio identity:
//
//	A creates item X (publish to relay + logA).  B creates item Y (relay + logB).
//	Each machine negentropy-syncs against the relay.
//	=> both logs replay to the SAME two items {X, Y} (convergence).
//	A second sync transfers ZERO event bodies (converged: the anti-fs-sync-
//	pathology measurement — no full re-sync, cost bounded by the empty diff).
//
// Gated behind RD_NOSTR_LIVE_RELAY=1.
func TestLiveRelay_TwoMachineConvergence(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the live two-machine convergence proof")
	}
	relay := liveRelayURL(t)
	t.Logf("live relay: %s", relay)

	// Shared portfolio identity across both machines (the multi-machine model).
	// Allowlisted portfolio key: the locked relays reject non-admitted authors (ready-266).
	k := liveRelayKey(t)
	run := time.Now().UnixNano()
	idX := fmt.Sprintf("ready-797-X-%d", run)
	idY := fmt.Sprintf("ready-797-Y-%d", run)

	dir := t.TempDir()
	logA := NewNostrLog(filepath.Join(dir, "A", NostrLogFile))
	logB := NewNostrLog(filepath.Join(dir, "B", NostrLogFile))
	pubA := &Publisher{Key: k, Log: logA, WriteRelays: []string{relay}, PendingPath: filepath.Join(dir, "A", NostrPendingFile)}
	pubB := &Publisher{Key: k, Log: logB, WriteRelays: []string{relay}, PendingPath: filepath.Join(dir, "B", NostrPendingFile)}
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}

	ctx := context.Background()
	if _, err := pubA.PublishItem(ctx, &board, CardSpec{ItemID: idX, Title: "X", Status: state.StatusActive, Type: "task", BoardD: "ready"}, time.Now().Unix()); err != nil {
		t.Fatalf("A publish X: %v", err)
	}
	if _, err := pubB.PublishItem(ctx, &board, CardSpec{ItemID: idY, Title: "Y", Status: state.StatusActive, Type: "task", BoardD: "ready"}, time.Now().Unix()); err != nil {
		t.Fatalf("B publish Y: %v", err)
	}
	time.Sleep(1 * time.Second)

	filter := BoardSyncFilter("", []string{k.PubKeyHex()})

	// Each machine syncs against the relay.
	rA, err := NegentropySync(ctx, relay, logA, filter, map[string]bool{k.PubKeyHex(): true}, 30*time.Second)
	if err != nil {
		t.Fatalf("A sync: %v", err)
	}
	rB, err := NegentropySync(ctx, relay, logB, filter, map[string]bool{k.PubKeyHex(): true}, 30*time.Second)
	if err != nil {
		t.Fatalf("B sync: %v", err)
	}
	t.Logf("A sync: local_before=%d need=%d have=%d downloaded=%d uploaded=%d neg(sent=%dB recv=%dB rounds=%d) eventBytes(down=%d up=%d)",
		rA.LocalBefore, rA.Need, rA.Have, rA.Downloaded, rA.Uploaded, rA.BytesSent, rA.BytesReceived, rA.RoundTrips, rA.EventBytesDownloaded, rA.EventBytesUploaded)
	t.Logf("B sync: local_before=%d need=%d have=%d downloaded=%d uploaded=%d neg(sent=%dB recv=%dB rounds=%d) eventBytes(down=%d up=%d)",
		rB.LocalBefore, rB.Need, rB.Have, rB.Downloaded, rB.Uploaded, rB.BytesSent, rB.BytesReceived, rB.RoundTrips, rB.EventBytesDownloaded, rB.EventBytesUploaded)

	// Convergence: both logs must replay to BOTH items with identical status.
	assertConverged(t, "A", logA, idX, idY)
	assertConverged(t, "B", logB, idX, idY)

	// Anti-pathology: a SECOND sync on a converged machine transfers ZERO event
	// bodies (no full re-sync). This is the measured proof vs campfire's 44x.
	rA2, err := NegentropySync(ctx, relay, logA, filter, map[string]bool{k.PubKeyHex(): true}, 30*time.Second)
	if err != nil {
		t.Fatalf("A resync: %v", err)
	}
	t.Logf("A RE-sync (converged): need=%d have=%d downloaded=%d uploaded=%d neg(sent=%dB recv=%dB) eventBytes(down=%d up=%d)",
		rA2.Need, rA2.Have, rA2.Downloaded, rA2.Uploaded, rA2.BytesSent, rA2.BytesReceived, rA2.EventBytesDownloaded, rA2.EventBytesUploaded)
	if rA2.Downloaded != 0 || rA2.Uploaded != 0 || rA2.EventBytesDownloaded != 0 || rA2.EventBytesUploaded != 0 {
		t.Fatalf("converged re-sync must transfer ZERO event bodies, got down=%d up=%d (downBytes=%d upBytes=%d)",
			rA2.Downloaded, rA2.Uploaded, rA2.EventBytesDownloaded, rA2.EventBytesUploaded)
	}
	t.Logf("PROVEN: two machines converged via negentropy; converged re-sync moved 0 event bytes (no fs-sync re-sync pathology)")
}

// TestLiveRelay_OfflineFlushIdempotent proves the offline buffer path: an event
// authored while relays are unreachable buffers locally, then flushes on
// reconnect, and re-publishing the same event id is idempotent (relay dedupes).
func TestLiveRelay_OfflineFlushIdempotent(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the offline-flush proof")
	}
	relay := liveRelayURL(t)

	// Allowlisted portfolio key: the locked relays reject non-admitted authors (ready-266).
	k := liveRelayKey(t)
	dir := t.TempDir()
	log := NewNostrLog(filepath.Join(dir, NostrLogFile))
	pendingPath := filepath.Join(dir, NostrPendingFile)
	deadRelay := "ws://127.0.0.1:1" // guaranteed unreachable

	// Author while "offline": publish to a dead relay. Event lands in the log AND
	// the pending buffer.
	pub := &Publisher{Key: k, Log: log, WriteRelays: []string{deadRelay}, PendingPath: pendingPath}
	id := fmt.Sprintf("ready-797-offline-%d", time.Now().UnixNano())
	res, err := pub.PublishItem(context.Background(), nil, CardSpec{ItemID: id, Title: "offline", Status: state.StatusActive, Type: "task", BoardD: "ready"}, time.Now().Unix())
	if err != nil {
		t.Fatalf("offline publish: %v", err)
	}
	if !res.Buffered {
		t.Fatal("expected events to be buffered when the only relay is unreachable")
	}
	buffered, _ := readPendingEvents(pendingPath)
	if len(buffered) == 0 {
		t.Fatal("pending buffer should be non-empty offline")
	}
	t.Logf("offline: %d events buffered, all durable in the local log", len(buffered))

	// Reconnect: flush to the LIVE relay.
	fr, err := FlushNostrPending(context.Background(), pendingPath, []string{relay}, 30*time.Second)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	t.Logf("flush#1: total=%d flushed=%d remaining=%d", fr.Total, fr.Flushed, fr.Remaining)
	if fr.Flushed != fr.Total || fr.Remaining != 0 {
		t.Fatalf("flush should drain the buffer: flushed=%d total=%d remaining=%d", fr.Flushed, fr.Total, fr.Remaining)
	}

	// Idempotency by event id: re-publishing the SAME events must still be accepted
	// (relay dedupes, OK,true) — prove by direct republish of the buffered events.
	for _, e := range buffered {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		accepted, msg, perr := nostr.Publish(ctx, relay, e)
		cancel()
		if perr != nil {
			t.Fatalf("republish %s: %v", e.ID, perr)
		}
		if !accepted {
			t.Fatalf("relay rejected idempotent republish of %s: %q", e.ID, msg)
		}
	}
	t.Logf("PROVEN: offline events buffered + flushed on reconnect; republish idempotent by event id (relay OK,true)")
}

// TestLiveRelay_NegentropyDownloadTrustGate is the ready-b57 LIVE proof for fix (1),
// download path: an event the relay legitimately serves is REJECTED from the local
// authoritative log when its author is NOT in the syncing machine's web-of-trust set
// — the client-side gate stands even against an honest relay, so it also stands
// against a permissive/hostile one that ignores the write-allowlist (ready-266).
//
// The locked relays reject non-allowlisted WRITES, so we cannot place a
// genuinely-foreign event on the relay. We invert the roles instead: publish item X
// with the ALLOWLISTED key (accepted + stored), then have a fresh machine negentropy-
// sync with a trust set that DOES NOT include that author. The relay serves X over
// the download, and the gate must drop it before AppendUnique — proving the download
// admission is gated on the SYNCING machine's trust set, not on what the relay serves.
// Re-syncing with the author admitted then downloads X, proving the gate was the only
// thing blocking it.
func TestLiveRelay_NegentropyDownloadTrustGate(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the live negentropy-download trust-gate proof")
	}
	relay := liveRelayURL(t)
	k := liveRelayKey(t) // allowlisted author — its writes are accepted by the relay

	// A pubkey the syncing machine does NOT trust (never authored anything here).
	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen stranger: %v", err)
	}

	dir := t.TempDir()
	logPub := NewNostrLog(filepath.Join(dir, "pub", NostrLogFile))
	pub := &Publisher{Key: k, Log: logPub, WriteRelays: []string{relay}, PendingPath: filepath.Join(dir, "pub", NostrPendingFile)}
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	idX := fmt.Sprintf("ready-b57-dl-%d", time.Now().UnixNano())
	if _, err := pub.PublishItem(context.Background(), &board, CardSpec{ItemID: idX, Title: "X", Status: state.StatusActive, Type: "task", BoardD: "ready"}, time.Now().Unix()); err != nil {
		t.Fatalf("publish X: %v", err)
	}
	time.Sleep(1 * time.Second)

	// A fresh machine with an EMPTY log syncs, requesting k's events, but trusts only
	// `stranger` — NOT k. The relay serves X; the download gate must drop every event.
	syncLog := NewNostrLog(filepath.Join(dir, "sync", NostrLogFile))
	filter := BoardSyncFilter("", []string{k.PubKeyHex()})
	untrusting := map[string]bool{stranger.PubKeyHex(): true}
	r, err := NegentropySync(context.Background(), relay, syncLog, filter, untrusting, 30*time.Second)
	if err != nil {
		t.Fatalf("gated sync: %v", err)
	}
	if r.Need == 0 {
		t.Fatalf("relay served nothing for %s — cannot distinguish the gate from an empty fetch", idX)
	}
	if r.Downloaded != 0 {
		t.Fatalf("TRUST GATE FAILED: downloaded %d untrusted-author event(s) into the log", r.Downloaded)
	}
	got, err := syncLog.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("untrusted events poisoned the local log via negentropy download: %d present", len(got))
	}
	t.Logf("PROVEN: relay offered need=%d event(s); ZERO admitted under a trust set excluding the author", r.Need)

	// Admit k -> the very same events now download. Proves the gate (not the relay,
	// not the filter) was the sole blocker.
	trusting := map[string]bool{k.PubKeyHex(): true}
	r2, err := NegentropySync(context.Background(), relay, syncLog, filter, trusting, 30*time.Second)
	if err != nil {
		t.Fatalf("trusting sync: %v", err)
	}
	if r2.Downloaded == 0 {
		t.Fatalf("once the author is admitted, its events must download, got downloaded=0 (need=%d)", r2.Need)
	}
	items := ProjectItems(mustReadAll(t, syncLog), ProjectOptions{Trusted: trusting})
	if items[idX] == nil {
		t.Fatalf("admitted author's item %s missing after trusted sync", idX)
	}
	t.Logf("PROVEN: same events admitted once the author is trusted (downloaded=%d)", r2.Downloaded)
}

func mustReadAll(t *testing.T, log *NostrLog) []*nostr.Event {
	t.Helper()
	evs, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return evs
}

func assertConverged(t *testing.T, who string, log *NostrLog, ids ...string) {
	t.Helper()
	evs, err := log.ReadAll()
	if err != nil {
		t.Fatalf("[%s] read log: %v", who, err)
	}
	items := ProjectItems(evs, ProjectOptions{})
	for _, id := range ids {
		if items[id] == nil {
			t.Errorf("[%s] did not converge: missing item %s (has %d items)", who, id, len(items))
		}
	}
}
