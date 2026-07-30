// confwrite.emit.ts — the browser half of the CONFIDENTIAL write conformance
// loop (ready-191). It is a TOOL, not a test and not part of the app:
//
//	node_modules/.bin/vite-node src/board/confwrite.emit.ts <input.json>
//
// web/board/confidential_write_test.go is its only caller. That Go test mints
// the board, the CEK, the LTK and a REAL rd-authored confidential card
// (pkg/sync.BuildCardEvent with an Envelope), hands them here, and then folds
// whatever this prints back through pkg/sync.ProjectItems. So the round trip it
// closes is the full one:
//
//	rd seals → the browser opens → the browser seals → rd opens
//
// WHY A SEPARATE PROCESS AT ALL. testdata/write.vectors.json cannot pin a
// confidential write — its own known_gaps says so, because a sealed Content is
// nondeterministic (fresh nonce per seal) and not byte-comparable. The claim
// that matters is not "the bytes match a golden file", it is "the OTHER
// implementation reads back exactly what was meant", and the only way to assert
// that honestly is to run both implementations. This is the same shape as
// web/board/timestamp_encoding_test.go, which shells out to node to prove a JS
// engine reads the committed vector file the way the Go tests claim it does.
//
// NOTHING HERE IS APP CODE PATHS' STAND-IN: it calls the SHIPPED projectItems
// and the SHIPPED buildWrite, with no local re-implementation of either. If it
// ever grows a substitute for something in src/, this stops being a conformance
// test and becomes a self-portrait.
//
// It is under src/ so `npm run typecheck` covers it. It is not a *.test.ts, so
// vitest does not collect it, and nothing in index.html → main.ts imports it, so
// it is never bundled into dist/ — the same arrangement confidential.fixtures.ts
// (which also carries test-only key material) already uses.

import { readFileSync } from "node:fs";
import { projectItems } from "../lib/fold";
import type { NostrEvent } from "../lib/nostrevent";
import { hexToBytes } from "../lib/sha256";
import { buildWrite, type WriteEnv, type WriteOp } from "./writeevents";

interface Input {
  /** hex pubkey the Go side will sign the emitted events with. */
  signer: string;
  boardAuthor: string;
  boardD: string;
  boardCoord: string;
  /** the board CEK/LTK the Go side minted, hex.
   *
   * `ltkHex` EMPTY means the session holds no label-token key — the state a
   * grant that carries no `ltk` tag produces. It is not a degenerate case: rd's
   * own writer has the same state (`spec.Enc != nil && spec.Enc.LTK == nil`) and
   * both implementations must then emit NO `l` tag at all rather than leaking a
   * plaintext label. confidential_write_test.go's no-LTK test drives it. */
  cekHex: string;
  ltkHex: string;
  epoch: number;
  createdAt: number;
  /** the signed events the Go side already published (board + rd's own card). */
  snapshot: NostrEvent[];
  /** the intent to perform, in writeevents.ts's own vocabulary. */
  op: WriteOp;
}

const path = process.argv[2];
if (!path) {
  console.error("usage: vite-node confwrite.emit.ts <input.json>");
  process.exit(1);
}
const input = JSON.parse(readFileSync(path, "utf8")) as Input;

const cek = hexToBytes(input.cekHex);
const decryptor = {
  cek: (coord: string, epoch: number) => (coord === input.boardCoord && epoch === input.epoch ? cek : null),
};

// The projection the page would be looking at: rd's sealed cards, opened with
// the key this session holds. buildWrite rebuilds the WHOLE card from this, so
// if the decrypt failed here every item would be Redacted and the write would be
// refused — which is the correct outcome, and one the Go test asserts is NOT
// what happened by checking the free text survived.
const items = projectItems(input.snapshot, {
  trusted: null,
  maintainers: null,
  pinnedBoard: input.boardCoord,
  decryptor,
  encryptedBoards: null,
});

const env: WriteEnv = {
  signer: input.signer,
  boardAuthor: input.boardAuthor,
  boardD: input.boardD,
  boardTitle: input.boardD,
  items,
  issueEventIds: new Map(),
  createdAt: input.createdAt,
  confidential: true,
  enc: { cek, epoch: input.epoch, ltk: input.ltkHex === "" ? null : hexToBytes(input.ltkHex) },
};

const events = buildWrite(env, input.op);

// `projected` is what the BROWSER believes it wrote, reported so the Go test can
// compare its own fold against it. Two implementations agreeing on the same
// bytes is the whole assertion; one of them reporting its answer is how the
// other can say so.
const targetId = "itemId" in input.op ? input.op.itemId : input.op.id;
const before = items.get(targetId);
process.stdout.write(
  JSON.stringify({
    events,
    browser_view_before: before
      ? { title: before.title, context: before.context, redacted: before.redacted === true }
      : null,
  }),
);
