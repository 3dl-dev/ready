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
// ignored on purpose for SERVING: the relay is an UNTRUSTED cache — correctness
// (owner signature, grantee binding, ECDH wrap opening) is enforced client-side by
// Verify + the reconcile trust gate + DeriveBoardKeyring, exactly as in prod. That
// deliberately makes the relay blind to a caller that sends a malformed or
// over-broad filter (wrong kind, missing "#p"/"#a", too many boards): serving
// everything regardless would make a broken query look exactly like a correct
// one to any assertion keyed on WHAT CAME BACK. So the relay separately RECORDS
// every filter it was actually asked with (reqFilters), and callers that need to
// prove the query itself was correctly scoped — not just that the derivation
// tolerated whatever arrived — assert against lastFilter()/reqFilters directly.
type storingRelay struct {
	srv        *httptest.Server
	mu         sync.Mutex
	events     []*nostr.Event
	reqCount   int
	reqFilters []map[string]any
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
				// Record the actual filter the caller sent, RAW — this is the
				// evidence a filter-ignoring relay cannot otherwise produce.
				// NIP-01 allows multiple filter objects per REQ; every relayFetchMany
				// call in this codebase sends exactly one (pkg/nostr/client.go's
				// FetchMany builds `["REQ", sub, filter]`), so frame[2] is it.
				var f map[string]any
				if len(frame) >= 3 {
					_ = json.Unmarshal(frame[2], &f)
				}
				r.mu.Lock()
				r.reqCount++
				r.reqFilters = append(r.reqFilters, f)
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

// lastFilter returns the filter object sent with the most recent REQ this relay
// received (nil if none yet), so a test can assert the QUERY was correctly
// scoped — kind, "#a", "#p" — independent of what this filter-ignoring relay
// chose to serve back.
func (r *storingRelay) lastFilter() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqFilters) == 0 {
		return nil
	}
	return r.reqFilters[len(r.reqFilters)-1]
}

// filterForKind returns the first recorded REQ filter whose "kinds" contains
// wantKind (nil if none). A single command can fan out MULTIPLE distinct
// gathers against the same relay (e.g. `rd board --portfolio` also runs a
// separate archived-boards check over KindBoard) — this finds the specific
// gather a test cares about, rather than assuming it was the last REQ sent.
func (r *storingRelay) filterForKind(wantKind int) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.reqFilters {
		for _, k := range filterKinds(f) {
			if k == wantKind {
				return f
			}
		}
	}
	return nil
}

// filterStrings pulls a string-tag array (e.g. "#a", "#p") out of a raw
// unmarshaled filter for a plain equality assertion.
func filterStrings(f map[string]any, key string) []string {
	raw, ok := f[key]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// filterKinds pulls the numeric "kinds" array out of a raw unmarshaled filter.
func filterKinds(f map[string]any) []int {
	raw, ok := f["kinds"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(arr))
	for _, v := range arr {
		if n, ok := v.(float64); ok { // encoding/json numbers decode as float64
			out = append(out, int(n))
		}
	}
	return out
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
	// boardD is an arbitrary fixture name, not the reserved production "ready"
	// coordinate (ready-fce; see Publisher.Production's doc) — this test's
	// relay is an in-process fake (relay.url() below), not a live production
	// relay, but the write-path guard fires regardless of which relay is dialed.
	const boardD = "selfheal-board"
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

	// THE QUERY ITSELF, asserted directly — not inferred from what came back.
	// storingRelay ignores filters when SERVING (see its doc comment), so a
	// self-heal fetch that sent kinds=[] (everything) or dropped "#a"/"#p" would
	// still land the same grant and this test would pass for the wrong reason.
	// The recorded REQ closes that gap: it proves ReconcileSelfGrants asked for
	// exactly kind-39301 role grants, scoped to THIS board and THIS member —
	// not an over-broad "give me everything" query a permissive relay would
	// happily also have satisfied.
	got := f.relay.lastFilter()
	if got == nil {
		t.Fatal("self-heal never sent a REQ to the relay — nothing to assert the filter shape of")
	}
	if kinds := filterKinds(got); len(kinds) != 1 || kinds[0] != rdSync.KindRoleGrant {
		t.Errorf("self-heal filter kinds = %v, want exactly [%d] (KindRoleGrant) — not an over-broad kind set", kinds, rdSync.KindRoleGrant)
	}
	if a := filterStrings(got, "#a"); len(a) != 1 || a[0] != f.coord {
		t.Errorf("self-heal filter #a = %v, want exactly [%q] — an unscoped or wrong-board query", a, f.coord)
	}
	if p := filterStrings(got, "#p"); len(p) != 1 || p[0] != member.PubKeyHex() {
		t.Errorf("self-heal filter #p = %v, want exactly [%q] — an unscoped or wrong-member query", p, member.PubKeyHex())
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
	// And that single fetch was correctly scoped to THIS stranger, not a
	// broad query that happened to still come back empty in this fixture.
	filt := f.relay.lastFilter()
	if p := filterStrings(filt, "#p"); len(p) != 1 || p[0] != stranger.PubKeyHex() {
		t.Errorf("self-heal filter #p = %v, want exactly [%q]", p, stranger.PubKeyHex())
	}
	if a := filterStrings(filt, "#a"); len(a) != 1 || a[0] != f.coord {
		t.Errorf("self-heal filter #a = %v, want exactly [%q]", a, f.coord)
	}
}

// TestConfidentialWriteSelfHealRejectsHostileGrants is the ready-b66 hardening
// (3): a hostile or merely-permissive relay (storingRelay deliberately ignores
// filters, exactly like one) can serve MORE than the one genuine owner-signed
// grant for this pubkey+board in response to the self-heal reconcile fetch. Three
// hostile grants are seeded alongside the one valid grant:
//
//  1. ATTACKER-signed — a real signature, but from a key that is NEITHER the board
//     owner NOR anywhere in the member's trust closure. This must be rejected at
//     the RECONCILE TRUST GATE (nostrinbound.go reconcile()) — the self-heal SEAM
//     — and so must never even be merged into the member's local log. Proving this
//     at the seam (not just "DeriveBoardKeyring ignores non-owner signers") is the
//     point of this test: a relay-injection defense that only worked downstream
//     would still let an attacker bloat the local log with junk it can never use.
//  2. OWNER-signed, valid, but addressed to a DIFFERENT MEMBER on the SAME board —
//     a genuine grant for someone else that a permissive relay serves anyway.
//  3. OWNER-signed, valid, but for a DIFFERENT BOARD entirely (still addressed to
//     THIS member) — a genuine grant that must not leak its board's key into this
//     board's derivation.
//
// Grants #2 and #3 pass the trust gate (owner IS trusted) and so are expected to
// land in the local log — reconcile() admits any trusted-signer role-grant
// unconditionally (nostrinbound.go: role-grants carry no item id and are
// authoritative regardless of addressee/board, by design). Their rejection must
// happen at DeriveBoardKeyring's grantee/board-coordinate checks instead. Each
// hostile grant carries a CEK at a deliberately distinct, easy-to-recognize epoch
// (99, 2, 5) so a leak is unambiguous: the final envelope/keyring must reflect
// ONLY the genuine owner epoch-1 CEK from fixture bootstrap.
func TestConfidentialWriteSelfHealRejectsHostileGrants(t *testing.T) {
	f := newSelfHealFixture(t)

	member, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("member key: %v", err)
	}
	f.grantMemberToRelayOnly(t, member.PubKeyHex())

	// Hostile #1: ATTACKER-signed, claiming a CEK for member on THIS board.
	attacker, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("attacker key: %v", err)
	}
	var attackerCEK [32]byte
	copy(attackerCEK[:], []byte("attacker-controlled-malicious-cek"))
	wAttackerCEK, err := rdSync.WrapKey(attacker, member.PubKeyHex(), attackerCEK)
	if err != nil {
		t.Fatalf("wrap attacker cek: %v", err)
	}
	attackerEv, err := rdSync.BuildRoleGrantEvent(attacker, rdSync.RoleGrantSpec{
		BoardD: f.boardD, BoardAuthor: f.owner.PubKeyHex(), Grantee: member.PubKeyHex(),
		Role: rdSync.RoleContributor, Label: "hostile: attacker-signed",
		WrappedCEK: wAttackerCEK, CEKEpoch: 99,
	}, time.Now().Unix()+1)
	if err != nil {
		t.Fatalf("build attacker grant: %v", err)
	}
	if accepted, msg, perr := rdSync.GuardedPublish(context.Background(), f.relay.url(), attackerEv, false); perr != nil || !accepted {
		t.Fatalf("publish attacker grant to relay: accepted=%v msg=%q err=%v", accepted, msg, perr)
	}

	// Hostile #2: OWNER-signed, valid — but addressed to a DIFFERENT member, same board.
	otherMember, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("other member key: %v", err)
	}
	var otherMemberCEK [32]byte
	copy(otherMemberCEK[:], []byte("owner-cek-for-a-different-member"))
	wOtherMemberCEK, err := rdSync.WrapKey(f.owner, otherMember.PubKeyHex(), otherMemberCEK)
	if err != nil {
		t.Fatalf("wrap other-member cek: %v", err)
	}
	retargetedMemberEv, err := rdSync.BuildRoleGrantEvent(f.owner, rdSync.RoleGrantSpec{
		BoardD: f.boardD, BoardAuthor: f.owner.PubKeyHex(), Grantee: otherMember.PubKeyHex(),
		Role: rdSync.RoleContributor, Label: "hostile: owner-signed, wrong member",
		WrappedCEK: wOtherMemberCEK, CEKEpoch: 2,
	}, time.Now().Unix()+2)
	if err != nil {
		t.Fatalf("build retargeted-member grant: %v", err)
	}
	if accepted, msg, perr := rdSync.GuardedPublish(context.Background(), f.relay.url(), retargetedMemberEv, false); perr != nil || !accepted {
		t.Fatalf("publish retargeted-member grant to relay: accepted=%v msg=%q err=%v", accepted, msg, perr)
	}

	// Hostile #3: OWNER-signed, valid — addressed to THIS member, but for a DIFFERENT board.
	var otherBoardCEK [32]byte
	copy(otherBoardCEK[:], []byte("owner-cek-for-a-different-board"))
	wOtherBoardCEK, err := rdSync.WrapKey(f.owner, member.PubKeyHex(), otherBoardCEK)
	if err != nil {
		t.Fatalf("wrap other-board cek: %v", err)
	}
	retargetedBoardEv, err := rdSync.BuildRoleGrantEvent(f.owner, rdSync.RoleGrantSpec{
		BoardD: "other-board", BoardAuthor: f.owner.PubKeyHex(), Grantee: member.PubKeyHex(),
		Role: rdSync.RoleContributor, Label: "hostile: owner-signed, wrong board",
		WrappedCEK: wOtherBoardCEK, CEKEpoch: 5,
	}, time.Now().Unix()+3)
	if err != nil {
		t.Fatalf("build retargeted-board grant: %v", err)
	}
	if accepted, msg, perr := rdSync.GuardedPublish(context.Background(), f.relay.url(), retargetedBoardEv, false); perr != nil || !accepted {
		t.Fatalf("publish retargeted-board grant to relay: accepted=%v msg=%q err=%v", accepted, msg, perr)
	}

	memberDir, memberPub := f.newMemberMachine(t, "D", member)

	env, err := boardConfidentialEnvelope(memberDir, memberPub, f.owner.PubKeyHex(), f.boardD)
	if err != nil {
		t.Fatalf("confidential write did not self-heal amid hostile grants — errored instead: %v", err)
	}
	if env == nil {
		t.Fatal("self-heal returned a nil envelope amid hostile grants; the write would fall through to plaintext on a confidential board")
	}
	// Only the ONE valid grant may contribute: genuine owner epoch-1 CEK, never a
	// hostile epoch (99, 2, 5) or a hostile CEK value.
	if env.Epoch != f.ownerEpoch {
		t.Fatalf("self-healed epoch = %d, want owner epoch %d (a hostile grant's epoch leaked in)", env.Epoch, f.ownerEpoch)
	}
	if env.CEK != f.ownerCEK {
		t.Fatal("self-healed CEK does not match the owner's genuine CEK — a hostile grant's key leaked in")
	}

	// SEAM ASSERTION: the attacker-signed event must never have been merged into
	// the member's local log — proving relay-injection rejection at the reconcile
	// trust gate itself, not merely a downstream DeriveBoardKeyring filter.
	afterEvents, err := memberPub.Log.ReadAll()
	if err != nil {
		t.Fatalf("read member log: %v", err)
	}
	for _, e := range afterEvents {
		if e.ID == attackerEv.ID {
			t.Fatalf("attacker-signed grant (id=%s) was merged into the member's local log by self-heal — relay-injection rejection failed at the seam", e.ID)
		}
	}

	// The owner-signed-but-wrong-target grants MAY legitimately land in the log
	// (reconcile admits any trusted-signer role-grant unconditionally) — what must
	// NOT happen is either one yielding usable key material for THIS member on
	// THIS board. Re-derive independently and check each hostile epoch is absent.
	afterKR := rdSync.DeriveBoardKeyring(afterEvents, member, f.owner.PubKeyHex(), f.boardD)
	if _, ok := afterKR.CEK(f.coord, 99); ok {
		t.Fatal("attacker-signed grant's epoch 99 yielded a usable CEK — hostile injection succeeded")
	}
	if _, ok := afterKR.CEK(f.coord, 2); ok {
		t.Fatal("owner-signed grant retargeted to a DIFFERENT MEMBER (epoch 2) yielded a usable CEK for this member")
	}
	if _, ok := afterKR.CEK(f.coord, 5); ok {
		t.Fatal("owner-signed grant retargeted to a DIFFERENT BOARD (epoch 5) yielded a usable CEK for this board coordinate")
	}
	if epoch, cek, ok := afterKR.CurrentEpoch(f.coord); !ok || epoch != f.ownerEpoch || cek != f.ownerCEK {
		t.Fatalf("CurrentEpoch = (%d, ok=%v), want ONLY the genuine owner epoch %d", epoch, ok, f.ownerEpoch)
	}
}
