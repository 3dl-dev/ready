package sync

// DerivePortfolioKeyring (ready-4d9): "every board this key can read", the
// enumeration `rd board --portfolio --with-key` needs before it can gather any
// keys.
//
// WHAT THESE TESTS ARE SHAPED AROUND. The function's whole job is to widen a
// per-board computation to a whole portfolio, and the way a widening like this
// goes wrong is by widening one step too far — picking up a board whose grant
// was signed by the wrong key, or addressed to someone else, or forged outright.
// So every negative case below places a REAL, WELL-FORMED, DECRYPTABLE-LOOKING
// key one property short of admissible, and asserts the key never appears. Each
// one is paired with a positive control on the same fixture, so a build where
// portfolio derivation returned nothing at all would fail the positives rather
// than pass the negatives.

import (
	"sort"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// pkBoard bootstraps one confidential board owned by `owner` and grants
// `grantee` the CEK for `epoch`, exactly the way the real owner bootstrap does.
func pkBoard(t *testing.T, owner *nostr.Key, boardD string, grantee string, epoch int, at int64) (*nostr.Event, [32]byte) {
	t.Helper()
	cek, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	g := kdGrant(t, owner, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: grantee,
		Role: RoleOwner, WrappedCEK: kdWrap(t, owner, grantee, cek), CEKEpoch: epoch,
	}, at)
	return g, cek
}

func heldCEK(t *testing.T, kr *BoardKeyring, coord string, epoch int) [32]byte {
	t.Helper()
	cek, ok := kr.CEK(coord, epoch)
	if !ok {
		t.Fatalf("keyring holds no CEK for %s epoch %d", coord, epoch)
	}
	return cek
}

// TestDerivePortfolioKeyring_SpansEveryBoardTheKeyCanRead is the positive case
// and the point of the function: three boards, two owners, and one call returns
// all of them — including the boards this reader does NOT own, which is what a
// per-board DeriveBoardKeyring call in a single project directory can never
// reach.
func TestDerivePortfolioKeyring_SpansEveryBoardTheKeyCanRead(t *testing.T) {
	me := kdKey(t)
	other := kdKey(t)

	g1, cek1 := pkBoard(t, me, "ready", me.PubKeyHex(), 1, 1000)
	g2, cek2 := pkBoard(t, me, "galtrader", me.PubKeyHex(), 1, 1001)
	g3, cek3 := pkBoard(t, other, "forge", me.PubKeyHex(), 7, 1002)

	kr, coords := DerivePortfolioKeyring([]*nostr.Event{g1, g2, g3}, me)

	want := []string{
		BoardCoord(other.PubKeyHex(), "forge"),
		BoardCoord(me.PubKeyHex(), "galtrader"),
		BoardCoord(me.PubKeyHex(), "ready"),
	}
	if len(coords) != len(want) {
		t.Fatalf("coords = %v, want the 3 boards %v", coords, want)
	}
	got := map[string]bool{}
	for _, c := range coords {
		got[c] = true
	}
	for _, c := range want {
		if !got[c] {
			t.Errorf("coords %v is missing %s", coords, c)
		}
	}
	// Sorted ascending by coordinate — a link's byte shape must not depend on map
	// iteration order.
	if !sort.StringsAreSorted(coords) {
		t.Errorf("coords are not sorted ascending: %v", coords)
	}

	// The KEYS, not just the coordinates: each board's real CEK, at its real epoch.
	if k := heldCEK(t, kr, BoardCoord(me.PubKeyHex(), "ready"), 1); k != cek1 {
		t.Error("ready board's CEK is not the one the grant wrapped")
	}
	if k := heldCEK(t, kr, BoardCoord(me.PubKeyHex(), "galtrader"), 1); k != cek2 {
		t.Error("galtrader board's CEK is not the one the grant wrapped")
	}
	if k := heldCEK(t, kr, BoardCoord(other.PubKeyHex(), "forge"), 7); k != cek3 {
		t.Error("forge board's CEK is not the one the grant wrapped")
	}
}

// TestDerivePortfolioKeyring_CarriesEveryHeldEpoch: a rotated board has cards
// sealed under the older epoch, so handing over "the read capability" means
// handing over every epoch, not the newest one. Same reason BoardKeyring.Epochs
// exists (pkg/sync/keyepochs.go).
func TestDerivePortfolioKeyring_CarriesEveryHeldEpoch(t *testing.T) {
	me := kdKey(t)
	g1, cek1 := pkBoard(t, me, "ready", me.PubKeyHex(), 1, 1000)
	g2, cek2 := pkBoard(t, me, "ready", me.PubKeyHex(), 2, 1001)

	kr, coords := DerivePortfolioKeyring([]*nostr.Event{g1, g2}, me)
	coord := BoardCoord(me.PubKeyHex(), "ready")
	if len(coords) != 1 || coords[0] != coord {
		t.Fatalf("coords = %v, want exactly [%s]", coords, coord)
	}
	if eps := kr.Epochs(coord); len(eps) != 2 || eps[0] != 1 || eps[1] != 2 {
		t.Fatalf("Epochs = %v, want [1 2] — a rotated board's older cards need the older key", eps)
	}
	if k := heldCEK(t, kr, coord, 1); k != cek1 {
		t.Error("epoch 1 CEK is wrong")
	}
	if k := heldCEK(t, kr, coord, 2); k != cek2 {
		t.Error("epoch 2 CEK is wrong")
	}
}

// TestDerivePortfolioKeyring_RejectsInadmissibleGrants is the negative battery.
// Every case ships a REAL 32-byte CEK, really NIP-44-wrapped, inside a really
// well-formed grant — one property short of admissible each time — alongside a
// board that IS admissible, so "returned nothing" cannot pass.
func TestDerivePortfolioKeyring_RejectsInadmissibleGrants(t *testing.T) {
	me := kdKey(t)
	owner := kdKey(t)
	impostor := kdKey(t)
	stranger := kdKey(t)

	// The positive control: an ordinary owner-signed grant addressed to me.
	good, goodCEK := pkBoard(t, owner, "good", me.PubKeyHex(), 1, 1000)

	// (a) NOT SIGNED BY THE BOARD OWNER. The impostor signs a grant naming
	// owner's board coordinate and wraps a key of its own choosing to me. Only
	// the authz root mints board keys.
	impostorCEK, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	notOwner := kdGrant(t, impostor, RoleGrantSpec{
		BoardD: "hijacked", BoardAuthor: owner.PubKeyHex(), Grantee: me.PubKeyHex(),
		Role: RoleOwner, WrappedCEK: kdWrap(t, impostor, me.PubKeyHex(), impostorCEK), CEKEpoch: 1,
	}, 1001)

	// (b) ADDRESSED TO SOMEBODY ELSE. A perfectly valid owner grant — for the
	// stranger. Reading the relay does not make it mine.
	notMine, notMineCEK := pkBoard(t, owner, "someone-elses", stranger.PubKeyHex(), 1, 1002)

	// (c) FORGED SIGNATURE. A grant that would be admissible if the bytes were
	// signed; they are not.
	forged, forgedCEK := pkBoard(t, owner, "forged", me.PubKeyHex(), 1, 1003)
	forged.Sig = "00" + forged.Sig[2:]

	// (d) EPOCH 0. parseRoleGrant coerces an unparseable cek_epoch to 0; a key
	// must never be bound to it.
	zeroCEK, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	epochZero := kdGrant(t, owner, RoleGrantSpec{
		BoardD: "epoch-zero", BoardAuthor: owner.PubKeyHex(), Grantee: me.PubKeyHex(),
		Role: RoleOwner, WrappedCEK: kdWrap(t, owner, me.PubKeyHex(), zeroCEK), CEKEpoch: 0,
	}, 1004)

	// (e) WRAP LIFTED FROM SOMEONE ELSE'S GRANT. The owner really signed a grant
	// p-tagged to me, but the cek tag is the wrap made for the stranger — the
	// ECDH binding must refuse it.
	liftedCEK, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	lifted := kdGrant(t, owner, RoleGrantSpec{
		BoardD: "lifted", BoardAuthor: owner.PubKeyHex(), Grantee: me.PubKeyHex(),
		Role: RoleOwner, WrappedCEK: kdWrap(t, owner, stranger.PubKeyHex(), liftedCEK), CEKEpoch: 1,
	}, 1005)

	events := []*nostr.Event{good, notOwner, notMine, forged, epochZero, lifted}
	kr, coords := DerivePortfolioKeyring(events, me)

	goodCoord := BoardCoord(owner.PubKeyHex(), "good")
	if len(coords) != 1 || coords[0] != goodCoord {
		t.Fatalf("coords = %v, want exactly [%s] — only the admissible board may appear", coords, goodCoord)
	}
	if k := heldCEK(t, kr, goodCoord, 1); k != goodCEK {
		t.Fatal("the admissible board's CEK is wrong — the positive control failed, so the negatives below prove nothing")
	}

	// And none of the inadmissible key material is reachable under ANY coordinate.
	forbidden := map[string][32]byte{
		"grant signed by a non-owner":      impostorCEK,
		"grant addressed to someone else":  notMineCEK,
		"grant with a forged signature":    forgedCEK,
		"grant with cek_epoch 0":           zeroCEK,
		"wrap lifted from another grantee": liftedCEK,
	}
	for label, secret := range forbidden {
		for _, coord := range []string{
			goodCoord,
			BoardCoord(owner.PubKeyHex(), "hijacked"),
			BoardCoord(owner.PubKeyHex(), "someone-elses"),
			BoardCoord(owner.PubKeyHex(), "forged"),
			BoardCoord(owner.PubKeyHex(), "epoch-zero"),
			BoardCoord(owner.PubKeyHex(), "lifted"),
			BoardCoord(impostor.PubKeyHex(), "hijacked"),
		} {
			for _, epoch := range []int{0, 1} {
				if got, ok := kr.CEK(coord, epoch); ok && got == secret {
					t.Errorf("%s yielded a usable CEK at %s epoch %d", label, coord, epoch)
				}
			}
		}
	}
}

// TestDerivePortfolioKeyring_NoGrants returns an empty, non-nil result. A key
// with no confidential boards must produce a link with no keys= parameter, not a
// crash and not a link with an empty one.
func TestDerivePortfolioKeyring_NoGrants(t *testing.T) {
	kr, coords := DerivePortfolioKeyring(nil, kdKey(t))
	if kr == nil {
		t.Fatal("DerivePortfolioKeyring returned a nil keyring")
	}
	if len(coords) != 0 {
		t.Errorf("coords = %v, want none", coords)
	}
}

// TestDerivePortfolioKeyring_CarriesCutoverAndLTKForAdmittedBoards: the returned
// keyring must answer Cutover()/LTK() the same way a per-board
// DeriveBoardKeyring would, for the boards it admitted. A caller that swapped in
// the portfolio keyring and silently lost the cutover would disable the fold
// gate that quarantines post-cutover cleartext.
func TestDerivePortfolioKeyring_CarriesCutoverAndLTKForAdmittedBoards(t *testing.T) {
	me := kdKey(t)
	cek, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	ltk, err := MintKey()
	if err != nil {
		t.Fatalf("MintKey ltk: %v", err)
	}
	const at = 1700000000
	g := kdGrant(t, me, RoleGrantSpec{
		BoardD: "ready", BoardAuthor: me.PubKeyHex(), Grantee: me.PubKeyHex(),
		Role: RoleOwner, WrappedCEK: kdWrap(t, me, me.PubKeyHex(), cek), CEKEpoch: 1,
		WrappedLTK: kdWrap(t, me, me.PubKeyHex(), ltk),
	}, at)

	kr, _ := DerivePortfolioKeyring([]*nostr.Event{g}, me)
	coord := BoardCoord(me.PubKeyHex(), "ready")

	perBoard := DeriveBoardKeyring([]*nostr.Event{g}, me, me.PubKeyHex(), "ready")
	wantCutover, wantOK := perBoard.Cutover(coord)
	gotCutover, gotOK := kr.Cutover(coord)
	if gotOK != wantOK || gotCutover != wantCutover {
		t.Errorf("Cutover = (%d,%v), want (%d,%v) — same as the per-board keyring", gotCutover, gotOK, wantCutover, wantOK)
	}
	if got, ok := kr.LTK(coord); !ok || got != ltk {
		t.Errorf("LTK not carried across for an admitted board")
	}
}
