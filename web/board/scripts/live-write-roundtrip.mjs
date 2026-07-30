#!/usr/bin/env node
// live-write-roundtrip.mjs — ready-b2b's GROUND-SOURCE PROOF.
//
// Each of the SEVEN board write operations is performed in a REAL Chromium,
// against the LIVE relay, and read back by an INDEPENDENT rd that has never
// seen the browser's events except through that relay.
//
//   1 status move (drag a card between columns)   5 re-prioritise
//   2 claim                                        6 label add / remove
//   3 close                                        7 gate approve / reject
//   4 retitle
//
// WHAT IS REAL HERE, AND WHAT IS NOT — stated plainly because the whole value
// of this script is that the claim it supports is exact.
//
//  REAL: the browser (headless Chromium over CDP, the shipped `vite build`
//  bundle served over HTTP), the signatures (BIP-340 secp256k1, validated
//  against the official test vectors in src/lib/schnorrsign.test.ts), the
//  relay (wss://relay.3dl.network, which verifies every signature and enforces
//  a write allowlist), and the reader (the real `rd` binary built from this
//  tree, in a CLEAN RD_HOME with an EMPTY log, so the relay is its only
//  possible source).
//
//  NOT REAL: the NIP-07 extension. Per ready-bff's ruling (2026-07-30, option
//  b + c) window.nostr is INJECTED over CDP by a genuine secp256k1 signer
//  instead of being supplied by nos2x/Alby. That exercises the page's write
//  path and the wire contract completely; it does NOT exercise the extension
//  handshake, which ready-35a carries as a one-run human release gate. Do not
//  report this script as proof of extension integration.
//
// WHY A FRESH READER PER OPERATION. The local log is append-only and retains
// superseded events forever, so reading back through the SAME log proves
// nothing about convergence — an entire confidential suite once stayed green
// while a rotation was deleting keys from the relay, because every test read
// the log instead of what the relay retains. Every verification below runs in a
// brand-new RD_HOME + project directory whose nostr-log.jsonl starts EMPTY and
// is filled only by `rd sync` from the relay.
//
// WHY THE OWNER'S KEY, ON A THROWAWAY BOARD. wss://relay.3dl.network enforces a
// tenant write-allowlist: an arbitrary fresh key is refused ("restricted:
// pubkey is not admitted"), so the signer must be a key that already holds
// write access. This script therefore reads the LOCAL machine's own rd signing
// key (the same file live-parity.mjs reads, for the same reason: a one-time,
// same-machine verification) and confines every event it writes to a FRESH,
// PUBLIC, per-run board — never the real project board. The secret is held in
// memory, injected into the page's signer, and is never logged, printed or
// written anywhere by this script.
//
// TWO BOARD MODES, AND WHY BOTH ARE RUN (ready-191). This script's header used
// to say "WHY A PUBLIC BOARD: rd seals free text on a confidential board and
// this page has no seal path, so the browser REFUSES to write to a confidential
// board". That was true when ready-b2b shipped and it is FALSE NOW: ready-191
// gave the page the seal half of the envelope (src/lib/envelope.ts's SEAL half,
// src/board/writeevents.ts's enc branches), so a confidential board is writable
// from the browser exactly while the session HOLDS the board's CEK. The refusal
// did not go away — it narrowed to `confidential && !enc`, which is still the
// only honest answer when no key reached the page.
//
//   node scripts/live-write-roundtrip.mjs                 -> `rd init --public`
//   node scripts/live-write-roundtrip.mjs --confidential  -> plain `rd init`
//
// `--public` is the mode testdata/write.vectors.json pins, so the default run
// stays the one that lines up with the deterministic vectors. `--confidential`
// is the mode `rd init` DEFAULTS to — i.e. every real project board — and it is
// the only run that can support ready-191's done condition, which says "a real
// browser" and "an independent rd decrypts it". What the confidential run adds
// on top of the seven operations:
//
//   - the injected signer grows a REAL NIP-44 v2 `nip44.decrypt` (lib/nip44ref.ts,
//     validated against the official spec vectors), because that is the only way
//     a CEK can reach the page: the board client cannot do ECDH, so it asks the
//     signer to open the owner-signed grant (src/lib/keyunwrap.ts's header);
//   - the independent reader is a SEPARATE key that the board owner `rd grant`s,
//     so the read-back genuinely DECRYPTS rather than being the writer reading
//     its own log — a keyless reader is asserted separately, and sees nothing;
//   - the published card is inspected ON THE WIRE out of the reader's own synced
//     log: no clear `title`/`waiting_on` tag, the enc + cek_epoch markers present,
//     and no free text the browser authored anywhere in the serialized event;
//   - the browser's `l` label token is compared to the token RD'S OWN WRITER
//     emitted for the SAME label on the SAME board — the live form of the
//     cross-implementation LTK agreement web/board/confidential_write_test.go
//     asserts in-process.
//
// THIS IS A MANUAL PROOF, NOT A CI JOB. It needs the live relay, a Chromium on
// disk, and the local machine's allowlisted rd key — none of which CI has. The
// deterministic half of the same guarantee DOES run in CI:
// src/board/writeevents.vectors.test.ts (the exact events, against rd's own
// vector file) and src/board/nostrwriter.test.ts (every refusal, and the
// relay-rejection path against a scripted OK frame).
//
// Usage:
//   node scripts/live-write-roundtrip.mjs [--keep] [--relay wss://…]
// Exits non-zero on the FIRST operation that does not converge.
//
// DEPENDENCIES: node, go, npx vite (devDependency), and esbuild — which is
// vite's own hard dependency and is therefore always present in node_modules
// alongside it; it is imported here rather than added as a second declared
// dependency at a version that could drift from the one vite pins.

import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { createServer } from "vite";

const BOARD_DIR = path.resolve(import.meta.dirname, "..");
const REPO_ROOT = path.resolve(BOARD_DIR, "../..");
const CHROME =
  process.env.CHROME_PATH ?? path.join(os.homedir(), ".cache/ms-playwright/chromium-1228/chrome-linux64/chrome");

const argv = process.argv.slice(2);
const KEEP = argv.includes("--keep");
const RELAY = argv.includes("--relay") ? argv[argv.indexOf("--relay") + 1] : "wss://relay.3dl.network";
/** ready-191: run the whole proof against a CONFIDENTIAL board (plain `rd init`,
 * the default mode) instead of a `--public` one. See this file's header. */
const CONFIDENTIAL = argv.includes("--confidential");

const RUN = Date.now().toString(36);
const BOARD_D = `${CONFIDENTIAL ? "c191live" : "b2blive"}${RUN}`;

/** The label BOTH implementations attach, on different items of the same board.
 * On a confidential board the `l` tag is an HMAC token under the board LTK, so
 * rd's token and the browser's token for this one string must be byte-identical
 * or the relay-side `#l` equality filter silently stops matching across writers. */
const SHARED_LABEL = "browser-written";

const log = (...a) => console.log(...a);
const step = (s) => console.log(`\n── ${s}`);

// ── tiny CDP client (no puppeteer: one socket, ids in, results out) ─────────

class CDP {
  constructor(ws) {
    this.ws = ws;
    this.next = 1;
    this.pending = new Map();
    this.sessionId = undefined;
    ws.onmessage = (m) => {
      const msg = JSON.parse(m.data);
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) reject(new Error(`${msg.error.message} (${JSON.stringify(msg.error.data ?? "")})`));
        else resolve(msg.result);
      }
    };
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => {
      ws.onopen = res;
      ws.onerror = (e) => rej(new Error(`CDP connect: ${e.message ?? "failed"}`));
    });
    return new CDP(ws);
  }
  send(method, params = {}) {
    const id = this.next++;
    const frame = { id, method, params };
    if (this.sessionId) frame.sessionId = this.sessionId;
    this.ws.send(JSON.stringify(frame));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }
  /** evaluate runs an async expression in the page and returns its value. */
  async evaluate(expression) {
    const r = await this.send("Runtime.evaluate", {
      expression: `(async () => { ${expression} })()`,
      awaitPromise: true,
      returnByValue: true,
    });
    if (r.exceptionDetails) {
      throw new Error(`page: ${r.exceptionDetails.exception?.description ?? r.exceptionDetails.text}`);
    }
    return r.result.value;
  }
}

// REAL WALL-CLOCK WAITS ONLY. --virtual-time-budget races past the WebSocket
// handshake and falsely reports a relay timeout (proven in M0); this script
// never uses it.
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitFor(cdp, expr, what, timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await cdp.evaluate(`return !!(${expr});`)) return;
    if (Date.now() > deadline) {
      const dump = await cdp.evaluate("return document.body.innerText.slice(0, 900);");
      throw new Error(`timed out waiting for ${what}\n--- page text ---\n${dump}`);
    }
    await sleep(500);
  }
}

// ── rd helpers ──────────────────────────────────────────────────────────────

function rd(bin, cwd, home, args) {
  return execFileSync(bin, args, {
    cwd,
    env: { ...process.env, RD_HOME: home, RD_NOSTR_RELAY_URL: RELAY },
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
}

/**
 * independentRead is the whole point of this script. It builds a reader that
 * has NEVER seen the browser's events: a brand-new RD_HOME and a brand-new
 * project directory whose nostr-log.jsonl does not exist until `rd sync` pulls
 * it from the relay. Trust resolves to the board author, whose 30301 board and
 * 39301 self-grant are themselves fetched from the relay.
 *
 * `readerSecret` (ready-191, confidential runs): the reader's IDENTITY, as
 * opposed to its log, cannot be fresh on a confidential board — a CEK is
 * NIP-44-wrapped TO a pubkey, so a key nobody granted decrypts nothing and the
 * read-back would be a wall of [encrypted] that proves only that the envelope
 * is fail-closed. So the confidential runs pin ONE separate key, `rd grant` it
 * from the owner ONCE, and give every fresh reader that same identity. The log
 * is still empty every time, which is the independence that matters here: the
 * relay is the only possible source of every event the reader projects. A
 * reader given NO secret is genuinely keyless, and the keyless case is asserted
 * on its own below (it must see the placeholder, never the free text).
 */
function independentRead(bin, tmp, coord, tag, readerSecret) {
  const home = path.join(tmp, `reader-${tag}-home`);
  const dir = path.join(tmp, `reader-${tag}`);
  mkdirSync(home, { recursive: true });
  if (readerSecret) {
    writeFileSync(
      path.join(home, "nostr-identity.json"),
      JSON.stringify({ version: 1, secret_hex: readerSecret.secret, pubkey_hex: readerSecret.pubkey }),
      { mode: 0o600 },
    );
  }
  mkdirSync(path.join(dir, ".ready"), { recursive: true });
  writeFileSync(
    path.join(dir, ".ready", "config.json"),
    JSON.stringify(
      {
        project_name: `reader-${tag}`,
        board: coord,
        public: !CONFIDENTIAL,
        relay_endpoints: [{ url: RELAY, read: true, write: false }],
      },
      null,
      2,
    ),
  );
  if (existsSync(path.join(dir, ".ready", "nostr-log.jsonl"))) {
    throw new Error("reader log is not empty — this reader would not be independent");
  }
  rd(bin, dir, home, ["sync"]);
  const out = rd(bin, dir, home, ["list", "--json", "--all"]);
  const parsed = JSON.parse(out);
  const items = Array.isArray(parsed) ? parsed : (parsed.items ?? []);
  return new Map(items.map((i) => [i.id, i]));
}

/** readerEvents returns every event of `kind` addressing `itemId` from a
 * reader's freshly-synced log — i.e. the SERIALIZED BYTES the relay retains and
 * handed back, not a projection of them. ready-191's done condition is asserted
 * on these: "no plaintext title/context/label ever on the wire" is a claim about
 * the event, and a projection that renders the right title says nothing about
 * it. */
function readerEvents(tmp, tag, itemId, kind) {
  const file = path.join(tmp, `reader-${tag}`, ".ready", "nostr-log.jsonl");
  const out = [];
  for (const line of readFileSync(file, "utf8").split("\n")) {
    if (line.trim() === "") continue;
    const e = JSON.parse(line);
    if (e.kind !== kind) continue;
    if (!e.tags?.some((t) => t[0] === "d" && t[1] === itemId)) continue;
    out.push(e);
  }
  return out;
}

const tagValues = (ev, name) => (ev.tags ?? []).filter((t) => t[0] === name).map((t) => t[1]);

/**
 * readerStatusStamps returns the created_at of every NIP-34 STATUS event for
 * itemId in a reader's freshly-synced log — i.e. what the RELAY retains.
 *
 * STATUS events, not cards: a 30302 card is ADDRESSABLE (replaceable), so a
 * relay keeps only the newest one per (kind, author, d) and a card chain can
 * never show more than one stamp. The 1630/1631/1632 status chain is
 * append-only, which is what makes the §17.2 monotonic bump observable end to
 * end instead of only inside the writer.
 */
function readerStatusStamps(tmp, tag, itemId) {
  const file = path.join(tmp, `reader-${tag}`, ".ready", "nostr-log.jsonl");
  const out = [];
  for (const line of readFileSync(file, "utf8").split("\n")) {
    if (line.trim() === "") continue;
    const e = JSON.parse(line);
    if (e.kind !== 1630 && e.kind !== 1631 && e.kind !== 1632) continue;
    if (!e.tags?.some((t) => t[0] === "d" && t[1] === itemId)) continue;
    out.push(e.created_at);
  }
  return out.sort((a, b) => a - b);
}

/** mintKey draws a fresh secp256k1 key through the SAME signer module the page
 * uses, so a key minted here and a key used there cannot disagree. */
async function mintKey(vite) {
  const mod = await vite.ssrLoadModule("/src/lib/schnorrsign.ts");
  const secret = mod.randomSecretHex();
  return { secret, pubkey: mod.xOnlyPubkey(secret) };
}

/** directRelayPublish signs a card for `coord` with `secret` and offers it to
 * the relay directly, returning the relay's own OK frame. This is the SEPARATE
 * relay-side refusal of done condition 6 — the client-side refusal is observed
 * in the page, and one is not evidence of the other. */
async function directRelayPublish(vite, secret, coord) {
  const mod = await vite.ssrLoadModule("/src/lib/schnorrsign.ts");
  const ev = mod.signNostrEvent(
    {
      created_at: Math.floor(Date.now() / 1000),
      kind: 30302,
      tags: [
        ["d", `ungranted-probe-${RUN}`],
        ["title", "ungranted write probe"],
        ["a", coord],
        ["s", "inbox"],
      ],
      content: "",
    },
    secret,
  );
  const ws = new WebSocket(RELAY);
  return new Promise((resolve) => {
    const t = setTimeout(() => resolve({ accepted: null, message: "timed out" }), 25000);
    ws.onopen = () => ws.send(JSON.stringify(["EVENT", ev]));
    ws.onmessage = (m) => {
      const f = JSON.parse(m.data);
      if (f[0] === "OK") {
        clearTimeout(t);
        resolve({ accepted: f[2], message: f[3] ?? "" });
        try {
          ws.close();
        } catch {
          /* closed */
        }
      }
    };
    ws.onerror = () => {
      clearTimeout(t);
      resolve({ accepted: null, message: "connection failed" });
    };
  });
}

/** injectedSignerJs bundles the REAL secp256k1 signer as an IIFE that installs
 * window.nostr before any page script runs. See this file's header for exactly
 * what this does and does not prove.
 *
 * THE nip44 NAMESPACE (ready-191) is what makes a confidential run possible at
 * all. The page cannot do ECDH — the secret key never enters it — so the ONLY
 * way a board CEK reaches it is `window.nostr.nip44.decrypt(ownerPubkey, wrap)`
 * over the owner-signed 39301 grant (src/lib/keyunwrap.ts's header). The crypto
 * underneath is src/lib/nip44ref.ts, which is validated against the official
 * NIP-44 v2 test vectors (nip44ref.test.ts), and the UTF-8 string boundary every
 * real extension has is reproduced exactly (TextDecoder, non-fatal) rather than
 * shortcut — that boundary is why pkg/sync/keydist.go seals hex, and a shortcut
 * here would hide a regression in it. */
async function injectedSignerJs(esbuild, secretHex) {
  const built = await esbuild.build({
    absWorkingDir: BOARD_DIR,
    stdin: {
      contents: `
        import { signNostrEvent, xOnlyPubkey } from "./src/lib/schnorrsign";
        import { open as nip44Open } from "./src/lib/nip44ref";
        const SECRET = "__SECRET__";
        window.nostr = {
          async getPublicKey() { return xOnlyPubkey(SECRET); },
          async signEvent(e) {
            return signNostrEvent(
              { created_at: e.created_at, kind: e.kind, tags: e.tags, content: e.content },
              SECRET,
            );
          },
          nip44: {
            async decrypt(counterpartyPubHex, ciphertext) {
              // Throws on a MAC failure / wrong recipient, exactly as an
              // extension does; the page treats that as "no such key".
              return new TextDecoder("utf-8").decode(nip44Open(SECRET, counterpartyPubHex, ciphertext));
            },
          },
        };
      `,
      resolveDir: BOARD_DIR,
      loader: "ts",
    },
    bundle: true,
    write: false,
    format: "iife",
    target: "es2022",
  });
  return built.outputFiles[0].text.replace("__SECRET__", secretHex);
}

/** openBoard (re)loads the board as a given identity: install that identity's
 * signer, navigate, click the NIP-07 login, wait for the board. */
async function openBoard(cdp, origin, coord, esbuild, secretHex, opts = {}) {
  // Replace the previously-installed signer rather than stacking another one,
  // so "which identity is this page acting as" is never ambiguous.
  if (openBoard.scriptId) {
    await cdp.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: openBoard.scriptId });
  }
  const { identifier } = await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
    source: await injectedSignerJs(esbuild, secretHex),
  });
  openBoard.scriptId = identifier;

  // A CROSS-DOCUMENT navigation, deliberately: the app strips its own fragment
  // with replaceState on load, so a fragment-only navigation followed by a
  // reload can race and reload the STRIPPED url — which silently drops the
  // board coordinate and lands on own-boards discovery ("No boards found") for
  // an identity that owns nothing. about:blank first guarantees a real load.
  const url = `${origin}/board/#board=${encodeURIComponent(coord)}&relays=${encodeURIComponent(RELAY)}`;
  await cdp.send("Page.navigate", { url: "about:blank" });
  await sleep(400);
  await cdp.send("Page.navigate", { url });
  await waitFor(cdp, `document.querySelector("button")`, "the login page", 40000);
  await cdp.evaluate(`
    const btn = [...document.querySelectorAll("button")].find((b) => /extension/i.test(b.textContent ?? ""));
    if (!btn) throw new Error("no NIP-07 login button");
    if (btn.disabled) throw new Error("the page did not see the injected window.nostr");
    btn.click();
    return true;
  `);
  if (opts.expectCards !== false) {
    await waitFor(cdp, `document.querySelectorAll(".card").length > 0`, "the board's cards", 90000);
  } else {
    await waitFor(
      cdp,
      `document.querySelector(".card") || document.querySelector(".awaiting-authorization") || document.querySelector(".notice")`,
      "the board or an authorization notice",
      90000,
    );
  }
  return { cards: await cdp.evaluate("return document.querySelectorAll('.card').length;") };
}

// ── the seven operations, each performed IN THE PAGE ────────────────────────

/** selectCard clicks the card so the detail pane (which carries five of the
 * seven affordances) renders for it. */
async function selectCard(cdp, id) {
  await cdp.evaluate(`
    const card = [...document.querySelectorAll(".card")].find((c) =>
      c.querySelector(".card-id")?.textContent?.trim() === ${JSON.stringify(id)});
    if (!card) throw new Error("no card for ${id}");
    card.click();
    return true;
  `);
  await waitFor(cdp, `document.querySelector(".detail-actions")`, `detail pane for ${id}`, 10000);
}

/** A real HTML5 drag: dragstart on the card, drop on the target column, with a
 * genuine DataTransfer carrying the payload the drop handler reads. */
async function dragCardToColumn(cdp, id, column) {
  return cdp.evaluate(`
    const card = [...document.querySelectorAll(".card")].find((c) =>
      c.querySelector(".card-id")?.textContent?.trim() === ${JSON.stringify(id)});
    if (!card) throw new Error("no card for ${id}");
    const col = document.querySelector('.column[data-column=${JSON.stringify(column)}]');
    if (!col) throw new Error("no column ${column}");
    const dt = new DataTransfer();
    card.dispatchEvent(new DragEvent("dragstart", { bubbles: true, dataTransfer: dt }));
    col.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
    return true;
  `);
}

async function clickIn(cdp, selector) {
  return cdp.evaluate(`
    const n = document.querySelector(${JSON.stringify(selector)});
    if (!n) throw new Error("no element " + ${JSON.stringify(selector)});
    n.click();
    return true;
  `);
}

/** settle waits until the page has finished the write it just started: either
 * the transient error appeared (a refusal) or one poll cycle passed with none.
 * The publish itself is awaited inside the page's own promise chain. */
async function settle(cdp, ms = 9000) {
  await sleep(ms);
  return cdp.evaluate(`return document.querySelector(".transient-error")?.textContent ?? "";`);
}

async function main() {
  if (!existsSync(CHROME)) throw new Error(`no Chromium at ${CHROME} (set CHROME_PATH)`);

  const tmp = mkdtempSync(path.join(os.tmpdir(), "rd-b2b-"));
  const cleanup = [];
  let failures = 0;

  try {
    step("build rd from this tree");
    const rdBin = path.join(tmp, "rd");
    execFileSync("go", ["build", "-o", rdBin, "./cmd/rd"], { cwd: REPO_ROOT, stdio: "inherit" });

    // Stood up BEFORE the board is provisioned (ready-191) because mintKey draws
    // keys through it, and a confidential run needs the independent reader's key
    // to exist before `rd grant` can address the CEK to it.
    step("prepare the injected signer (REAL secp256k1 — see this file's header)");
    const vite = await createServer({
      root: BOARD_DIR,
      configFile: false,
      logLevel: "error",
      server: { middlewareMode: true },
      appType: "custom",
      optimizeDeps: { noDiscovery: true },
    });
    cleanup.push(() => vite.close());
    const esbuild = (await import("esbuild")).default ?? (await import("esbuild"));

    step(`provision the throwaway board (owner key, ${CONFIDENTIAL ? "CONFIDENTIAL" : "PUBLIC"}, fresh board-d)`);
    const writerHome = path.join(tmp, "writer-home");
    mkdirSync(writerHome, { recursive: true });
    const idPath = path.join(
      process.env.RD_HOME ?? path.join(process.env.XDG_CONFIG_HOME ?? path.join(os.homedir(), ".config"), "rd"),
      "nostr-identity.json",
    );
    const identity = JSON.parse(readFileSync(idPath, "utf8"));
    writeFileSync(path.join(writerHome, "nostr-identity.json"), JSON.stringify(identity), { mode: 0o600 });

    const projectDir = path.join(tmp, BOARD_D);
    mkdirSync(projectDir, { recursive: true });
    const initOut = JSON.parse(
      rd(rdBin, projectDir, writerHome, [
        "init",
        // ready-191: plain `rd init` IS the confidential mode — omitting the flag
        // is the whole difference, and it is the mode every real project board
        // is created in.
        ...(CONFIDENTIAL ? [] : ["--public"]),
        "--no-commit-binding",
        "--relay",
        RELAY,
        "--name",
        BOARD_D,
        "--json",
      ]),
    );
    const coord = initOut.board;
    const owner = initOut.owner;
    log(`  board ${coord}`);

    // THE INDEPENDENT READER'S KEY (confidential runs only). Minted here, granted
    // by the owner below, and used by every independentRead() call for the rest of
    // the run. It is NOT the browser's key and NOT the owner's: the read-back has
    // to be somebody else decrypting, or it proves nothing about the wire.
    const readerKey = CONFIDENTIAL ? await mintKey(vite) : undefined;

    step("seed the items each operation acts on (rd's OWN writer, published to the relay)");
    const ids = {
      move: `${BOARD_D}-move`,
      claim: `${BOARD_D}-claim`,
      close: `${BOARD_D}-close`,
      title: `${BOARD_D}-title`,
      prio: `${BOARD_D}-prio`,
      label: `${BOARD_D}-label`,
      gateOk: `${BOARD_D}-gateok`,
      gateNo: `${BOARD_D}-gateno`,
      rapid: `${BOARD_D}-rapid`,
      reject: `${BOARD_D}-reject`,
      // ready-191: written by RD, carrying the SAME label the browser adds to
      // ids.label below. On a confidential board the two `l` tags must be the
      // identical HMAC token — see the comparison after the seven operations.
      rdlabel: `${BOARD_D}-rdlabel`,
    };
    // The shared label has to exist in the project registry before rd will
    // attach it. The BROWSER's label-add path does not consult the registry, so
    // this is a precondition of rd's half of the comparison only.
    rd(rdBin, projectDir, writerHome, ["label", "define", SHARED_LABEL, "--description", "written from a browser"]);
    for (const [k, id] of Object.entries(ids)) {
      rd(rdBin, projectDir, writerHome, [
        "create",
        `browser write proof: ${k}`,
        "--id",
        id,
        "--type",
        "task",
        "--priority",
        "p2",
        ...(k === "rdlabel" ? ["--label", SHARED_LABEL] : []),
      ]);
    }
    rd(rdBin, projectDir, writerHome, ["claim", ids.gateOk, "--reason", "for the gate"]);
    rd(rdBin, projectDir, writerHome, ["claim", ids.gateNo, "--reason", "for the gate"]);
    rd(rdBin, projectDir, writerHome, [
      "gate",
      ids.gateOk,
      "--gate-type",
      "design",
      "--description",
      "needs a browser ruling",
    ]);
    rd(rdBin, projectDir, writerHome, [
      "gate",
      ids.gateNo,
      "--gate-type",
      "design",
      "--description",
      "needs a browser ruling",
    ]);
    if (readerKey) {
      // The owner wraps this board's CEK to the reader's pubkey. `rd grant` IS
      // the whole invite — there is no relay-side admission involved.
      rd(rdBin, projectDir, writerHome, ["grant", readerKey.pubkey]);
    }
    rd(rdBin, projectDir, writerHome, ["relay", "flush"]);

    step("BASELINE: an independent reader sees the seeds through the relay only");
    const baseline = independentRead(rdBin, tmp, coord, "baseline", readerKey);
    for (const id of Object.values(ids)) {
      if (!baseline.has(id)) throw new Error(`seed ${id} did not reach the relay — the proof cannot start`);
    }
    log(`  ${baseline.size} items read back independently`);

    if (CONFIDENTIAL) {
      // PRECONDITIONS FOR THE WHOLE CONFIDENTIAL RUN, checked before a single
      // browser write, so a later failure can never be blamed on a board that
      // was not really confidential or a reader that could not really decrypt.
      const seed = baseline.get(ids.move);
      if (!seed || seed.title !== "browser write proof: move") {
        throw new Error(
          `the granted reader could not DECRYPT rd's own seeded card (title=${JSON.stringify(seed?.title)}) — ` +
            "the grant did not take, and nothing below would prove anything",
        );
      }
      const blind = independentRead(rdBin, tmp, coord, `keyless-${RUN}`);
      const blindSeed = blind.get(ids.move);
      if (!blindSeed) throw new Error("the KEYLESS reader lost the item entirely — expected a placeholder, not absence");
      if (blindSeed.title === seed.title) {
        throw new Error(
          "a reader holding NO grant read the seeded title in the clear — this board is not confidential, " +
            "so every no-plaintext claim below would be vacuous",
        );
      }
      log(`  ANTI-TAUTOLOGY ok: granted reader sees ${JSON.stringify(seed.title)}, keyless reader sees ${JSON.stringify(blindSeed.title)}`);
    }

    step("build the shipped bundle and serve it");
    execFileSync("npx", ["vite", "build", "--outDir", path.join(tmp, "dist"), "--emptyOutDir"], {
      cwd: BOARD_DIR,
      stdio: "inherit",
    });
    const dist = path.join(tmp, "dist");
    // The production bundle is built with base "/board/" (it is hosted at
    // ready.3dl.dev/board), so the local server mounts dist at that same path —
    // serving it at "/" would 404 every asset the shipped index.html asks for.
    const server = http.createServer((req, res) => {
      let rel = decodeURIComponent((req.url ?? "/").split("?")[0].split("#")[0]);
      if (rel.startsWith("/board")) rel = rel.slice("/board".length) || "/";
      const file = path.join(dist, rel === "/" ? "index.html" : rel);
      if (!file.startsWith(dist) || !existsSync(file)) {
        res.writeHead(404).end("not found");
        return;
      }
      const type = file.endsWith(".js")
        ? "text/javascript"
        : file.endsWith(".css")
          ? "text/css"
          : "text/html";
      res.writeHead(200, { "content-type": type }).end(readFileSync(file));
    });
    await new Promise((r) => server.listen(0, "127.0.0.1", r));
    cleanup.push(() => server.close());
    const origin = `http://127.0.0.1:${server.address().port}`;

    step("launch Chromium and open the board");
    const chrome = spawn(
      CHROME,
      [
        "--headless=new",
        "--remote-debugging-port=0",
        "--no-sandbox",
        "--disable-gpu",
        `--user-data-dir=${path.join(tmp, "chrome-profile")}`,
        "about:blank",
      ],
      { stdio: ["ignore", "pipe", "pipe"] },
    );
    cleanup.push(() => chrome.kill());
    const wsUrl = await new Promise((resolve, reject) => {
      let buf = "";
      const t = setTimeout(() => reject(new Error("chromium did not print a devtools url")), 30000);
      chrome.stderr.on("data", (d) => {
        buf += d.toString();
        const m = buf.match(/ws:\/\/127\.0\.0\.1:\d+\/devtools\/browser\/[a-f0-9-]+/);
        if (m) {
          clearTimeout(t);
          resolve(m[0]);
        }
      });
    });
    const browser = await CDP.connect(wsUrl);
    const { targetId } = await browser.send("Target.createTarget", { url: "about:blank" });
    const { sessionId } = await browser.send("Target.attachToTarget", { targetId, flatten: true });
    browser.sessionId = sessionId;
    const cdp = browser;
    await cdp.send("Page.enable");
    await cdp.send("Runtime.enable");

    const opened = await openBoard(cdp, origin, coord, esbuild, identity.secret_hex);
    log(`  ${opened.cards} cards rendered`);

    // ── the seven operations ────────────────────────────────────────────────
    const results = [];
    let seq = 0;
    const check = async (n, name, run, id, expect) => {
      step(`${n}/7 ${name}`);
      const err = await run();
      if (err) log(`  page reported: ${err}`);
      // A FRESH reader per check — the tag is a monotonic sequence, never the
      // operation number, so two branches of the same operation cannot reuse a
      // directory and quietly read a log that is no longer empty.
      const items = independentRead(rdBin, tmp, coord, `${++seq}-${RUN}`, readerKey);
      const it = items.get(id);
      const problems = [];
      if (!it) problems.push(`item ${id} absent from the independent projection`);
      else
        for (const [field, want] of Object.entries(expect)) {
          const got = Array.isArray(it[field]) ? [...it[field]].sort().join(",") : it[field];
          const wantStr = Array.isArray(want) ? [...want].sort().join(",") : want;
          if (got !== wantStr) problems.push(`${field} = ${JSON.stringify(got)}, want ${JSON.stringify(wantStr)}`);
        }
      if (problems.length > 0) {
        failures++;
        log(`  FAIL ${problems.join("; ")}`);
      } else {
        log(`  OK — independent rd projects ${id} exactly as intended`);
      }
      results.push({ n, name, id, ok: problems.length === 0, problems });
    };

    await check(
      1,
      "status move (drag a card between columns)",
      async () => {
        await dragCardToColumn(cdp, ids.move, "moving");
        return settle(cdp);
      },
      ids.move,
      { status: "active" },
    );

    await check(
      2,
      "claim",
      async () => {
        await selectCard(cdp, ids.claim);
        await clickIn(cdp, ".act-claim");
        return settle(cdp);
      },
      ids.claim,
      { status: "active", by: owner },
    );

    await check(
      3,
      "close",
      async () => {
        await selectCard(cdp, ids.close);
        await clickIn(cdp, ".act-close");
        return settle(cdp);
      },
      ids.close,
      { status: "done" },
    );

    await check(
      4,
      "retitle",
      async () => {
        await selectCard(cdp, ids.title);
        await cdp.evaluate(`
          const input = document.querySelector(".act-title-input");
          input.value = "retitled in a real browser";
          document.querySelector(".act-title-save").click();
          return true;
        `);
        return settle(cdp);
      },
      ids.title,
      { title: "retitled in a real browser" },
    );

    await check(
      5,
      "re-prioritise",
      async () => {
        await selectCard(cdp, ids.prio);
        await cdp.evaluate(`
          const sel = document.querySelector(".act-priority");
          sel.value = "p0";
          sel.dispatchEvent(new Event("change", { bubbles: true }));
          return true;
        `);
        return settle(cdp);
      },
      ids.prio,
      { priority: "p0" },
    );

    await check(
      6,
      "label add",
      async () => {
        await selectCard(cdp, ids.label);
        await cdp.evaluate(`
          const input = document.querySelector(".act-label-input");
          input.value = ${JSON.stringify(SHARED_LABEL)};
          document.querySelector(".act-label-add").click();
          return true;
        `);
        return settle(cdp);
      },
      ids.label,
      { labels: [SHARED_LABEL] },
    );

    await check(
      7,
      "gate approve",
      async () => {
        await selectCard(cdp, ids.gateOk);
        await clickIn(cdp, ".gate-approve");
        return settle(cdp);
      },
      ids.gateOk,
      { status: "active", gate: undefined },
    );

    step("bonus: gate reject keeps the gate open (same operation, other branch)");
    await check(
      7,
      "gate reject",
      async () => {
        await selectCard(cdp, ids.gateNo);
        await clickIn(cdp, ".gate-deny");
        return settle(cdp);
      },
      ids.gateNo,
      { status: "waiting", gate: "design" },
    );

    // ── ready-191: THE CONFIDENTIAL CLAUSE, asserted ON THE WIRE ─────────────
    //
    // Everything above is asserted on the independent rd's PROJECTION. A
    // projection that renders the right title proves the reader works and says
    // nothing about what crossed the socket. ready-191's done condition is a
    // claim about the EVENT: "produces a card an independent rd decrypts to
    // exactly the intended state, with no plaintext title/context/label ever on
    // the wire — asserted by inspecting the published event". So this block
    // reads the raw JSONL the reader's own `rd sync` pulled off the relay and
    // asserts against the bytes.
    if (CONFIDENTIAL) {
      step("ready-191: the browser's card ON THE WIRE — sealed, and its label token is RD's");
      const tag = `wire-${RUN}`;
      const items = independentRead(rdBin, tmp, coord, tag, readerKey);
      const problems = [];

      // The RETITLED card: the browser authored this exact string, so if it
      // appears anywhere in the serialized event the seal did not happen.
      const NEW_TITLE = "retitled in a real browser";
      const titleCards = readerEvents(tmp, tag, ids.title, 30302);
      const browserCard = titleCards.find((e) => e.pubkey === owner && tagValues(e, "enc").includes("1"));
      if (titleCards.length === 0) problems.push(`no kind:30302 card for ${ids.title} in the relay-sourced log`);
      else if (!browserCard) problems.push(`the card for ${ids.title} carries no ["enc","1"] marker — it was NOT sealed`);
      else {
        const wire = JSON.stringify(browserCard);
        if (wire.includes(NEW_TITLE)) problems.push(`PLAINTEXT ON THE WIRE: the browser's title appears in the event`);
        if (tagValues(browserCard, "title").length > 0) problems.push("the card carries a CLEAR title tag");
        if (tagValues(browserCard, "waiting_on").length > 0) problems.push("the card carries a CLEAR waiting_on tag");
        if (tagValues(browserCard, "cek_epoch").length !== 1) problems.push("the card carries no cek_epoch marker");
        // The clear ROUTING tags must survive — the envelope seals free text
        // only, and a card that sealed its routing would be unfilterable.
        if (!tagValues(browserCard, "a").includes(coord)) problems.push("the card lost its clear board coordinate");
        // …and the independent rd, holding a granted key, decrypts it to exactly
        // the string the browser authored.
        const projected = items.get(ids.title);
        if (projected?.title !== NEW_TITLE) {
          problems.push(`independent rd decrypted the title to ${JSON.stringify(projected?.title)}, want ${JSON.stringify(NEW_TITLE)}`);
        }
        log(`  browser card event id ${browserCard.id}`);
        log(`  its tags: ${JSON.stringify(browserCard.tags)}`);
        log(`  independent rd decrypts it to: title=${JSON.stringify(projected?.title)} status=${projected?.status}`);
      }

      // THE LABEL TOKEN, cross-implementation and live. rd attached SHARED_LABEL
      // to ids.rdlabel; the browser attached the same string to ids.label. On a
      // confidential board both `l` tags are HMAC-SHA256(board LTK, label), so
      // they must be the SAME string — that equality is the entire reason the
      // token exists (a relay's #l filter matches exact bytes).
      const rdCard = readerEvents(tmp, tag, ids.rdlabel, 30302).at(-1);
      const browserLabelCard = readerEvents(tmp, tag, ids.label, 30302).at(-1);
      const rdTokens = rdCard ? tagValues(rdCard, "l") : [];
      const browserTokens = browserLabelCard ? tagValues(browserLabelCard, "l") : [];
      if (rdTokens.length !== 1) problems.push(`rd's own card carries ${rdTokens.length} l tags, want exactly 1`);
      else if (rdTokens[0] === SHARED_LABEL) problems.push("rd emitted the label in the CLEAR on a confidential board");
      else if (browserTokens.length !== 1) problems.push(`the browser's card carries ${browserTokens.length} l tags, want exactly 1`);
      else if (browserTokens[0] === SHARED_LABEL) problems.push("the browser emitted the label in the CLEAR");
      else if (browserTokens[0] !== rdTokens[0]) {
        problems.push(`LABEL TOKENS DIVERGE: browser ${browserTokens[0]} vs rd ${rdTokens[0]} for the same label`);
      } else {
        log(`  label token agreement: rd and the browser both emitted ["l","${rdTokens[0]}"] for ${JSON.stringify(SHARED_LABEL)}`);
      }

      // ANTI-TAUTOLOGY: a reader with NO grant is fail-closed on the browser's
      // card specifically — not merely on rd's seeds.
      const blind = independentRead(rdBin, tmp, coord, `wireblind-${RUN}`);
      const blindTitle = blind.get(ids.title);
      if (!blindTitle) problems.push("the keyless reader lost the browser-written item entirely");
      else if (blindTitle.title === NEW_TITLE) problems.push("a reader with NO grant read the browser's title in the clear");
      else log(`  keyless reader sees: title=${JSON.stringify(blindTitle.title)} (fail-closed)`);

      const ok = problems.length === 0;
      if (!ok) {
        failures++;
        log(`  FAIL ${problems.join("; ")}`);
      } else {
        log("  PASS — sealed on the wire, decrypted by an independent rd, label token identical to rd's");
      }
      results.push({ n: "C", name: "confidential: sealed on the wire, decrypted independently", id: ids.title, ok, problems });
    }

    // ── done condition 5: two rapid writes to the same item do not collide ──
    step("done condition 5: two RAPID writes to the same item, later one wins");
    {
      await selectCard(cdp, ids.rapid);
      // Fired back to back with no wait between them: both land inside the same
      // wall-clock second, which is exactly the collision §17.2's monotonic bump
      // exists to prevent. Claim then close, because both emit an append-only
      // status event — so the relay retains BOTH and the stamps are observable
      // (a card is replaceable; the relay keeps only its newest).
      await clickIn(cdp, ".act-claim");
      await cdp.evaluate(`
        const btn = document.querySelector(".act-close");
        if (!btn) throw new Error("no close button");
        btn.click();
        return true;
      `);
      await settle(cdp, 20000);
      const tag = `rapid-${RUN}`;
      const items = independentRead(rdBin, tmp, coord, tag, readerKey);
      const it = items.get(ids.rapid);
      const stamps = readerStatusStamps(tmp, tag, ids.rapid);
      const monotonic = stamps.every((s, i) => i === 0 || s > stamps[i - 1]);
      const ok = it?.status === "done" && monotonic && stamps.length >= 3;
      if (!ok) failures++;
      log(
        `  ${ok ? "PASS" : "FAIL"}  status=${it?.status} (want done — the LATER write wins), status-event ` +
          `created_at chain retained by the relay = [${stamps.join(", ")}] strictly increasing=${monotonic}`,
      );
      results.push({
        n: 5,
        name: "two rapid writes — later wins, created_at strictly increasing",
        id: ids.rapid,
        ok,
        problems: [],
      });
    }

    // ── done condition 4 (failure path) + 6, against the REAL relay ─────────
    step("done condition 4: a relay that REJECTS puts the card back and says why");
    {
      // A key that the BOARD grants but the RELAY does not admit. This is the
      // real production write-policy: wss://relay.3dl.network enforces a tenant
      // allowlist, so this key passes the board's client-side authorization and
      // is then refused on the wire — the only honest way to exercise the
      // rejection path without a fake relay.
      const grantedButNotAdmitted = await mintKey(vite);
      rd(rdBin, projectDir, writerHome, ["grant", grantedButNotAdmitted.pubkey]);
      rd(rdBin, projectDir, writerHome, ["relay", "flush"]);

      const before = independentRead(rdBin, tmp, coord, `reject-before-${RUN}`, readerKey).get(ids.reject);
      const page = await openBoard(cdp, origin, coord, esbuild, grantedButNotAdmitted.secret);
      log(`  signed in as a granted-but-not-admitted key (${page.cards} cards)`);
      await dragCardToColumn(cdp, ids.reject, "moving");
      const message = await settle(cdp, 25000);
      const reverted = await cdp.evaluate(`
        const card = [...document.querySelectorAll(".card")].find((c) =>
          c.querySelector(".card-id")?.textContent?.trim() === ${JSON.stringify(ids.reject)});
        return card?.closest(".column")?.dataset.column ?? "(gone)";
      `);
      const after = independentRead(rdBin, tmp, coord, `reject-after-${RUN}`, readerKey).get(ids.reject);
      const saysWhy = /restricted|not admitted|reject/i.test(message);
      const ok = saysWhy && after?.status === before?.status;
      if (!ok) failures++;
      log(`  relay said: ${message.trim() || "(nothing)"}`);
      log(
        `  ${ok ? "PASS" : "FAIL"}  card is back in column "${reverted}"; independent rd still projects ` +
          `status=${after?.status} (was ${before?.status})`,
      );
      results.push({
        n: 4,
        name: "relay rejection reverts the card and states the relay's reason",
        id: ids.reject,
        ok,
        problems: [],
      });
    }

    step("done condition 6: no grant is refused CLIENT-SIDE and, separately, by the relay");
    {
      const noGrant = await mintKey(vite);
      await openBoard(cdp, origin, coord, esbuild, noGrant.secret, { expectCards: false });
      // The page must say it is read-only, and must offer no write affordance.
      const stated = await cdp.evaluate(`
        const card = document.querySelector(".card");
        if (card) card.click();
        await new Promise((r) => setTimeout(r, 300));
        const note = document.querySelector(".read-only-note")?.textContent ?? "";
        const actions = document.querySelectorAll(".act-claim, .act-title-save, .act-priority").length;
        return JSON.stringify({ note, actions });
      `);
      const { note, actions } = JSON.parse(stated);
      // WHICH REFUSAL, AND WHY THE MODE CHANGES IT (ready-191). whyReadOnly()
      // tests CONFIDENTIALITY first, then signer, then grant level. On a public
      // board the first branch is inapplicable and the reader is told about the
      // missing grant. On a CONFIDENTIAL board this key holds no CEK either, so
      // the honest first answer is the seal — "no read key reached this session,
      // so the browser cannot seal what it writes". Accepting the grant sentence
      // there would let a page that had silently stopped sealing pass this check.
      const wantRefusal = CONFIDENTIAL ? /cannot seal|seals its free text/i : /no write grant/i;
      const clientRefused = wantRefusal.test(note) && actions === 0;

      // …and SEPARATELY, the relay refuses the same key's event on the wire.
      const relayRefusal = await directRelayPublish(vite, noGrant.secret, coord);
      const relayRefused = relayRefusal.accepted === false;
      const ok = clientRefused && relayRefused;
      if (!ok) failures++;
      log(`  client said: ${note.trim() || "(nothing)"} — write affordances rendered: ${actions}`);
      log(`  relay said:  accepted=${relayRefusal.accepted} ${relayRefusal.message}`);
      log(`  ${ok ? "PASS" : "FAIL"}  both refusals present (neither alone is sufficient)`);
      results.push({ n: 6, name: "ungranted key refused client-side AND by the relay", id: "-", ok, problems: [] });
    }

    step("SUMMARY");
    for (const r of results) log(`  ${r.ok ? "PASS" : "FAIL"}  ${r.n}. ${r.name}  ${r.problems.join("; ")}`);
    log(`\nboard: ${coord}  (${CONFIDENTIAL ? "CONFIDENTIAL — plain `rd init`" : "PUBLIC — `rd init --public`"})`);
    log(`relay: ${RELAY}`);
    log(`${results.filter((r) => r.ok).length}/${results.length} operations converged`);
  } finally {
    for (const c of cleanup.reverse()) {
      try {
        c();
      } catch {
        /* best effort */
      }
    }
    if (KEEP) log(`\nkept: ${tmp}`);
    else rmSync(tmp, { recursive: true, force: true });
  }

  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error(`\nFAILED: ${err.stack ?? err.message}`);
  process.exit(1);
});
