// Whole-portfolio key material (ready-4d9) — the browser half of
// cmd/rd/board_portfolio.go's keys= blob. Read that file's header for the
// format's rationale; this file is its decoder, and the two must agree
// byte-for-byte (proven against Go-generated vectors in portfoliokeys.test.ts).
//
// WHY A BINARY BLOB RATHER THAN A LONGER cek= LIST. ready-df0's grammar,
// cek=<epoch>:<64-hex>[,...], extends naturally to board scoping — and that
// extension has a failure mode a 24-board link cannot afford. A comma-delimited
// list truncated in transit still parses, as a SHORTER well-formed list, and the
// reader opens a portfolio that looks complete while boards are silently
// missing. This format makes that impossible: every count is declared BEFORE the
// entries it governs and the buffer must be consumed EXACTLY, so no proper
// prefix of a valid blob is a valid blob. Every truncation is an error, and an
// error here surfaces as main.ts's "ask whoever shared it for a fresh link"
// notice — the honest outcome. portfoliokeys.test.ts proves that over EVERY
// prefix length, not a sampled few.
//
// It is also smaller than hex — 1552 characters for the real 24-board portfolio
// against ~1849 for the board-scoped hex grammar — but that is the SECOND
// reason, not the first, and both would fit a browser. The truncation property
// is what decided the format.
//
// FAIL CLOSED, LOUDLY. Every malformed input throws. Nothing here "skips the bad
// entry and keeps the good ones": a partially-decoded key set is exactly the
// silent-partial outcome above, and it is worse than no link, because the reader
// cannot tell which boards they are not seeing.

import type { FragmentKeys } from "./fragment";
import { boardCoord } from "./boarddiscovery";
import { bytesToHex } from "./sha256";

/** PORTFOLIO_BLOB_VERSION mirrors cmd/rd/board_portfolio.go's
 * portfolioBlobVersion. An unrecognized version rejects the whole link rather
 * than rendering an unknown fraction of it. */
export const PORTFOLIO_BLOB_VERSION = 1;

/**
 * PortfolioKeys maps a board COORDINATE ("30301:<owner>:<d>") to the key
 * material the link carries for that board, and nothing else.
 *
 * Keying on the full coordinate — not on the d-tag — is what makes cross-board
 * leakage unrepresentable at the type level: main.ts looks a board up by its own
 * coordinate, so there is no code path that hands board A's key to board B, and
 * no owner-collision between two boards that happen to share a d-tag.
 */
export type PortfolioKeys = ReadonlyMap<string, FragmentKeys>;

const BASE64URL = /^[A-Za-z0-9_-]*$/;

/** base64UrlToBytes decodes unpadded base64url to raw bytes.
 *
 * fragment.ts's base64UrlDecode cannot be reused: it finishes with a
 * TextDecoder, which is lossy for arbitrary bytes (the same hazard that made
 * pkg/sync WrapKey seal hex instead of raw key bytes). Key material must survive
 * this boundary intact, so it stays bytes the whole way. */
function base64UrlToBytes(s: string): Uint8Array {
  if (!BASE64URL.test(s)) throw new Error("fragment: keys= is not base64url");
  // A 4-character base64 group carries 3 bytes; a leftover of exactly one
  // character encodes no whole byte and cannot occur in any valid encoding.
  if (s.length % 4 === 1) throw new Error("fragment: keys= is truncated (invalid base64url length)");
  const padded = s + "=".repeat((4 - (s.length % 4)) % 4);
  const binary = atob(padded.replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

/** A cursor that CANNOT over-read. Every take() is bounds-checked and every
 * failure names the field, so a truncated link produces "keys= ended mid <x>"
 * rather than a silent undefined. */
class Reader {
  private i = 0;
  constructor(private readonly buf: Uint8Array) {}

  private need(n: number, what: string): void {
    if (this.i + n > this.buf.length) {
      throw new Error(`fragment: keys= is truncated — ended mid-${what} (needed ${n} more byte(s) at offset ${this.i} of ${this.buf.length})`);
    }
  }

  u8(what: string): number {
    this.need(1, what);
    return this.buf[this.i++];
  }

  u32(what: string): number {
    this.need(4, what);
    const b = this.buf;
    const i = this.i;
    this.i += 4;
    // >>> 0 keeps a value with the high bit set positive; a signed shift would
    // turn epoch 2^31 into a negative number and trip the epoch >= 1 check for
    // the wrong reason.
    return ((b[i] << 24) | (b[i + 1] << 16) | (b[i + 2] << 8) | b[i + 3]) >>> 0;
  }

  bytes(n: number, what: string): Uint8Array {
    this.need(n, what);
    const out = this.buf.slice(this.i, this.i + n);
    this.i += n;
    return out;
  }

  /** atEnd is the other half of the anti-truncation property: a blob with
   * TRAILING bytes is rejected too. Accepting extra bytes would mean two
   * different byte strings decode to the same key set, and it would let a
   * corrupted link pass as intact. */
  assertConsumed(): void {
    if (this.i !== this.buf.length) {
      throw new Error(`fragment: keys= has ${this.buf.length - this.i} trailing byte(s) — the link is malformed`);
    }
  }
}

const utf8 = new TextDecoder("utf-8", { fatal: true });

/**
 * decodePortfolioKeys parses the keys= fragment parameter into per-board key
 * material. Throws on anything malformed — see the module header.
 *
 * The returned map is keyed by board coordinate; a board appearing twice, an
 * epoch appearing twice within a board, an epoch below 1, a zero count, or a
 * boardD that is not valid UTF-8 are all rejected rather than resolved by
 * last-wins, because every one of them means the link is not what its minter
 * emitted.
 */
export function decodePortfolioKeys(param: string): PortfolioKeys {
  const r = new Reader(base64UrlToBytes(param));

  const version = r.u8("version");
  if (version !== PORTFOLIO_BLOB_VERSION) {
    throw new Error(`fragment: keys= is version ${version}, this page understands ${PORTFOLIO_BLOB_VERSION}`);
  }

  const out = new Map<string, FragmentKeys>();
  const ownerCount = r.u8("owner count");
  if (ownerCount === 0) throw new Error("fragment: keys= declares 0 owners — a link that carries no key must omit keys= entirely");

  for (let o = 0; o < ownerCount; o++) {
    const owner = bytesToHex(r.bytes(32, "owner pubkey"));
    const boardCount = r.u8("board count");
    if (boardCount === 0) throw new Error(`fragment: keys= declares 0 boards for owner ${owner}`);

    for (let b = 0; b < boardCount; b++) {
      const dLen = r.u8("board d-tag length");
      if (dLen === 0) throw new Error("fragment: keys= carries an empty board d-tag");
      const boardD = utf8.decode(r.bytes(dLen, "board d-tag"));
      const coord = boardCoord(owner, boardD);
      if (out.has(coord)) throw new Error(`fragment: keys= names board ${coord} twice`);

      const epochCount = r.u8("epoch count");
      if (epochCount === 0) throw new Error(`fragment: keys= declares 0 epochs for board ${coord}`);

      const ceks: { epoch: number; key: Uint8Array }[] = [];
      const seenEpochs = new Set<number>();
      for (let e = 0; e < epochCount; e++) {
        const epoch = r.u32("epoch");
        // Mirrors Go's DeriveBoardKeyring, which refuses any grant with
        // cek_epoch < 1 rather than binding a key to epoch 0.
        if (epoch < 1) throw new Error(`fragment: keys= carries epoch ${epoch} for board ${coord}; epochs start at 1`);
        if (seenEpochs.has(epoch)) throw new Error(`fragment: keys= names epoch ${epoch} twice for board ${coord}`);
        seenEpochs.add(epoch);
        ceks.push({ epoch, key: r.bytes(32, "content key") });
      }
      out.set(coord, { ceks });
    }
  }

  r.assertConsumed();
  return out;
}
