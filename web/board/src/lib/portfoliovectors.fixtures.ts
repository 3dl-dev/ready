// Loader for the cross-language `keys=` conformance vectors (ready-4d9).
//
// TEST-ONLY. Nothing in index.html -> main.ts imports this, so it is never
// bundled into dist/ (dist_test.go would notice if it were: it reads the built
// output and refuses any external origin, and node:fs is not something a browser
// bundle can carry).
//
// The vectors themselves are produced by the REAL Go encoder — see
// web/board/testdata/genportfoliovectors — and pinned from the Go side by
// pkg/sync/portfolioblob_test.go. Read portfoliokeys.test.ts's header for what
// the two-sided pinning buys.
//
// WHY process.cwd() AND NOT import.meta.url. Under the jsdom environment
// import.meta.url is an http: URL, so fileURLToPath refuses it. vitest always
// runs with the package root (web/board) as cwd, which resolves identically in
// both the node and jsdom environments — and fragment.test.ts needs jsdom while
// portfoliokeys.test.ts does not.

import { readFileSync } from "node:fs";
import { resolve } from "node:path";

export interface PortfolioKeyVector {
  name: string;
  /** board coordinate -> epoch (decimal string) -> 64-hex CEK */
  boards: Record<string, Record<string, string>>;
  /** the base64url blob pkg/sync.EncodePortfolioKeyBlob emits for `boards` */
  blob: string;
}

export const portfolioKeyVectors: PortfolioKeyVector[] = JSON.parse(
  readFileSync(resolve(process.cwd(), "testdata/portfolio-key-vectors.json"), "utf8"),
).vectors;
