// Negentropy (NIP-77) set reconciliation — client/initiator side (ready-797).
//
// This is a from-spec implementation of the Negentropy Protocol V1 wire format
// (https://github.com/hoytech/negentropy, docs/negentropy-protocol-v1.md), the
// range-based set-reconciliation protocol strfry speaks natively. rd uses it to
// converge two machines' work-item state against a relay while transferring only
// the DIFFERENCE between the local set and the relay's set — never the whole
// dataset. This is precisely what kills campfire's fs-sync pathology (the 44x
// full re-sync, multi-GB joins): the amount of data on the wire is bounded by the
// number of DIFFERING event ids, not the size of the set.
//
// Scope: rd is always the CLIENT (initiator). strfry is the server. The client
// creates the initial message, exchanges messages until its reconstructed
// response is empty, and ends up knowing exactly which event ids it HAS that the
// relay lacks (upload set) and which the relay HAS that it lacks (download set).
// The actual event transfer (REQ download / EVENT upload) is external to this
// protocol and lives in pkg/sync.
//
// The record ID is the 32-byte nostr event id; the ordering timestamp is the
// event created_at (seconds). Byte encoding follows the spec exactly so it
// interoperates with a live strfry relay (proven in nostr_live_relay_test.go /
// the two-machine demo).
package nostr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

const (
	// negProtocolVersion is Negentropy V1 (byte 0x61) — the version strfry speaks.
	negProtocolVersion = 0x61
	// negIDSize is the record id length in bytes (a nostr event id).
	negIDSize = 32
	// negFingerprintSize is the truncated-SHA256 fingerprint length.
	negFingerprintSize = 16
	// negInfinity is the reserved "infinity" timestamp (max uint64). Real event
	// created_at values (seconds) are always far below this.
	negInfinity = ^uint64(0)
	// negBuckets is how many sub-ranges a differing Fingerprint range is split into.
	negBuckets = 16
)

// negentropy protocol modes.
const (
	negModeSkip        = 0
	negModeFingerprint = 1
	negModeIDList      = 2
)

// NegItem is one reconciliation record: an ordering timestamp plus a 32-byte id.
type NegItem struct {
	Timestamp uint64
	ID        [negIDSize]byte
}

// negBound is an (exclusive) upper bound: a timestamp plus a variable-length id
// prefix that disambiguates records sharing a timestamp.
type negBound struct {
	Timestamp uint64
	ID        [negIDSize]byte // zero-padded; only IDLen bytes are meaningful
	IDLen     int
}

// Negentropy is a client-side reconciliation session over a sorted record set.
type Negentropy struct {
	items []NegItem
	// per-message timestamp delta state (reset at the start of each message).
	lastTimestampIn  uint64
	lastTimestampOut uint64
}

// NewNegentropy builds a client session over the given records. The records are
// sorted (ascending timestamp, then lexical id) as the protocol requires, and
// de-duplicated by id. It errors if any record uses the reserved infinity
// timestamp.
func NewNegentropy(items []NegItem) (*Negentropy, error) {
	cp := make([]NegItem, 0, len(items))
	seen := make(map[[negIDSize]byte]bool, len(items))
	for _, it := range items {
		if it.Timestamp == negInfinity {
			return nil, errors.New("negentropy: record uses reserved infinity timestamp")
		}
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		cp = append(cp, it)
	}
	sort.Slice(cp, func(i, j int) bool { return negItemLess(cp[i], cp[j]) })
	return &Negentropy{items: cp}, nil
}

// negItemLess orders records ascending by timestamp, then lexically by id.
func negItemLess(a, b NegItem) bool {
	if a.Timestamp != b.Timestamp {
		return a.Timestamp < b.Timestamp
	}
	for i := 0; i < negIDSize; i++ {
		if a.ID[i] != b.ID[i] {
			return a.ID[i] < b.ID[i]
		}
	}
	return false
}

// Initiate builds the client's initial reconciliation message covering the full
// timestamp/id universe.
func (n *Negentropy) Initiate() []byte {
	n.lastTimestampOut = 0
	out := []byte{negProtocolVersion}
	n.splitRange(0, len(n.items), negBound{Timestamp: negInfinity}, &out)
	return out
}

// Reconcile processes one message from the server and produces the client's
// response message, plus the ids discovered so far. have = ids the client holds
// that the server lacks (upload set); need = ids the server holds that the client
// lacks (download set). When the returned response is nil the client is done: it
// has learned the full have/need difference and should send NEG-CLOSE.
func (n *Negentropy) Reconcile(query []byte) (response []byte, have [][negIDSize]byte, need [][negIDSize]byte, err error) {
	out, h, nd, err := n.reconcileAux(query, true)
	if err != nil {
		return nil, nil, nil, err
	}
	// A response consisting of only the version byte is a full-universe Skip:
	// nothing more is needed, terminate.
	if len(out) == 1 {
		return nil, h, nd, nil
	}
	return out, h, nd, nil
}

// reconcileServer processes a client message as the SERVER (non-initiator) would,
// producing a reply message. It never extracts have/need (that is the client's
// job). This exists so the protocol can be validated deterministically without a
// live relay; against a real strfry the relay performs this role itself. When the
// reply is only the version byte the server has nothing left to send.
func (n *Negentropy) reconcileServer(query []byte) ([]byte, error) {
	out, _, _, err := n.reconcileAux(query, false)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// reconcileAux processes one message. When isInitiator is true it behaves as the
// client: IdList ranges are base cases from which the have/need diff is extracted
// and answered with Skip. When false it behaves as the server: IdList ranges are
// answered with the server's own IdList so the client can learn the difference.
func (n *Negentropy) reconcileAux(query []byte, isInitiator bool) (out []byte, have, need [][negIDSize]byte, err error) {
	n.lastTimestampIn = 0
	n.lastTimestampOut = 0

	r := &byteReader{buf: query}
	if r.remaining() == 0 {
		return nil, nil, nil, errors.New("negentropy: empty query")
	}
	ver, _ := r.readByte()
	if ver != negProtocolVersion {
		// Version negotiation: a single byte reply is the highest version we
		// support. Anything but V1 we cannot handle.
		return nil, nil, nil, fmt.Errorf("negentropy: unsupported protocol version 0x%02x (want 0x%02x)", ver, negProtocolVersion)
	}

	fullOutput := []byte{negProtocolVersion}

	var prevBound negBound
	prevIndex := 0
	skip := false

	for r.remaining() > 0 {
		var o []byte

		currBound, err := n.decodeBound(r)
		if err != nil {
			return nil, nil, nil, err
		}
		mode, err := r.readVarint()
		if err != nil {
			return nil, nil, nil, err
		}

		lower := prevIndex
		upper := n.findUpperBound(prevIndex, len(n.items), currBound)

		switch mode {
		case negModeSkip:
			skip = true
		case negModeFingerprint:
			theirFP, err := r.readBytes(negFingerprintSize)
			if err != nil {
				return nil, nil, nil, err
			}
			ourFP := n.fingerprint(lower, upper)
			if !bytesEqual(theirFP, ourFP) {
				if skip {
					n.encodeBound(&o, prevBound)
					appendVarint(&o, negModeSkip)
					skip = false
				}
				n.splitRange(lower, upper, currBound, &o)
			} else {
				skip = true
			}
		case negModeIDList:
			numIDs, err := r.readVarint()
			if err != nil {
				return nil, nil, nil, err
			}
			theirElems := make(map[[negIDSize]byte]bool, numIDs)
			for i := uint64(0); i < numIDs; i++ {
				idb, err := r.readBytes(negIDSize)
				if err != nil {
					return nil, nil, nil, err
				}
				var id [negIDSize]byte
				copy(id[:], idb)
				theirElems[id] = true
			}
			if isInitiator {
				// Client: IdList is a base case. Our items in [lower,upper) present on
				// their side => shared; absent => we have and they need. Whatever
				// remains on their side, they have and we need. Reply Skip.
				for i := lower; i < upper; i++ {
					id := n.items[i].ID
					if theirElems[id] {
						delete(theirElems, id)
					} else {
						have = append(have, id)
					}
				}
				for id := range theirElems {
					need = append(need, id)
				}
				skip = true
			} else {
				// Server: reply with OUR ids for this range so the client can diff.
				if skip {
					n.encodeBound(&o, prevBound)
					appendVarint(&o, negModeSkip)
					skip = false
				}
				n.encodeBound(&o, currBound)
				appendVarint(&o, negModeIDList)
				appendVarint(&o, uint64(upper-lower))
				for i := lower; i < upper; i++ {
					o = append(o, n.items[i].ID[:]...)
				}
			}
		default:
			return nil, nil, nil, fmt.Errorf("negentropy: unknown mode %d", mode)
		}

		fullOutput = append(fullOutput, o...)
		prevIndex = upper
		prevBound = currBound
	}

	return fullOutput, have, need, nil
}

// splitRange emits ranges covering items[lower:upper] with upper bound
// upperBound. Small ranges are emitted as a single IdList (base case); large
// ranges are split into negBuckets Fingerprint sub-ranges (the recursion).
func (n *Negentropy) splitRange(lower, upper int, upperBound negBound, o *[]byte) {
	numElems := upper - lower
	if numElems < negBuckets*2 {
		n.encodeBound(o, upperBound)
		appendVarint(o, negModeIDList)
		appendVarint(o, uint64(numElems))
		for i := lower; i < upper; i++ {
			*o = append(*o, n.items[i].ID[:]...)
		}
		return
	}
	itemsPerBucket := numElems / negBuckets
	bucketsWithExtra := numElems % negBuckets
	curr := lower
	for i := 0; i < negBuckets; i++ {
		bucketSize := itemsPerBucket
		if i < bucketsWithExtra {
			bucketSize++
		}
		fp := n.fingerprint(curr, curr+bucketSize)
		curr += bucketSize
		var nextBound negBound
		if curr == upper {
			nextBound = upperBound
		} else {
			nextBound = getMinimalBound(n.items[curr-1], n.items[curr])
		}
		n.encodeBound(o, nextBound)
		appendVarint(o, negModeFingerprint)
		*o = append(*o, fp...)
	}
}

// findUpperBound returns the first index in [first,last) whose record is NOT less
// than bound (binary search) — the exclusive upper index for the range.
func (n *Negentropy) findUpperBound(first, last int, bound negBound) int {
	lo, hi := first, last
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if n.itemLessThanBound(n.items[mid], bound) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// itemLessThanBound reports whether item < bound under (timestamp, id-prefix).
func (n *Negentropy) itemLessThanBound(item NegItem, bound negBound) bool {
	if item.Timestamp != bound.Timestamp {
		return item.Timestamp < bound.Timestamp
	}
	for i := 0; i < bound.IDLen; i++ {
		if item.ID[i] != bound.ID[i] {
			return item.ID[i] < bound.ID[i]
		}
	}
	// item shares the full idPrefix => item is >= bound (not less).
	return false
}

// fingerprint computes the negentropy fingerprint over items[lower:upper]: the
// mod-2^256 little-endian sum of ids, concatenated with the varint element count,
// SHA-256'd, truncated to 16 bytes.
func (n *Negentropy) fingerprint(lower, upper int) []byte {
	var acc [negIDSize]byte // little-endian 256-bit accumulator
	for i := lower; i < upper; i++ {
		addLE256(&acc, n.items[i].ID)
	}
	buf := make([]byte, 0, negIDSize+9)
	buf = append(buf, acc[:]...)
	appendVarint(&buf, uint64(upper-lower))
	sum := sha256.Sum256(buf)
	out := make([]byte, negFingerprintSize)
	copy(out, sum[:negFingerprintSize])
	return out
}

// getMinimalBound returns the shortest bound separating prev from curr (both
// records, prev < curr). If they differ in timestamp the prefix is empty;
// otherwise it is their common id-prefix plus one byte.
func getMinimalBound(prev, curr NegItem) negBound {
	if prev.Timestamp != curr.Timestamp {
		return negBound{Timestamp: curr.Timestamp}
	}
	shared := 0
	for i := 0; i < negIDSize; i++ {
		if curr.ID[i] != prev.ID[i] {
			break
		}
		shared++
	}
	b := negBound{Timestamp: curr.Timestamp, IDLen: shared + 1}
	copy(b.ID[:], curr.ID[:b.IDLen])
	return b
}

// --- timestamp / bound codec ------------------------------------------------

func (n *Negentropy) encodeTimestampOut(o *[]byte, timestamp uint64) {
	if timestamp == negInfinity {
		n.lastTimestampOut = negInfinity
		appendVarint(o, 0)
		return
	}
	delta := timestamp - n.lastTimestampOut
	n.lastTimestampOut = timestamp
	appendVarint(o, delta+1)
}

func (n *Negentropy) encodeBound(o *[]byte, b negBound) {
	n.encodeTimestampOut(o, b.Timestamp)
	appendVarint(o, uint64(b.IDLen))
	*o = append(*o, b.ID[:b.IDLen]...)
}

func (n *Negentropy) decodeTimestampIn(r *byteReader) (uint64, error) {
	v, err := r.readVarint()
	if err != nil {
		return 0, err
	}
	var timestamp uint64
	if v == 0 {
		timestamp = negInfinity
	} else {
		timestamp = v - 1
	}
	if n.lastTimestampIn == negInfinity || timestamp == negInfinity {
		n.lastTimestampIn = negInfinity
		return negInfinity, nil
	}
	timestamp += n.lastTimestampIn
	n.lastTimestampIn = timestamp
	return timestamp, nil
}

func (n *Negentropy) decodeBound(r *byteReader) (negBound, error) {
	ts, err := n.decodeTimestampIn(r)
	if err != nil {
		return negBound{}, err
	}
	l, err := r.readVarint()
	if err != nil {
		return negBound{}, err
	}
	if l > negIDSize {
		return negBound{}, fmt.Errorf("negentropy: bound id prefix length %d exceeds %d", l, negIDSize)
	}
	prefix, err := r.readBytes(int(l))
	if err != nil {
		return negBound{}, err
	}
	b := negBound{Timestamp: ts, IDLen: int(l)}
	copy(b.ID[:], prefix)
	return b, nil
}

// --- primitives -------------------------------------------------------------

// addLE256 computes acc = (acc + id) mod 2^256, both interpreted as 32-byte
// little-endian unsigned integers (byte 0 = least significant).
func addLE256(acc *[negIDSize]byte, id [negIDSize]byte) {
	var carry uint16
	for i := 0; i < negIDSize; i++ {
		s := uint16(acc[i]) + uint16(id[i]) + carry
		acc[i] = byte(s & 0xff)
		carry = s >> 8
	}
}

// appendVarint appends v as a negentropy varint (base-128, most significant digit
// first, high bit set on all but the last byte).
func appendVarint(o *[]byte, v uint64) {
	if v == 0 {
		*o = append(*o, 0)
		return
	}
	var tmp [10]byte
	i := len(tmp)
	// build least-significant-first, then reverse into MSB-first with continuation.
	var digits [10]byte
	nd := 0
	for v > 0 {
		digits[nd] = byte(v & 0x7f)
		v >>= 7
		nd++
	}
	i = 0
	for j := nd - 1; j >= 0; j-- {
		b := digits[j]
		if j != 0 {
			b |= 0x80
		}
		tmp[i] = b
		i++
	}
	*o = append(*o, tmp[:i]...)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// byteReader is a minimal cursor over a byte slice for protocol decoding.
type byteReader struct {
	buf []byte
	pos int
}

func (r *byteReader) remaining() int { return len(r.buf) - r.pos }

func (r *byteReader) readByte() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, errors.New("negentropy: unexpected end of message")
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *byteReader) readBytes(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.buf) {
		return nil, errors.New("negentropy: unexpected end of message")
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *byteReader) readVarint() (uint64, error) {
	var v uint64
	for {
		b, err := r.readByte()
		if err != nil {
			return 0, err
		}
		v = (v << 7) | uint64(b&0x7f)
		if b&0x80 == 0 {
			break
		}
	}
	return v, nil
}

// NegItemFromEvent builds a reconciliation record from a signed event: the
// event's created_at as the ordering timestamp and its 32-byte id.
func NegItemFromEvent(e *Event) (NegItem, error) {
	idb, err := hex.DecodeString(e.ID)
	if err != nil {
		return NegItem{}, fmt.Errorf("negentropy: decode event id: %w", err)
	}
	if len(idb) != negIDSize {
		return NegItem{}, fmt.Errorf("negentropy: event id must be %d bytes, got %d", negIDSize, len(idb))
	}
	var it NegItem
	it.Timestamp = uint64(e.CreatedAt)
	copy(it.ID[:], idb)
	return it, nil
}

// HexID returns the lowercase-hex form of a reconciliation id.
func HexID(id [negIDSize]byte) string { return hex.EncodeToString(id[:]) }
