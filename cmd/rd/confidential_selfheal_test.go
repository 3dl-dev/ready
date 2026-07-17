package main

// Edge #5 self-heal integration test (ready-bd0): when a valid owner-signed grant
// carrying THIS pubkey's read key exists on a relay but has not reached the local
// log, a confidential write must fetch the grant and SEAL — instead of erroring
// "board is confidential and you hold no read key — ask the owner to grant your
// pubkey" (which tells the writer to do what the owner already did).
//
// The test drives the REAL boardConfidentialEnvelope write path against a REAL
// in-process NIP-01 relay (no mock of the code under test): a real owner key mints
// the board CEK and a real owner-signed 39301 grant for a real member key is
// published to the relay only. The member's local log is seeded with the board +
// owner self-grant (so the board is known-confidential locally = the scary-error
// precondition) but NOT the member's own grant, so the write must self-heal.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/gorilla/websocket"
)

// storingRelay is a minimal in-process NIP-01 relay that stores every EVENT it is
// sent and serves ALL stored events back on any REQ (then EOSE). Filters are
// ignored on purpose: the relay is an UNTRUSTED cache — correctness (owner
// signature, grantee binding, ECDH wrap opening) is enforced client-side by
// Verify + the reconcile trust gate + DeriveBoardKeyring, exactly as in prod.
type storingRelay struct {
	srv      *httptest.Server
	mu       sync.Mutex
	events   []*nostr.Event
	reqCount int
}

func newStoringRelay(t *testing.T) *storingRelay {
	t.Helper()
	r := &storingRelay{}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
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
			if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
				continue
			}
			var typ string
			_ = json.Unmarshal(frame[0], &typ)
			switch typ {
			case "EVENT":
				var ev nostr.Event
				if json.Unmarshal(frame[1], &ev) == nil {
					r.mu.Lock()
					e := ev
					r.events = append(r.events, &e)
					r.mu.Unlock()
					_ = conn.WriteJSON([]any{"OK", ev.ID, true, ""})
				}
			case "REQ":
				var sub string
				_ = json.Unmarshal(frame[1], &sub)
				r.mu.Lock()
				r.reqCount++
				snap := append([]*nostr.Event(nil), r.events...)
				r.mu.Unlock()
				for _, e := range snap {
					_ = conn.WriteJSON([]any{"EVENT", sub, e})
				}
				_ = conn.WriteJSON([]any{"EOSE", sub})
			case "CLOSE":
				// keep the connection open for further REQs
			}
		}
	}))
	return r
}

func (r *storingRelay) url() string { return "ws" + strings.TrimPrefix(r.srv.URL, "http") }
func (r *storingRelay) close()      { r.srv.Close() }
func (r *storingRelay) reqs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqCount
}

// selfHealFixture stands up an owner machine on a live in-process relay: a
// confidential board bootstrapped by the owner (CEK minted, owner self-grant on the
// relay + owner log). It returns the pieces a member machine needs to reproduce
// edge #5.
type selfHealFixture struct {
	relay      *storingRelay
	base       string
	boardD     string
	coord      string
	owner      *nostr.Key
	ownerDir   string
	ownerPub   *rdSync.Publisher
	ownerEpoch int
	ownerCEK   [32]byte
}

func newSelfHealFixture(t *testing.T) *selfHealFixture {
	t.Helper()
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)

	base := t.TempDir()
	// RD_HOME feeds loadRDConfig()/nostrTrustSet(); an empty config degrades to
	// self+owner trust, which is all this test needs.
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("RD_HOME", home)
	// Both read and write relays resolve to the in-process relay.
	t.Setenv("RD_NOSTR_RELAY_URL", relay.url())
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner key: %v", err)
	}
	const boardD = "ready"
	coord := rdSync.BoardCoord(owner.PubKeyHex(), boardD)

	ownerDir := filepath.Join(base, "A")
	if err := os.MkdirAll(filepath.Join(ownerDir, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir owner .ready: %v", err)
	}
	// Confidential (Public unset) + pinned board.
	if err := rdconfig.SaveSyncConfig(ownerDir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord}); err != nil {
		t.Fatalf("owner SaveSyncConfig: %v", err)
	}
	ownerLog := rdSync.NewNostrLog(rdSync.NostrLogPath(ownerDir))
	be, err := rdSync.BuildBoardEvent(owner, rdSync.BoardSpec{BoardD: boardD, Title: "project", Maintainers: []string{owner.PubKeyHex()}}, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	if _, err := ownerLog.AppendUnique([]*nostr.Event{be}); err != nil {
		t.Fatalf("append board event: %v", err)
	}
	ownerPub := &rdSync.Publisher{
		Key:         owner,
		Log:         ownerLog,
		WriteRelays: []string{relay.url()},
		PendingPath: filepath.Join(ownerDir, ".ready", rdSync.NostrPendingFile),
	}

	// Owner's first confidential write bootstraps the CEK and publishes the owner
	// self-grant (to the relay + owner log).
	env, err := boardConfidentialEnvelope(ownerDir, ownerPub, owner.PubKeyHex(), boardD)
	if err != nil {
		t.Fatalf("owner bootstrap: %v", err)
	}
	if env == nil {
		t.Fatal("owner bootstrap returned a nil envelope on a confidential board")
	}

	return &selfHealFixture{
		relay: relay, base: base, boardD: boardD, coord: coord,
		owner: owner, ownerDir: ownerDir, ownerPub: ownerPub,
		ownerEpoch: env.Epoch, ownerCEK: env.CEK,
	}
}

// grantMemberToRelayOnly publishes an owner-signed CEK-bearing grant for member to
// the relay + owner log — but NOT to the member's log. This is the crux of edge #5:
// a valid grant exists on the relay that the member has never ingested.
func (f *selfHealFixture) grantMemberToRelayOnly(t *testing.T, memberPub string) {
	t.Helper()
	wCEK, epoch, wLTK, err := confidentialGrantKeys(f.ownerDir, f.ownerPub, f.owner.PubKeyHex(), f.boardD, memberPub, rdSync.RoleContributor)
	if err != nil {
		t.Fatalf("confidentialGrantKeys: %v", err)
	}
	if wCEK == "" {
		t.Fatal("owner produced no wrapped CEK for the member grant")
	}
	spec := rdSync.RoleGrantSpec{
		BoardD: f.boardD, BoardAuthor: f.owner.PubKeyHex(), Grantee: memberPub, Role: rdSync.RoleContributor,
		Label: "self-heal member", WrappedCEK: wCEK, CEKEpoch: epoch, WrappedLTK: wLTK,
	}
	ev, err := rdSync.BuildRoleGrantEvent(f.owner, spec, time.Now().Unix()+1)
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent: %v", err)
	}
	if _, err := f.ownerPub.PublishEvents(context.Background(), []*nostr.Event{ev}); err != nil {
		t.Fatalf("publish member grant to relay: %v", err)
	}
}

// newMemberMachine builds a member project dir whose local log is seeded with the
// board event + the OWNER self-grant (so the board is known-confidential locally),
// but no key-bearing grant for the member — the scary-error precondition.
func (f *selfHealFixture) newMemberMachine(t *testing.T, name string, member *nostr.Key) (string, *rdSync.Publisher) {
	t.Helper()
	memberDir := filepath.Join(f.base, name)
	if err := os.MkdirAll(filepath.Join(memberDir, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir member .ready: %v", err)
	}
	if err := rdconfig.SaveSyncConfig(memberDir, &rdconfig.SyncConfig{ProjectName: "project", Board: f.coord}); err != nil {
		t.Fatalf("member SaveSyncConfig: %v", err)
	}
	memberLog := rdSync.NewNostrLog(rdSync.NostrLogPath(memberDir))
	// Seed with the board event + the owner self-grant ONLY (the cutover source),
	// copied from the owner's log — an earlier reconcile before the member was granted.
	ownerEvents, err := f.ownerPub.Log.ReadAll()
	if err != nil {
		t.Fatalf("read owner log: %v", err)
	}
	var seed []*nostr.Event
	for _, e := range ownerEvents {
		if e.Kind == rdSync.KindBoard {
			seed = append(seed, e)
		}
		if e.Kind == rdSync.KindRoleGrant {
			if p, ok := tagVal(e.Tags, "p"); ok && p == f.owner.PubKeyHex() {
				seed = append(seed, e) // owner self-grant only
			}
		}
	}
	if _, err := memberLog.AppendUnique(seed); err != nil {
		t.Fatalf("seed member log: %v", err)
	}
	memberPub := &rdSync.Publisher{
		Key:         member,
		Log:         memberLog,
		WriteRelays: []string{f.relay.url()},
		PendingPath: filepath.Join(memberDir, ".ready", rdSync.NostrPendingFile),
	}
	return memberDir, memberPub
}

func TestConfidentialWriteSelfHealsMissingGrant(t *testing.T) {
	f := newSelfHealFixture(t)

	member, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("member key: %v", err)
	}
	f.grantMemberToRelayOnly(t, member.PubKeyHex())
	memberDir, memberPub := f.newMemberMachine(t, "B", member)

	// Precondition: with only the seed, the member is EXACTLY in the scary-error
	// branch — board known-confidential (cutover set) but no readable CEK.
	seedEvents, _ := memberPub.Log.ReadAll()
	seedKR := rdSync.DeriveBoardKeyring(seedEvents, member, f.owner.PubKeyHex(), f.boardD)
	if _, confidential := seedKR.Cutover(f.coord); !confidential {
		t.Fatal("precondition: member log must know the board is confidential (cutover) before self-heal")
	}
	if _, _, ok := seedKR.CurrentEpoch(f.coord); ok {
		t.Fatal("precondition: member must hold NO CEK locally before self-heal")
	}

	// The write self-heals: fetches the owner-signed member grant from the relay,
	// ingests it, and returns a sealing envelope — no scary error.
	env, err := boardConfidentialEnvelope(memberDir, memberPub, f.owner.PubKeyHex(), f.boardD)
	if err != nil {
		t.Fatalf("confidential write did not self-heal — errored instead: %v", err)
	}
	if env == nil {
		t.Fatal("self-heal returned a nil envelope; the write would fall through to plaintext on a confidential board")
	}
	// SECURITY: the recovered key must be the owner's genuine epoch-1 CEK.
	if env.Epoch != f.ownerEpoch {
		t.Fatalf("self-healed epoch = %d, want owner epoch %d", env.Epoch, f.ownerEpoch)
	}
	if env.CEK != f.ownerCEK {
		t.Fatal("self-healed CEK does not match the owner's minted CEK — wrong/forged key ingested")
	}

	// The grant is now durable in the member's local log (ingested, not just used).
	afterEvents, _ := memberPub.Log.ReadAll()
	afterKR := rdSync.DeriveBoardKeyring(afterEvents, member, f.owner.PubKeyHex(), f.boardD)
	if _, _, ok := afterKR.CurrentEpoch(f.coord); !ok {
		t.Fatal("self-heal did not persist the fetched grant into the local log")
	}
}

func TestConfidentialWriteStillErrorsWhenNoGrantExists(t *testing.T) {
	f := newSelfHealFixture(t)

	// A member that was NEVER granted: no key-bearing grant for it exists on the
	// relay, so the self-heal fetch finds nothing and the original error must fire.
	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("stranger key: %v", err)
	}
	memberDir, memberPub := f.newMemberMachine(t, "C", stranger)

	reqsBefore := f.relay.reqs()
	env, err := boardConfidentialEnvelope(memberDir, memberPub, f.owner.PubKeyHex(), f.boardD)
	if env != nil {
		t.Fatalf("no grant exists — a confidential write must NOT return a sealing envelope, got %+v", env)
	}
	if err == nil {
		t.Fatal("no grant exists — the write must still error, not silently succeed")
	}
	if !strings.Contains(err.Error(), "hold no read key") {
		t.Fatalf("expected the original 'hold no read key' error, got: %v", err)
	}
	// Guard against an infinite retry loop: the self-heal is a SINGLE fetch.
	if got := f.relay.reqs() - reqsBefore; got != 1 {
		t.Fatalf("self-heal must issue exactly one reconcile fetch, relay saw %d REQs", got)
	}
}
