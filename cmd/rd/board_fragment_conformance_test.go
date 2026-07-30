package main

// board_fragment.conformance.json IS THE CROSS-IMPLEMENTATION CHECK
// TestBoardCmd_ThisBoard_FragmentShapeMatchesBrowserParser (board_withkey_test.go)
// claimed to be but was not: that test asserts the fragment ownBoardURL prints
// against two regexes TYPED INTO THIS FILE — a Go-side belief about what
// web/board/src/lib/fragment.ts accepts, never checked against fragment.ts
// itself. A Go/TS grammar divergence went undetected for seven merges in one
// session before being caught by hand (fixed in b372bc7); a regex re-typed in
// Go cannot catch its own drift from the TypeScript it is impersonating.
//
// This file closes that gap the other way: ../../testdata/board_fragment.conformance.json
// is ONE committed file, and this test replays it through the REAL production
// function (ownBoardURL — the exact one `rd board`/`rd board --this-board`
// call) with the vector's fixed (coord, relays, viewer, ceks) inputs, asserting
// BYTE EQUALITY against the vector's `fragment`. web/board/src/lib/
// fragment.conformance.test.ts imports the SAME file and asserts the REAL
// parseFragment (fragment.ts) decodes that same `fragment` back into the
// vector's `expect`-equivalent fields. Neither side re-derives or
// re-approximates the other's grammar — an edit to either implementation that
// breaks the pairing fails at the specific vector, in whichever suite runs.
//
// No network, no signing, no relay: ownBoardURL is a pure string-formatting
// function over its four arguments, so this is (and should stay) a fast, fully
// deterministic unit test — the fixture's pubkey/CEK hex strings are
// placeholder bytes ("aaa...", "bbb...", ...), not real keys, because nothing
// here signs or verifies anything.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const boardFragmentConformancePath = "../../testdata/board_fragment.conformance.json"

type boardFragmentCEKVector struct {
	Epoch int    `json:"epoch"`
	Hex   string `json:"hex"`
}

type boardFragmentVector struct {
	Name     string                   `json:"name"`
	Note     string                   `json:"note"`
	Coord    string                   `json:"coord"`
	Relays   []string                 `json:"relays"`
	Viewer   *string                  `json:"viewer"`
	CEKs     []boardFragmentCEKVector `json:"ceks"`
	Fragment string                   `json:"fragment"`
}

type boardFragmentConformanceFile struct {
	Version int                   `json:"version"`
	Note    string                `json:"note"`
	Vectors []boardFragmentVector `json:"vectors"`
}

func loadBoardFragmentConformance(t *testing.T) boardFragmentConformanceFile {
	t.Helper()
	raw, err := os.ReadFile(boardFragmentConformancePath)
	if err != nil {
		t.Fatalf("read %s: %v", boardFragmentConformancePath, err)
	}
	var f boardFragmentConformanceFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal %s: %v", boardFragmentConformancePath, err)
	}
	if len(f.Vectors) == 0 {
		t.Fatalf("%s has no vectors — the committed fixture is empty or unreadable", boardFragmentConformancePath)
	}
	return f
}

// TestBoardFragmentConformanceVectors_MatchOwnBoardURL replays every vector in
// testdata/board_fragment.conformance.json through the REAL ownBoardURL and
// asserts the produced fragment is byte-identical to the committed value the
// TypeScript side also asserts against (fragment.conformance.test.ts).
func TestBoardFragmentConformanceVectors_MatchOwnBoardURL(t *testing.T) {
	file := loadBoardFragmentConformance(t)
	for _, v := range file.Vectors {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			var keys *boardKeyFragment
			if v.Viewer != nil || len(v.CEKs) > 0 {
				keys = &boardKeyFragment{ceks: map[int][32]byte{}}
				if v.Viewer != nil {
					keys.viewer = *v.Viewer
				}
				for _, c := range v.CEKs {
					cek, err := hexDecode32(c.Hex)
					if err != nil {
						t.Fatalf("vector %s: cek hex: %v", v.Name, err)
					}
					keys.ceks[c.Epoch] = cek
				}
			}
			got := ownBoardURL("", v.Coord, v.Relays, keys)
			got = strings.TrimPrefix(got, "#")
			if got != v.Fragment {
				t.Errorf("ownBoardURL produced a fragment that no longer matches the committed conformance vector.\ngot:  %s\nwant: %s\nThe TypeScript side (fragment.conformance.test.ts) asserts parseFragment decodes the COMMITTED value — if this Go emission genuinely changed on purpose, update testdata/board_fragment.conformance.json's fragment field (and re-derive it; do not hand-edit percent-encoding) and the TS assertions in the same commit.", got, v.Fragment)
			}
		})
	}
}

func hexDecode32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}
