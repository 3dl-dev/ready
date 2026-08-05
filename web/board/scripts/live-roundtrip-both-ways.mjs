#!/usr/bin/env node
// live-roundtrip-both-ways.mjs — ready-4359's END-TO-END PROOF, BOTH DIRECTIONS.
//
// One continuous scripted run, no manual step anywhere in it:
//
//   1  a REAL browser loads a board that has items, signed in through window.nostr
//   2  it MOVES an item between columns, EDITS a title, and APPROVES a gate
//   3  an INDEPENDENT rd — clean RD_HOME, empty log, trust set = the board owner —
//      reads all THREE back off the relay, with the right actor and reason
//   4  the REVERSE DIRECTION: the rd CLI changes the board and the STILL-OPEN
//      browser shows it, through its live subscription, with NO reload
//   5  and then a browser write on top of the CLI's change converges: an
//      independent rd sees BOTH, so the page was not writing a stale snapshot
//      back over what it was displaying
//
// WHY A SECOND SCRIPT NEXT TO live-write-roundtrip.mjs. That one is ready-b2b's
// proof of the seven WRITE operations, one independent read per operation, and
// ready-fd2 is extending it concurrently. This one asks a different question —
// three changes in ONE session read back by ONE reader, and then the direction
// nothing had ever exercised — and it owns the reverse/subscription path so the
// two do not fight over the same file. The forward half here is deliberately
// smaller than that script's: it is the done condition's three changes, not a
// second copy of the seven-operation matrix.
//
// WHAT IS REAL, AND WHAT IS NOT — stated plainly, because the value of this
// script is that its claim is exact.
//
//  REAL: the browser (headless Chromium over CDP, running the shipped `vite
//  build` bundle served over HTTP), the signatures (BIP-340 secp256k1, the same
//  module the page bundles, validated against the official vectors in
//  src/lib/schnorrsign.test.ts), the relay (wss://relay.3dl.network, which
//  verifies every signature and enforces a tenant write allowlist), the CLI (the
//  real `rd` binary built from this tree), and the reader (that same binary in a
//  CLEAN RD_HOME whose nostr-log.jsonl does not exist until `rd sync` fills it
//  from the relay).
//
//  NOT REAL: the NIP-07 EXTENSION. Per ready-bff's ruling (2026-07-30, option b)
//  window.nostr is a genuine secp256k1 signer INJECTED over CDP rather than one
//  supplied by nos2x/Alby. Every event is really signed, really published and
//  really read back; what that does NOT prove is the extension handshake, which
//  ready-35a carries as a one-run human release gate. Do not report this script
//  as proof of extension integration.
//
// WHY A FRESH READER, AND WHY IT MATTERS MORE THAN IT SOUNDS. The local log is
// append-only and retains superseded events forever, so reading back through the
// SAME log proves nothing about convergence — an entire confidential suite once
// stayed green while a rotation was deleting keys from the relay, because every
// test read the log instead of what the relay retains. Every read below runs in
// a brand-new RD_HOME + project directory, with `trusted_pubkeys` naming the
// board owner and nothing else, so the RELAY is the only possible source of
// every event it projects.
//
// WHY THE OWNER'S KEY ON A THROWAWAY BOARD. wss://relay.3dl.network enforces a
// tenant write allowlist: an arbitrary fresh key is refused ("restricted: pubkey
// is not admitted"), so the signer must be a key that already holds write
// access. This script reads the LOCAL machine's own rd signing key (as
// live-parity.mjs and live-write-roundtrip.mjs do, for the same reason) and
// confines every event it writes to a FRESH, per-run board — never a real
// project board. The secret is held in memory, injected into the page's signer,
// and is never logged, printed or written anywhere by this script.
//
// THIS IS A MANUAL PROOF, NOT A CI JOB: it needs the live relay, a Chromium on
// disk and the local machine's allowlisted rd key, none of which CI has. The
// deterministic half of the same guarantee DOES run in CI —
// src/lib/relaylive.test.ts (the subscription's wire behaviour),
// src/main.live.test.ts (the composition: a pushed event re-folds the mounted
// board with no remount), src/board/nostrwriter.absorb.test.ts (the writer does
// not republish a stale snapshot over a change it is displaying) and
// src/board/render.liveupdate.test.ts (a live re-render does not eat what the
// human is typing).
//
// Usage:
//   node scripts/live-roundtrip-both-ways.mjs [--keep] [--relay wss://…] [--confidential]
// Exits non-zero on the FIRST assertion that does not hold.
//
// DEPENDENCIES: node, go, npx vite (devDependency) and esbuild — vite's own hard
// dependency, always present in node_modules beside it, imported here rather
// than declared a second time at a version that could drift from vite's pin.

import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { createServer } from "vite";
import { writeReceipt } from "./receipt.mjs";

const BOARD_DIR = path.resolve(import.meta.dirname, "..");
const REPO_ROOT = path.resolve(BOARD_DIR, "../..");
const CHROME =
  process.env.CHROME_PATH ?? path.join(os.homedir(), ".cache/ms-playwright/chromium-1228/chrome-linux64/chrome");

const argv = process.argv.slice(2);
const KEEP = argv.includes("--keep");
const RELAY = argv.includes("--relay") ? argv[argv.indexOf("--relay") + 1] : "wss://relay.3dl.network";
const CONFIDENTIAL = argv.includes("--confidential");

const RUN = Date.now().toString(36);
const BOARD_D = `${CONFIDENTIAL ? "c4359" : "b4359"}${RUN}`;

/** How long the reverse direction may take before it is called a failure. The
 * relay pushes within milliseconds; this is a generous bound on a scale-to-zero
 * relay, and the OBSERVED time is printed so a regression that merely got slow
 * is visible rather than absorbed. */
const LIVE_DEADLINE_MS = 45000;

const log = (...a) => console.log(...a);
const step = (s) => console.log(`\n── ${s}`);

let failures = 0;
const results = [];
function record(name, ok, detail = "") {
  if (!ok) failures++;
  results.push({ name, ok, detail });
  log(`  ${ok ? "PASS" : "FAIL"}  ${name}${detail ? ` — ${detail}` : ""}`);
}

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
// handshake and falsely reports a relay timeout (proven in M0); never used here.
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

/** rdEcho runs an rd command and PRINTS the command and its output verbatim.
 * Done condition 5 asks for the actual transcript, not a description of it, so
 * every command whose output is evidence goes through here. */
function rdEcho(bin, cwd, home, args, label = "") {
  const out = rd(bin, cwd, home, args);
  log(`\n$ rd ${args.join(" ")}${label ? `   # ${label}` : ""}`);
  log(
    out
      .split("\n")
      .map((l) => (l === "" ? "" : `  ${l}`))
      .join("\n")
      .replace(/\n+$/, ""),
  );
  return out;
}

/**
 * newReader builds a reader that has NEVER seen the browser's events: a
 * brand-new RD_HOME whose rd.json trusts the BOARD OWNER and nobody else, and a
 * brand-new project directory whose nostr-log.jsonl does not exist until `rd
 * sync` pulls it from the relay. Returns { dir, home } for further commands.
 */
function newReader(bin, tmp, coord, owner, tag, readerSecret) {
  const home = path.join(tmp, `reader-${tag}-home`);
  const dir = path.join(tmp, `reader-${tag}`);
  mkdirSync(home, { recursive: true });
  // TRUST SET = OWNER ONLY (done condition 3). Self is always trusted and this
  // key writes nothing, so the owner is the only author whose events can enter
  // the projection.
  writeFileSync(path.join(home, "rd.json"), JSON.stringify({ trusted_pubkeys: [owner] }, null, 2), { mode: 0o600 });
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
  return { dir, home };
}

function readerItems(bin, reader) {
  const parsed = JSON.parse(rd(bin, reader.dir, reader.home, ["list", "--json", "--all"]));
  const items = Array.isArray(parsed) ? parsed : (parsed.items ?? []);
  return new Map(items.map((i) => [i.id, i]));
}

/** mintKey draws a fresh secp256k1 key through the SAME signer module the page
 * uses, so a key minted here and a key used there cannot disagree. */
async function mintKey(vite) {
  const mod = await vite.ssrLoadModule("/src/lib/schnorrsign.ts");
  const secret = mod.randomSecretHex();
  return { secret, pubkey: mod.xOnlyPubkey(secret) };
}

/** injectedSignerJs bundles the REAL secp256k1 signer as an IIFE that installs
 * window.nostr before any page script runs. See this file's header for what it
 * does and does not prove. The nip44 namespace is what lets a CONFIDENTIAL board
 * open at all: the page cannot do ECDH, so the only way a CEK reaches it is by
 * asking the signer to open the owner-signed grant. */
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

async function openBoard(cdp, origin, coord, esbuild, secretHex) {
  const { identifier } = await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
    source: await injectedSignerJs(esbuild, secretHex),
  });
  openBoard.scriptId = identifier;
  // A CROSS-DOCUMENT navigation, deliberately: the app strips its own fragment
  // with replaceState on load, so a fragment-only navigation followed by a
  // reload can race and reload the STRIPPED url.
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
  await waitFor(cdp, `document.querySelectorAll(".card").length > 0`, "the board's cards", 90000);
  // ready-c7b: ".card" alone is not proof this board's own load is real —
  // ready-fe4 can paint it from a cache before this call's own relay
  // round-trip lands. The left-tree node's `data-board-state` attribute flips
  // off "stale" the instant this board's REAL fold (and its writer) arrives;
  // see the matching comment in live-write-roundtrip.mjs's openBoard.
  await waitFor(
    cdp,
    `document.querySelector('[data-board-coord=${JSON.stringify(coord)}]')?.dataset.boardState !== "stale"`,
    `${coord}'s own (non-cached) load`,
    90000,
  );
  return { cards: await cdp.evaluate("return document.querySelectorAll('.card').length;") };
}

// ── page actions ────────────────────────────────────────────────────────────

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

async function resolveGateInRail(cdp, id, approve, reason) {
  return cdp.evaluate(`
    const li = document.querySelector('.gate-item[data-id=${JSON.stringify(id)}]');
    if (!li) throw new Error("no gate rail entry for ${id}");
    const input = li.querySelector(".gate-reason-input");
    if (!input) throw new Error("the rail offers no reason field for ${id} (read-only?)");
    input.value = ${JSON.stringify(reason)};
    const btn = li.querySelector(${JSON.stringify(approve ? ".gate-approve" : ".gate-deny")});
    if (!btn) throw new Error("no ${approve ? "Approve" : "Reject"} button in the rail for ${id}");
    btn.click();
    return true;
  `);
}

/** boardState is what the LIVE DOM shows right now: every card id, and the title
 * and column of each. The only witness for "the board reflects it". */
async function boardState(cdp) {
  return JSON.parse(
    await cdp.evaluate(`
      const cards = [...document.querySelectorAll(".card")].map((c) => ({
        id: c.querySelector(".card-id")?.textContent?.trim() ?? "",
        title: c.querySelector(".card-title")?.textContent?.trim() ?? "",
        column: c.closest(".column")?.dataset.column ?? "(none)",
      }));
      return JSON.stringify(cards);
    `),
  );
}

async function clickIn(cdp, selector) {
  return cdp.evaluate(`
    const n = document.querySelector(${JSON.stringify(selector)});
    if (!n) throw new Error("no element " + ${JSON.stringify(selector)});
    n.click();
    return true;
  `);
}

/** settle waits until the page has finished the write it just started: either a
 * transient error appeared (a refusal) or one cycle passed with none. */
async function settle(cdp, ms = 9000) {
  await sleep(ms);
  return cdp.evaluate(`return document.querySelector(".transient-error")?.textContent ?? "";`);
}

async function main() {
  if (!existsSync(CHROME)) throw new Error(`no Chromium at ${CHROME} (set CHROME_PATH)`);

  const tmp = mkdtempSync(path.join(os.tmpdir(), "rd-4359-"));
  const cleanup = [];

  try {
    step("build rd from this tree");
    const rdBin = path.join(tmp, "rd");
    execFileSync("go", ["build", "-o", rdBin, "./cmd/rd"], { cwd: REPO_ROOT, stdio: "inherit" });

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

    // On a confidential board a reader's IDENTITY cannot be fresh — a CEK is
    // NIP-44-wrapped TO a pubkey — so the confidential runs pin ONE granted key
    // and give every reader that identity. The LOG is still empty every time,
    // which is the independence that matters: the relay is the only source.
    const readerKey = CONFIDENTIAL ? await mintKey(vite) : undefined;

    step("seed the items the three changes act on (rd's OWN writer, published to the relay)");
    const ids = {
      move: `${BOARD_D}-move`,
      title: `${BOARD_D}-title`,
      gate: `${BOARD_D}-gate`,
      // The item the rd CLI renames while the browser is watching (step 4), and
      // that the browser then writes to (step 5).
      rev: `${BOARD_D}-rev`,
    };
    for (const [k, id] of Object.entries(ids)) {
      rd(rdBin, projectDir, writerHome, [
        "create",
        `both-ways proof: ${k}`,
        "--id",
        id,
        "--type",
        "task",
        "--priority",
        "p2",
      ]);
    }
    rd(rdBin, projectDir, writerHome, ["claim", ids.gate, "--reason", "for the gate"]);
    rd(rdBin, projectDir, writerHome, [
      "gate",
      ids.gate,
      "--gate-type",
      "design",
      "--description",
      "needs a browser ruling",
    ]);
    if (readerKey) rd(rdBin, projectDir, writerHome, ["grant", readerKey.pubkey]);
    rd(rdBin, projectDir, writerHome, ["relay", "flush"]);

    step("BASELINE: an independent reader sees the seeds through the relay only");
    const baseReader = newReader(rdBin, tmp, coord, owner, `baseline-${RUN}`, readerKey);
    const baseline = readerItems(rdBin, baseReader);
    for (const id of Object.values(ids)) {
      if (!baseline.has(id)) throw new Error(`seed ${id} did not reach the relay — the proof cannot start`);
    }
    log(`  ${baseline.size} items read back independently`);

    step("build the shipped bundle and serve it");
    execFileSync("npx", ["vite", "build", "--outDir", path.join(tmp, "dist"), "--emptyOutDir"], {
      cwd: BOARD_DIR,
      stdio: "inherit",
    });
    const dist = path.join(tmp, "dist");
    // The production bundle is built with base "/board/", so the local server
    // mounts dist at that same path — serving it at "/" would 404 every asset.
    const server = http.createServer((req, res) => {
      let rel = decodeURIComponent((req.url ?? "/").split("?")[0].split("#")[0]);
      if (rel.startsWith("/board")) rel = rel.slice("/board".length) || "/";
      const file = path.join(dist, rel === "/" ? "index.html" : rel);
      if (!file.startsWith(dist) || !existsSync(file)) {
        res.writeHead(404).end("not found");
        return;
      }
      const type = file.endsWith(".js") ? "text/javascript" : file.endsWith(".css") ? "text/css" : "text/html";
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

    // THE NO-RELOAD SENTINEL. Set on the page object AFTER load; a reload, a
    // navigation or a remount of the document clears it. Everything the reverse
    // direction claims rests on this token still being there at the end.
    const SENTINEL = `ready4359-${RUN}-${Math.random().toString(36).slice(2)}`;
    await cdp.evaluate(`window.__ready4359 = ${JSON.stringify(SENTINEL)}; return true;`);

    // ── FORWARD: three changes, one browser session ─────────────────────────
    const NEW_TITLE = "edited in a real browser";
    const GATE_REASON = "approved from the browser gate rail";

    step("done condition 2: MOVE a card between columns");
    await dragCardToColumn(cdp, ids.move, "moving");
    let err = await settle(cdp);
    if (err) log(`  page reported: ${err}`);

    step("done condition 2: EDIT a title");
    await selectCard(cdp, ids.title);
    await cdp.evaluate(`
      const input = document.querySelector(".act-title-input");
      input.value = ${JSON.stringify(NEW_TITLE)};
      document.querySelector(".act-title-save").click();
      return true;
    `);
    err = await settle(cdp);
    if (err) log(`  page reported: ${err}`);

    step("done condition 2: APPROVE a gate, from the rail, with a reason");
    await resolveGateInRail(cdp, ids.gate, true, GATE_REASON);
    err = await settle(cdp);
    if (err) log(`  page reported: ${err}`);

    // ── done condition 3: ONE independent reader sees ALL THREE ─────────────
    step("done condition 3: an INDEPENDENT rd (clean RD_HOME, empty log, trust = owner) reads all three back");
    const reader = newReader(rdBin, tmp, coord, owner, `three-${RUN}`, readerKey);
    log(`  RD_HOME=${reader.home}  (rd.json trusted_pubkeys=["${owner}"])`);
    log(`  project=${reader.dir}  (nostr-log.jsonl created by 'rd sync' from ${RELAY} and nothing else)`);
    // `--for ""` because the reader is a DIFFERENT identity from the board
    // owner: the default ready view is "mine", and everything on this board is
    // the owner's, so the unfiltered view is the one that shows the work.
    rdEcho(rdBin, reader.dir, reader.home, ["ready", "--for", ""], "what the independent reader considers workable");
    for (const id of [ids.move, ids.title, ids.gate]) {
      rdEcho(rdBin, reader.dir, reader.home, ["show", id]);
    }
    const seenByRd = readerItems(rdBin, reader);

    const moved = seenByRd.get(ids.move);
    record(
      "1/3 the browser's column move is in the independent projection",
      moved?.status === "active",
      `status=${JSON.stringify(moved?.status)} want "active"`,
    );

    const retitled = seenByRd.get(ids.title);
    record(
      "2/3 the browser's title edit is in the independent projection",
      retitled?.title === NEW_TITLE,
      `title=${JSON.stringify(retitled?.title)}`,
    );

    const ruled = seenByRd.get(ids.gate);
    const ruling = (ruled?.history ?? []).at(-1);
    const gateProblems = [];
    if (ruled?.status !== "active") gateProblems.push(`status=${JSON.stringify(ruled?.status)} want "active"`);
    if ((ruled?.gate ?? "") !== "") gateProblems.push(`gate=${JSON.stringify(ruled?.gate)} want cleared`);
    if ((ruled?.gate_msg_id ?? "") !== "") gateProblems.push(`gate_msg_id still set`);
    if (ruling?.note !== GATE_REASON) gateProblems.push(`history note=${JSON.stringify(ruling?.note)}`);
    if (ruling?.changed_by !== owner) gateProblems.push(`actor=${JSON.stringify(ruling?.changed_by)} want ${owner}`);
    record("3/3 the browser's gate approval is in the independent projection, with actor and reason", gateProblems.length === 0, gateProblems.join("; "));
    log(`  history entry: ${JSON.stringify(ruling)}`);

    const gatesOut = rdEcho(rdBin, reader.dir, reader.home, ["gates", "--json"], "the gate must be GONE from rd's own view");
    const gatesLeft = JSON.parse(gatesOut);
    const gateIds = new Set((Array.isArray(gatesLeft) ? gatesLeft : (gatesLeft.items ?? [])).map((i) => i.id));
    record("the approved gate left 'rd gates' on the independent reader", !gateIds.has(ids.gate), `rd gates: ${JSON.stringify([...gateIds])}`);

    // ── done condition 4: THE REVERSE DIRECTION ────────────────────────────
    step("done condition 4: the rd CLI changes the board and the OPEN browser reflects it with NO reload");
    const CLI_TITLE = "renamed by the rd CLI while the browser was open";
    const CLI_NEW_ITEM = `${BOARD_D}-cli`;
    const before = await boardState(cdp);
    log(`  board BEFORE (live DOM): ${JSON.stringify(before)}`);
    // ANTI-TAUTOLOGY: "the board shows X" proves nothing unless the board did
    // NOT show X a moment earlier, on the same page instance.
    record(
      "before the CLI ran, the open board showed neither change",
      before.find((c) => c.id === ids.rev)?.title !== CLI_TITLE && !before.some((c) => c.id === CLI_NEW_ITEM),
      `rev title=${JSON.stringify(before.find((c) => c.id === ids.rev)?.title)}, ${CLI_NEW_ITEM} present=${before.some((c) => c.id === CLI_NEW_ITEM)}`,
    );

    rdEcho(rdBin, projectDir, writerHome, ["update", ids.rev, "--title", CLI_TITLE], "the CLI writes");
    rdEcho(rdBin, projectDir, writerHome, [
      "create",
      "created by the rd CLI while the browser was open",
      "--id",
      CLI_NEW_ITEM,
      "--type",
      "task",
      "--priority",
      "p1",
    ]);
    // The clock starts HERE — the instant the CLI's own writes returned. On the
    // nostr-native path each command publishes to the relay itself, so the
    // `relay flush` below is a no-op (it reports total=0) and is run only to
    // prove there was nothing left queued locally. Starting the clock after it
    // would credit the subscription with time the flush spent.
    const publishedAt = Date.now();
    rdEcho(rdBin, projectDir, writerHome, ["relay", "flush"], "nothing should be queued — the CLI publishes directly");

    let live = before;
    let liveMs = -1;
    for (;;) {
      live = await boardState(cdp);
      const renamed = live.find((c) => c.id === ids.rev)?.title === CLI_TITLE;
      const appeared = live.some((c) => c.id === CLI_NEW_ITEM);
      if (renamed && appeared) {
        liveMs = Date.now() - publishedAt;
        break;
      }
      if (Date.now() - publishedAt > LIVE_DEADLINE_MS) break;
      await sleep(500);
    }
    log(`  board AFTER  (live DOM): ${JSON.stringify(live)}`);
    record(
      "the CLI's rename reached the open board with no reload",
      live.find((c) => c.id === ids.rev)?.title === CLI_TITLE,
      liveMs >= 0 ? `${liveMs}ms after the CLI's write returned` : `not seen within ${LIVE_DEADLINE_MS}ms`,
    );
    record(
      "an item CREATED by the CLI appeared on the open board with no reload",
      live.some((c) => c.id === CLI_NEW_ITEM),
      liveMs >= 0 ? `${liveMs}ms after the CLI's write returned` : `not seen within ${LIVE_DEADLINE_MS}ms`,
    );

    // THE ANTI-TAUTOLOGY FOR THE WHOLE REVERSE DIRECTION. Everything above is
    // explained equally well by "the page reloaded itself" — which would be a
    // manual refresh by another name, and is exactly what this item forbids
    // substituting. The sentinel was set on the page object after load and does
    // not survive a navigation.
    const sentinel = await cdp.evaluate("return window.__ready4359 ?? null;");
    record(
      "the page was NEVER reloaded — same document, same JS context",
      sentinel === SENTINEL,
      sentinel === SENTINEL ? "sentinel intact" : `sentinel is ${JSON.stringify(sentinel)} — the document was replaced`,
    );

    // ── done condition 4 (the other half): the page does not write a stale
    //    snapshot back over what it is showing ────────────────────────────────
    step("convergence: a BROWSER write on top of the CLI's change keeps both");
    await selectCard(cdp, ids.rev);
    await clickIn(cdp, ".act-claim");
    err = await settle(cdp);
    if (err) log(`  page reported: ${err}`);

    const afterReader = newReader(rdBin, tmp, coord, owner, `converge-${RUN}`, readerKey);
    rdEcho(rdBin, afterReader.dir, afterReader.home, ["show", ids.rev], "both changes, read independently");
    const converged = readerItems(rdBin, afterReader).get(ids.rev);
    record(
      "an independent rd sees the CLI's title AND the browser's claim on the same item",
      converged?.title === CLI_TITLE && converged?.status === "active",
      `title=${JSON.stringify(converged?.title)} status=${JSON.stringify(converged?.status)}`,
    );

    step("SUMMARY");
    for (const r of results) log(`  ${r.ok ? "PASS" : "FAIL"}  ${r.name}${r.detail ? ` — ${r.detail}` : ""}`);
    log(`\nboard: ${coord}  (${CONFIDENTIAL ? "CONFIDENTIAL — plain `rd init`" : "PUBLIC — `rd init --public`"})`);
    log(`relay: ${RELAY}`);
    log(`${results.filter((r) => r.ok).length}/${results.length} assertions held`);
    // ready-cc2: leave a committed, checkable record that this run happened, so the
    // only evidence is not prose in a commit message. See scripts/receipt.mjs.
    const receiptPath = writeReceipt({
      script: "live-roundtrip-both-ways.mjs",
      repoRoot: REPO_ROOT,
      boardDir: BOARD_DIR,
      relay: RELAY,
      boardCoord: coord,
      mode: CONFIDENTIAL ? "confidential" : "public",
      checks: results.map((r) => ({ name: r.name, ok: r.ok })),
    });
    log(`receipt: ${receiptPath}`);
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
