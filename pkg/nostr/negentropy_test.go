package nostr

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"testing"
)

// mkItem builds a deterministic NegItem from a timestamp and a seed byte.
func mkItem(ts uint64, seed byte) NegItem {
	var it NegItem
	it.Timestamp = ts
	h := sha256.Sum256([]byte{seed})
	copy(it.ID[:], h[:])
	return it
}

// TestVarintRoundTrip checks the base-128 MSB-first varint codec against a set of
// values including multi-byte boundaries.
func TestVarintRoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 127, 128, 129, 255, 256, 16383, 16384, 1 << 21, 1 << 28, 1<<32 - 1, 1 << 35, ^uint64(0)}
	for _, v := range cases {
		var buf []byte
		appendVarint(&buf, v)
		r := &byteReader{buf: buf}
		got, err := r.readVarint()
		if err != nil {
			t.Fatalf("readVarint(%d): %v", v, err)
		}
		if got != v {
			t.Errorf("varint round-trip: got %d want %d", got, v)
		}
		if r.remaining() != 0 {
			t.Errorf("varint %d left %d bytes", v, r.remaining())
		}
	}
}

// TestVarintKnownEncoding pins byte-exact encodings from the spec (base-128,
// high bit set on all but the last byte, most-significant digit first).
func TestVarintKnownEncoding(t *testing.T) {
	cases := map[uint64][]byte{
		0:   {0x00},
		1:   {0x01},
		127: {0x7f},
		128: {0x81, 0x00},
		300: {0x82, 0x2c}, // 300 = 0b100101100 -> 0x82 0x2c
	}
	for v, want := range cases {
		var buf []byte
		appendVarint(&buf, v)
		if !bytesEqual(buf, want) {
			t.Errorf("varint(%d) = % x, want % x", v, buf, want)
		}
	}
}

// TestFingerprintKnownVector checks the fingerprint algorithm: mod-2^256 LE sum
// of ids || varint(count), SHA-256, first 16 bytes — computed independently.
func TestFingerprintKnownVector(t *testing.T) {
	items := []NegItem{mkItem(10, 1), mkItem(20, 2), mkItem(30, 3)}
	n, err := NewNegentropy(items)
	if err != nil {
		t.Fatal(err)
	}
	got := n.fingerprint(0, len(n.items))

	// Independent reference computation.
	var acc [32]byte
	var carry uint16
	for _, it := range items {
		carry = 0
		for i := 0; i < 32; i++ {
			s := uint16(acc[i]) + uint16(it.ID[i]) + carry
			acc[i] = byte(s)
			carry = s >> 8
		}
	}
	pre := append(acc[:], 3) // varint(3) == 0x03
	sum := sha256.Sum256(pre)
	want := sum[:16]
	if !bytesEqual(got, want) {
		t.Errorf("fingerprint = % x, want % x", got, want)
	}
}

// TestReconcileConvergence runs two independent client sessions against each
// other (each acts as the other's "server" by feeding messages back and forth)
// and asserts the initiator learns the exact have/need diff. This validates the
// bound codec, range splitting, fingerprint compare, and diff extraction with NO
// relay — a pure deterministic proof.
func TestReconcileConvergence(t *testing.T) {
	// Shared items both sides hold.
	var shared []NegItem
	for i := 0; i < 50; i++ {
		shared = append(shared, mkItem(uint64(1000+i), byte(i)))
	}
	// Client-only items (client HAS, server NEEDs).
	clientOnly := []NegItem{mkItem(5000, 200), mkItem(5001, 201), mkItem(5002, 202)}
	// Server-only items (server HAS, client NEEDs).
	serverOnly := []NegItem{mkItem(6000, 210), mkItem(6001, 211)}

	clientItems := append(append([]NegItem{}, shared...), clientOnly...)
	serverItems := append(append([]NegItem{}, shared...), serverOnly...)

	client, err := NewNegentropy(clientItems)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewNegentropy(serverItems)
	if err != nil {
		t.Fatal(err)
	}

	haveSet := map[string]bool{}
	needSet := map[string]bool{}

	msg := client.Initiate()
	for round := 0; round < 100; round++ {
		// Server processes the client's message and replies. The server side reuses
		// the same client reconcile logic (symmetric message format); we discard the
		// server's have/need — only the CLIENT's view is authoritative per spec.
		serverReply, err := server.reconcileServer(msg)
		if err != nil {
			t.Fatalf("server reconcile round %d: %v", round, err)
		}
		// If the server's reply is just the version byte, nothing left to send.
		response, have, need, err := client.Reconcile(serverReply)
		if err != nil {
			t.Fatalf("client reconcile round %d: %v", round, err)
		}
		for _, id := range have {
			haveSet[HexID(id)] = true
		}
		for _, id := range need {
			needSet[HexID(id)] = true
		}
		if response == nil {
			break
		}
		msg = response
	}

	assertIDSet(t, "have (client-only)", haveSet, clientOnly)
	assertIDSet(t, "need (server-only)", needSet, serverOnly)
}

// TestReconcileIdenticalSets: when both sides hold the same set, the diff is
// empty and the exchange terminates quickly.
func TestReconcileIdenticalSets(t *testing.T) {
	var items []NegItem
	for i := 0; i < 40; i++ {
		items = append(items, mkItem(uint64(100+i), byte(i)))
	}
	client, _ := NewNegentropy(items)
	server, _ := NewNegentropy(items)

	haveSet, needSet := map[string]bool{}, map[string]bool{}
	msg := client.Initiate()
	rounds := 0
	for ; rounds < 50; rounds++ {
		serverReply, err := server.reconcileServer(msg)
		if err != nil {
			t.Fatal(err)
		}
		response, have, need, err := client.Reconcile(serverReply)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range have {
			haveSet[HexID(id)] = true
		}
		for _, id := range need {
			needSet[HexID(id)] = true
		}
		if response == nil {
			break
		}
		msg = response
	}
	if len(haveSet) != 0 || len(needSet) != 0 {
		t.Errorf("identical sets should have empty diff: have=%d need=%d", len(haveSet), len(needSet))
	}
}

// TestReconcileEmptyClient: client holds nothing, needs everything the server has.
func TestReconcileEmptyClient(t *testing.T) {
	var serverItems []NegItem
	for i := 0; i < 20; i++ {
		serverItems = append(serverItems, mkItem(uint64(100+i), byte(i)))
	}
	client, _ := NewNegentropy(nil)
	server, _ := NewNegentropy(serverItems)

	needSet := map[string]bool{}
	msg := client.Initiate()
	for round := 0; round < 50; round++ {
		serverReply, err := server.reconcileServer(msg)
		if err != nil {
			t.Fatal(err)
		}
		response, _, need, err := client.Reconcile(serverReply)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range need {
			needSet[HexID(id)] = true
		}
		if response == nil {
			break
		}
		msg = response
	}
	assertIDSet(t, "need everything", needSet, serverItems)
}

// TestTimestampDeltaEncoding checks the special timestamp codec: infinity==0,
// others 1+delta, reset per message.
func TestTimestampDeltaEncoding(t *testing.T) {
	n := &Negentropy{}
	var o []byte
	n.lastTimestampOut = 0
	n.encodeTimestampOut(&o, 100) // delta 100 -> varint(101)
	n.encodeTimestampOut(&o, 150) // delta 50  -> varint(51)
	n.encodeTimestampOut(&o, negInfinity)

	r := &byteReader{buf: o}
	n.lastTimestampIn = 0
	t1, _ := n.decodeTimestampIn(r)
	t2, _ := n.decodeTimestampIn(r)
	t3, _ := n.decodeTimestampIn(r)
	if t1 != 100 || t2 != 150 || t3 != negInfinity {
		t.Errorf("timestamp decode = %d,%d,%d want 100,150,inf", t1, t2, t3)
	}
}

// TestNegItemSortStable ensures sorting matches the spec (timestamp asc, then
// lexical id), independent of input order.
func TestNegItemSortStable(t *testing.T) {
	a := mkItem(10, 1)
	b := mkItem(10, 2)
	c := mkItem(20, 3)
	// Force known id order between a and b.
	if !negItemLess(a, b) && !negItemLess(b, a) {
		t.Skip("degenerate equal ids")
	}
	in := []NegItem{c, b, a}
	n, _ := NewNegentropy(in)
	if len(n.items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(n.items))
	}
	// Verify ascending order.
	for i := 1; i < len(n.items); i++ {
		if negItemLess(n.items[i], n.items[i-1]) {
			t.Errorf("items not sorted at %d", i)
		}
	}
	_ = binary.LittleEndian // keep import used if refactored
}

func assertIDSet(t *testing.T, label string, got map[string]bool, want []NegItem) {
	t.Helper()
	wantSet := map[string]bool{}
	for _, it := range want {
		wantSet[HexID(it.ID)] = true
	}
	if len(got) != len(wantSet) {
		gotKeys := make([]string, 0, len(got))
		for k := range got {
			gotKeys = append(gotKeys, k)
		}
		sort.Strings(gotKeys)
		t.Errorf("[%s] set size = %d, want %d (got %v)", label, len(got), len(wantSet), gotKeys)
	}
	for k := range wantSet {
		if !got[k] {
			t.Errorf("[%s] missing id %s", label, k)
		}
	}
	for k := range got {
		if !wantSet[k] {
			t.Errorf("[%s] unexpected id %s", label, k)
		}
	}
}
