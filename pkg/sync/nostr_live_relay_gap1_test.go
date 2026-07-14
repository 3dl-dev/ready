package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// TestLiveRelay_GrantPropagatesToDerivedTrust is the live half of the GAP-1
// (ready-7c1) proof: an owner-signed role-grant, published to a LIVE strfry relay,
// negentropy-syncs onto a FRESH second log (a second machine) and there feeds
// grant-derived read-trust — DeriveReadTrust over the synced log admits the granted
// contributor. This exercises the whole wire path the deterministic tests stub:
//
//	owner publishes kind-39301 grant  ->  relay stores it (KindRoleGrant now rides the
//	board sync filter)  ->  machine B negentropy-syncs  ->  the grant lands in logB  ->
//	DeriveReadTrust(logB, owner, boardD) includes the contributor.
//
// The contributor key is fresh and never writes, so no relay allowlisting of it is
// needed — the point is that the owner's ONE signed grant is sufficient to admit it,
// cross-machine, which is exactly what GAP-1 delivers. Gated behind
// RD_NOSTR_LIVE_RELAY=1 with an allowlisted owner key (the relays reject non-admitted
// authors, ready-266).
func TestLiveRelay_GrantPropagatesToDerivedTrust(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the live grant-propagation proof")
	}
	relay := liveRelayURL(t)
	t.Logf("live relay: %s", relay)

	owner := liveRelayKey(t) // allowlisted — may write to the locked relays.
	contributor := testKey(t) // fresh key, never granted before this run, never writes.
	const boardD = "ready"
	boardCoord := BoardCoord(owner.PubKeyHex(), boardD)

	dir := t.TempDir()
	logA := NewNostrLog(filepath.Join(dir, "A", NostrLogFile))
	logB := NewNostrLog(filepath.Join(dir, "B", NostrLogFile))
	pubA := &Publisher{Key: owner, Log: logA, WriteRelays: []string{relay}, PendingPath: filepath.Join(dir, "A", NostrPendingFile)}

	// Owner signs and publishes a contributor grant for the fresh key.
	grantEv, err := BuildRoleGrantEvent(owner, RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: owner.PubKeyHex(),
		Grantee:     contributor.PubKeyHex(),
		Role:        RoleContributor,
		Label:       "gap1-live-contributor",
	}, time.Now().Unix())
	if err != nil {
		t.Fatalf("build grant: %v", err)
	}

	ctx := context.Background()
	res, err := pubA.PublishEvents(ctx, []*nostr.Event{grantEv})
	if err != nil {
		t.Fatalf("publish grant: %v", err)
	}
	accepted := false
	for _, ev := range res.Events {
		if ev.AnyRelay {
			accepted = true
		}
	}
	if !accepted {
		t.Fatalf("relay did not accept the owner-signed grant (acks=%+v)", res.Events)
	}
	time.Sleep(1 * time.Second)

	// Machine B (fresh, empty log) negentropy-syncs the board. KindRoleGrant now rides
	// the board sync filter, so the grant is downloaded. The download trust gate admits
	// it because it is authored by the owner (bootstrap trust root); the contributor is
	// deliberately NOT in the trust set here.
	filter := BoardSyncFilter(boardCoord, nil)
	trust := map[string]bool{owner.PubKeyHex(): true}
	if _, err := NegentropySync(ctx, relay, logB, filter, trust, 30*time.Second); err != nil {
		t.Fatalf("B sync: %v", err)
	}

	eventsB, err := logB.ReadAll()
	if err != nil {
		t.Fatalf("read logB: %v", err)
	}
	// The grant event must have landed on machine B.
	sawGrant := false
	for _, e := range eventsB {
		if e.ID == grantEv.ID {
			sawGrant = true
		}
	}
	if !sawGrant {
		t.Fatalf("the owner-signed grant did NOT propagate to machine B over the relay (KindRoleGrant not synced); logB has %d events", len(eventsB))
	}

	// And it must feed grant-derived read-trust: the contributor is admitted purely by
	// the synced grant — no config, no prior trust.
	derived := DeriveReadTrust(eventsB, owner.PubKeyHex(), boardD)
	if !derived[contributor.PubKeyHex()] {
		t.Fatalf("GAP-1 live: the propagated grant did not admit the contributor to derived read-trust on machine B")
	}
	t.Logf("PROVEN (GAP-1 live): owner grant %s propagated to machine B and admitted contributor %s to grant-derived read-trust",
		grantEv.ID, contributor.PubKeyHex())
}
