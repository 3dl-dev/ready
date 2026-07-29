// THE CROSS-IMPLEMENTATION CHECK cmd/rd/board_withkey_test.go's
// TestBoardCmd_ThisBoard_FragmentShapeMatchesBrowserParser claimed to be but
// was not: that Go test asserted the fragment `rd board --this-board` prints
// against two regexes TYPED INTO THAT GO FILE — a Go-side belief about this
// module's grammar, never checked against fragment.ts itself. A Go/TS
// divergence in this exact wire format went undetected for seven merges in one
// session before being caught by hand (fixed in b372bc7); a regex re-typed in
// Go cannot catch its own drift from the TypeScript it is impersonating.
//
// testdata/board_fragment.conformance.json (repo root) is ONE committed file.
// cmd/rd/board_fragment_conformance_test.go replays it through the REAL
// ownBoardURL (the exact function `rd board`/`rd board --this-board` call)
// with each vector's fixed (coord, relays, viewer, ceks) inputs and asserts
// BYTE EQUALITY against `fragment`. This file imports that SAME committed
// JSON — not a copy, not a re-typed literal — and asserts the REAL
// parseFragment decodes `fragment` back into the fields the vector declares.
// Neither side re-derives the other's grammar: an edit to either
// implementation that breaks the pairing fails at the specific vector, in
// whichever suite runs.
import { describe, expect, it } from "vitest";
import { parseFragment } from "./fragment";
import { hexToBytes, bytesToHex } from "./sha256";
// Static JSON import (tsconfig.json: resolveJsonModule) of the COMMITTED
// repo-root fixture — the same mechanism fold.vectors.test.ts uses for
// testdata/fold.vectors.json.
import fixtureJSON from "../../../../testdata/board_fragment.conformance.json";

interface CekVector {
  epoch: number;
  hex: string;
}

interface FragmentVector {
  name: string;
  note: string;
  coord: string;
  relays: string[];
  viewer: string | null;
  ceks: CekVector[];
  fragment: string;
}

interface FragmentConformanceFile {
  version: number;
  note: string;
  vectors: FragmentVector[];
}

const file = fixtureJSON as unknown as FragmentConformanceFile;

describe("board_fragment.conformance.json conformance", () => {
  it(`loads vectors from the committed fixture (format version ${file.version})`, () => {
    expect(file.version).toBe(1);
    expect(file.vectors.length).toBeGreaterThanOrEqual(4);
  });

  it.each(file.vectors)("$name", (v: FragmentVector) => {
    const parsed = parseFragment("#" + v.fragment);
    expect(parsed.kind).toBe("board");
    if (parsed.kind !== "board") throw new Error("unreachable");

    expect(parsed.board).toBe(v.coord);
    expect(parsed.relays).toEqual(v.relays);
    // No relay in any vector is ws://, so none should have been dropped as
    // mixed content — a real regression here would silently thin the relay
    // list and this equality would catch it.
    expect(parsed.droppedRelays).toBeUndefined();

    if (v.viewer === null) {
      expect(parsed.viewer).toBeUndefined();
    } else {
      expect(parsed.viewer).toBe(v.viewer);
    }

    if (v.ceks.length === 0) {
      expect(parsed.keys).toBeUndefined();
    } else {
      expect(parsed.keys).toBeDefined();
      const gotCeks = parsed.keys!.ceks
        .map((c) => ({ epoch: c.epoch, hex: bytesToHex(c.key) }))
        .sort((a, b) => a.epoch - b.epoch);
      const wantCeks = [...v.ceks].sort((a, b) => a.epoch - b.epoch);
      expect(gotCeks).toEqual(wantCeks);
      // Byte-level cross-check against the vector's own hex, independent of
      // bytesToHex/hexToBytes agreeing with each other.
      for (const want of v.ceks) {
        const got = parsed.keys!.ceks.find((c) => c.epoch === want.epoch);
        expect(got).toBeDefined();
        expect(got!.key).toEqual(hexToBytes(want.hex));
      }
      expect(parsed.keys!.ltk).toBeUndefined();
    }
  });
});
