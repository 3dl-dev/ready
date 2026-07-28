package sync

// EncodePortfolioKeyBlob (ready-4d9) — the `keys=` wire format.
//
// The vectors half of this file is a CROSS-LANGUAGE conformance check, not a
// self-consistency one: web/board/testdata/portfolio-key-vectors.json is
// committed, and web/board/src/lib/portfoliokeys.test.ts decodes the very same
// blobs. Go asserting encode(input)==blob and TypeScript asserting
// decode(blob)==input means neither side can move without going red, which is
// the only way two implementations of a URL format stay in agreement.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

type blobVector struct {
	Name   string                       `json:"name"`
	Boards map[string]map[string]string `json:"boards"`
	Blob   string                       `json:"blob"`
}

func loadBlobVectors(t *testing.T) []blobVector {
	t.Helper()
	path := filepath.Join("..", "..", "web", "board", "testdata", "portfolio-key-vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Vectors []blobVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(doc.Vectors) < 5 {
		t.Fatalf("%s has %d vectors — too few to be the committed conformance set", path, len(doc.Vectors))
	}
	return doc.Vectors
}

func (v blobVector) keys(t *testing.T) map[string]map[int][32]byte {
	t.Helper()
	out := map[string]map[int][32]byte{}
	for coord, epochs := range v.Boards {
		m := map[int][32]byte{}
		for epStr, h := range epochs {
			ep, err := strconv.Atoi(epStr)
			if err != nil {
				t.Fatalf("vector %q: epoch %q: %v", v.Name, epStr, err)
			}
			raw, err := hex.DecodeString(h)
			if err != nil || len(raw) != 32 {
				t.Fatalf("vector %q: cek %q is not 32 bytes of hex", v.Name, h)
			}
			var k [32]byte
			copy(k[:], raw)
			m[ep] = k
		}
		out[coord] = m
	}
	return out
}

// TestEncodePortfolioKeyBlob_MatchesCommittedVectors is the Go side of the
// cross-language contract.
func TestEncodePortfolioKeyBlob_MatchesCommittedVectors(t *testing.T) {
	for _, v := range loadBlobVectors(t) {
		got, err := EncodePortfolioKeyBlob(v.keys(t))
		if err != nil {
			t.Errorf("vector %q: %v", v.Name, err)
			continue
		}
		if got != v.Blob {
			t.Errorf("vector %q:\n got  %s\n want %s\nRegenerate with: go run ./web/board/testdata/genportfoliovectors > web/board/testdata/portfolio-key-vectors.json — but only after checking the browser decoder still agrees.", v.Name, got, v.Blob)
		}
	}
}

// TestEncodePortfolioKeyBlob_IsDeterministic: Go map iteration order is
// randomized per run, so an encoder that walked the map directly would emit a
// different link every invocation. A link is something users compare and re-mint;
// re-running the command on unchanged key material must produce the same bytes.
func TestEncodePortfolioKeyBlob_IsDeterministic(t *testing.T) {
	vs := loadBlobVectors(t)
	multi := vs[len(vs)-1] // the 24-board vector: the most map iteration to shuffle
	keys := multi.keys(t)
	first, err := EncodePortfolioKeyBlob(keys)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i := 0; i < 50; i++ {
		// Rebuild the map each time so Go re-randomizes its iteration order.
		again, err := EncodePortfolioKeyBlob(multi.keys(t))
		if err != nil {
			t.Fatalf("encode #%d: %v", i, err)
		}
		if again != first {
			t.Fatalf("encoding is not deterministic: run %d differs from run 0", i)
		}
	}
}

// TestEncodePortfolioKeyBlob_NoProperPrefixIsAValidEncoding is the ENCODER-SIDE
// statement of the anti-truncation property the browser decoder enforces.
//
// It is not a restatement of the decoder test (which is in TypeScript, over the
// real decoder). It asserts something only the encoder can be asked: that no
// truncation of a valid blob is itself the valid encoding of ANY portfolio the
// encoder could have been given. Concretely, every proper prefix of the 24-board
// blob is compared against the encoding of every 1..23-board sub-portfolio of it.
// If any prefix matched one, a truncated link would decode cleanly as a smaller
// portfolio and the reader would never know boards were missing — which is
// exactly the silent-partial failure this format exists to make impossible.
func TestEncodePortfolioKeyBlob_NoProperPrefixIsAValidEncoding(t *testing.T) {
	vs := loadBlobVectors(t)
	full := vs[len(vs)-1].keys(t)
	if len(full) < 10 {
		t.Fatalf("expected the last vector to be the big portfolio, got %d boards", len(full))
	}
	blob, err := EncodePortfolioKeyBlob(full)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(blob)
	if err != nil {
		t.Fatalf("decode own output: %v", err)
	}

	// Every prefix of the FULL portfolio, as raw bytes.
	prefixes := map[string]int{}
	for n := 0; n < len(raw); n++ {
		prefixes[string(raw[:n])] = n
	}

	// The encoding of every sub-portfolio obtainable by dropping boards. Board
	// order in the blob is deterministic (sorted), so "the first k boards" is the
	// shape a truncated link would most plausibly resemble.
	coords := make([]string, 0, len(full))
	for c := range full {
		coords = append(coords, c)
	}
	sort.Strings(coords)
	for k := 1; k < len(coords); k++ {
		sub := map[string]map[int][32]byte{}
		for _, c := range coords[:k] {
			sub[c] = full[c]
		}
		subBlob, err := EncodePortfolioKeyBlob(sub)
		if err != nil {
			t.Fatalf("encode %d-board sub-portfolio: %v", k, err)
		}
		subRaw, err := base64.RawURLEncoding.DecodeString(subBlob)
		if err != nil {
			t.Fatalf("decode sub-portfolio: %v", err)
		}
		if n, hit := prefixes[string(subRaw)]; hit {
			t.Errorf("the %d-board sub-portfolio encodes to exactly the first %d bytes of the %d-board blob — a link truncated there would decode as a complete smaller portfolio", k, n, len(coords))
		}
	}
}

// TestEncodePortfolioKeyBlob_RejectsWhatItCannotRepresent. Each of these is a
// case where quietly emitting a SHORTER portfolio would be the easy behaviour and
// the wrong one: the user would get a link that opens fewer boards than they
// asked for, with nothing said about it.
func TestEncodePortfolioKeyBlob_RejectsWhatItCannotRepresent(t *testing.T) {
	var cek [32]byte
	owner := "d2f6e78d61e5a57df834b1fa4b8ab8dff7f3ab4414fc6167698ad7f25b2568f5"

	t.Run("no boards at all", func(t *testing.T) {
		if _, err := EncodePortfolioKeyBlob(nil); err == nil {
			t.Error("encoding an empty portfolio succeeded — a keyless link must omit keys= entirely")
		}
	})

	t.Run("256 boards for one owner", func(t *testing.T) {
		boards := map[string]map[int][32]byte{}
		for i := 0; i < 256; i++ {
			boards[BoardCoord(owner, "board-"+strconv.Itoa(i))] = map[int][32]byte{1: cek}
		}
		if _, err := EncodePortfolioKeyBlob(boards); err == nil {
			t.Error("256 boards encoded — the u8 board count cannot hold that, so it must be an error, not a truncation")
		}
	})

	t.Run("a board d-tag longer than 255 bytes", func(t *testing.T) {
		long := make([]byte, 256)
		for i := range long {
			long[i] = 'x'
		}
		boards := map[string]map[int][32]byte{BoardCoord(owner, string(long)): {1: cek}}
		if _, err := EncodePortfolioKeyBlob(boards); err == nil {
			t.Error("a 256-byte d-tag encoded — the u8 length prefix cannot hold it")
		}
	})

	t.Run("a board with no epochs", func(t *testing.T) {
		boards := map[string]map[int][32]byte{BoardCoord(owner, "ready"): {}}
		if _, err := EncodePortfolioKeyBlob(boards); err == nil {
			t.Error("a board with zero epochs encoded — it carries no key and must not be claimed in the blob")
		}
	})

	t.Run("epoch 0", func(t *testing.T) {
		boards := map[string]map[int][32]byte{BoardCoord(owner, "ready"): {0: cek}}
		if _, err := EncodePortfolioKeyBlob(boards); err == nil {
			t.Error("epoch 0 encoded — cards seal under epoch >= 1")
		}
	})

	t.Run("a malformed board coordinate", func(t *testing.T) {
		boards := map[string]map[int][32]byte{"not-a-coordinate": {1: cek}}
		if _, err := EncodePortfolioKeyBlob(boards); err == nil {
			t.Error("a malformed coordinate encoded")
		}
	})

	t.Run("an owner pubkey that is not 32 bytes of hex", func(t *testing.T) {
		boards := map[string]map[int][32]byte{"30301:nothex:ready": {1: cek}}
		if _, err := EncodePortfolioKeyBlob(boards); err == nil {
			t.Error("a non-hex owner pubkey encoded")
		}
	})
}

// TestEncodePortfolioKeyBlob_TwentyFourBoardsFitInALink is the URL-LENGTH check
// this item calls out by name. Twenty-four hex-encoded 32-byte keys plus their
// coordinates would be well over 2 KB; the packed encoding has to stay inside
// what a browser address bar and a chat client will carry.
//
// The bound is deliberately a real number rather than "smaller than hex": a
// future format change that regressed size while still beating hex would sail
// past a relative assertion.
func TestEncodePortfolioKeyBlob_TwentyFourBoardsFitInALink(t *testing.T) {
	vs := loadBlobVectors(t)
	big := vs[len(vs)-1]
	if n := len(big.Boards); n != 24 {
		t.Fatalf("expected the last vector to be the 24-board portfolio, got %d boards", n)
	}
	blob, err := EncodePortfolioKeyBlob(big.keys(t))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	const budget = 1800
	if len(blob) > budget {
		t.Errorf("the 24-board keys= blob is %d characters, over the %d-character budget — the whole link (host + pk= + relays= + this) must stay comfortably inside what browsers and chat clients carry", len(blob), budget)
	}

	// The comparison that justifies the format: the same key material as the
	// board-scoped hex grammar this replaced (<boardD>:<epoch>:<64-hex> per
	// board, plus the owner coordinate once).
	hexLen := 0
	for coord, epochs := range big.keys(t) {
		_, boardD, ok := ParseBoardCoord(coord)
		if !ok {
			t.Fatalf("bad coord %q", coord)
		}
		for ep := range epochs {
			hexLen += len(boardD) + 1 + len(strconv.Itoa(ep)) + 1 + 64 + 1
		}
	}
	if len(blob) >= hexLen {
		t.Errorf("the packed blob (%d chars) is not smaller than the hex grammar it replaced (%d chars) — the compactness rationale in portfolioblob.go's header is false", len(blob), hexLen)
	}
	t.Logf("24-board keys= blob: %d chars (hex grammar would be ~%d)", len(blob), hexLen)
}
