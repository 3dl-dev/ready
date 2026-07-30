#!/usr/bin/env node
// live-cache.mjs — ready-fe4's GROUND-SOURCE PROOF, and the honest one.
//
// THE MISTAKE THIS FILE EXISTS TO NOT REPEAT. The first round of ready-fe4
// reported "73.1s -> 2.0s first paint" as the cache's number. It is not. Both
// of those runs opened a browser with an EMPTY cache: 73.1s was the serial,
// verify-everything-twice load before the change and 2.0s was the concurrent,
// paint-as-you-fold load after it. That is a real win and it belongs to the
// FETCH/FOLD work in the same commit — it says nothing whatsoever about
// localStorage. The number that measures a CACHE is COLD (a browser that has
// never opened this board) against WARM (a browser that has), on the SAME
// build, the SAME relay and the SAME minute. That is what this script measures,
// and it prints both even when the answer is unflattering.
//
// WHAT IT PROVES, in two parts:
//
//  PART A — COLD vs WARM, on the operator's real portfolio, read-only.
//    One browser, one profile, two visits. Time-to-first-ITEM is stamped INSIDE
//    the page by a MutationObserver, so it is the instant a card entered the
//    DOM and not the instant a 1s poller from Node noticed. Every inbound relay
//    frame is stamped too, by a WebSocket subclass installed before any page
//    script runs, which is what makes ready-fe4 condition 1's actual claim —
//    "paints real items BEFORE any relay round-trip completes" — checkable
//    rather than plausible.
//
//  PART B — ready-fe4 condition 4, BY THE METHOD THE CONDITION NAMES: a stale
//    cache never wins over a newer event, verified by mutating an item with the
//    rd CLI and watching an OPEN page converge with NO reload. Two mutations,
//    covering the two ways a cached card can be wrong:
//      B-i   the CLI writes while the browser is CLOSED. The next visit paints
//            the OLD title from localStorage (asserted at the first-paint
//            instant, which is the anti-tautology: a cache that painted nothing
//            could not lose to anything), and that SAME document — proven by a
//            sentinel a reload would erase — converges to the new title.
//      B-ii  the CLI writes while the browser is OPEN. The live subscription
//            (ready-4359) carries it to the same document, sentinel intact.
//    Part B writes ONLY to a throwaway board it provisions for the run.
//
//  WHAT IS REAL: the browser (headless Chromium over CDP running the shipped
//  `vite build` bundle over HTTP), the relay (wss://relay.3dl.network), the CLI
//  (the real `rd` built from this tree), the storage (the browser's own
//  localStorage — nothing is stubbed).
//
//  NOT REAL: the NIP-07 extension. Per ready-bff's ruling window.nostr is a
//  genuine secp256k1 + NIP-44 signer INJECTED over CDP holding this machine's
//  own rd key. That exercises the read path and the grant-unwrap contract
//  completely; it does NOT exercise the extension handshake (ready-35a's
//  one-run human gate). The secret is never printed and never written to disk
//  by this script.
//
// Usage:
//   node scripts/live-cache.mjs [--relay wss://…] [--keep] [--only a|b]
//
// DEPENDENCIES: node, go, npx vite (devDependency), esbuild (vite's own).

import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";

const BOARD_DIR = path.resolve(import.meta.dirname, "..");
const REPO_ROOT = path.resolve(BOARD_DIR, "../..");
const CHROME =
  process.env.CHROME_PATH ?? path.join(os.homedir(), ".cache/ms-playwright/chromium-1228/chrome-linux64/chrome");

const argv = process.argv.slice(2);
const KEEP = argv.includes("--keep");
const RELAY = argv.includes("--relay") ? argv[argv.indexOf("--relay") + 1] : "wss://relay.3dl.network";
const ONLY = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const RUN = Date.now().toString(36);
const BOARD_D = `fe4${RUN}`;
/** How long a change made elsewhere may take to reach the open page. */
const CONVERGE_DEADLINE_MS = 60000;

const log = (...a) => console.log(...a);
const step = (s) => console.log(`\n── ${s}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const results = [];
function record(name, ok, detail = "") {
  results.push({ name, ok, detail });
  log(`  ${ok ? "PASS" : "FAIL"}  ${name}${detail ? ` — ${detail}` : ""}`);
}

// ── tiny CDP client (no puppeteer: one socket, ids in, results out) ──────────

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
  /**
   * EVERY CALL HAS A DEADLINE, which the other live-* harnesses do not do and
   * paid for here. A headless Chromium loading the whole 40-board portfolio
   * from a warm cache is the biggest thing this repo asks a browser to do, and
   * on a loaded machine it can be OOM-killed mid-run. With an open-ended
   * promise that is indistinguishable from a slow relay: the harness sat on a
   * dead browser for ten minutes and reported nothing. A rejected call turns
   * that into a loud failure at the point it happened.
   */
  send(method, params = {}, timeoutMs = 120000) {
    const id = this.next++;
    const frame = { id, method, params };
    if (this.sessionId) frame.sessionId = this.sessionId;
    this.ws.send(JSON.stringify(frame));
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`CDP ${method} did not answer within ${timeoutMs}ms — is the browser still alive?`));
      }, timeoutMs);
      this.pending.set(id, {
        resolve: (v) => {
          clearTimeout(t);
          resolve(v);
        },
        reject: (e) => {
          clearTimeout(t);
          reject(e);
        },
      });
    });
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
// handshake and falsely reports a relay timeout (proven in M0).
async function waitFor(cdp, expr, what, timeoutMs, pollMs = 250) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await cdp.evaluate(`return !!(${expr});`)) return;
    if (Date.now() > deadline) {
      const dump = await cdp.evaluate("return document.body.innerText.slice(0, 900);");
      throw new Error(`timed out waiting for ${what}\n--- page text ---\n${dump}`);
    }
    await sleep(pollMs);
  }
}

// ── the in-page instrument ──────────────────────────────────────────────────
//
// INSTALLED BEFORE ANY PAGE SCRIPT, on every document. Two things, both of
// which have to be page-side to mean anything:
//
//  1 THE MARKS. A Node-side poller can only report "≤1s after the fact", which
//    is useless when the quantity being measured is a warm paint of a few tens
//    of milliseconds. A MutationObserver stamps performance.now() on the exact
//    mutation that put the first card in the DOM, and captures WHAT was on
//    screen at that instant — the card ids and titles, the board nodes' states,
//    and how many relay frames had arrived. That last capture is what makes
//    "painted from cache" a measurement instead of an inference.
//
//  2 THE WEBSOCKET RECORDER. Condition 1 says the warm paint lands "before any
//    relay round-trip completes". A round-trip completes when the first frame
//    comes BACK, so the recorder subclasses WebSocket and stamps the first
//    inbound message. Sockets opened before the paint are fine and expected;
//    an ANSWER before the paint would mean the paint was not the cache's.
const INSTRUMENT_JS = `
(() => {
  const marks = {
    login: null, tree: null, firstCard: null, settled: null,
    firstCardCount: 0, firstCards: [], firstCardNodes: [], firstCardWsMsgs: 0, firstCardWsFirstAt: null,
    cacheKeysAtLogin: [], cacheBytesAtLogin: 0,
    ws: { created: [], firstMsgAt: null, msgs: 0 },
  };
  window.__rdmarks = marks;
  const NativeWS = window.WebSocket;
  class RecordingWS extends NativeWS {
    constructor(...args) {
      super(...args);
      marks.ws.created.push({ url: String(args[0]), at: performance.now() });
      this.addEventListener("message", () => {
        marks.ws.msgs++;
        if (marks.ws.firstMsgAt === null) marks.ws.firstMsgAt = performance.now();
      });
    }
  }
  window.WebSocket = RecordingWS;

  const cards = () => [...document.querySelectorAll(".card")].map((c) => ({
    id: c.querySelector(".card-id")?.textContent?.trim() ?? "",
    title: c.querySelector(".card-title")?.textContent?.trim() ?? "",
  }));
  const nodes = () => [...document.querySelectorAll(".node[data-board-coord]")].map((n) => ({
    coord: n.getAttribute("data-board-coord"),
    state: n.getAttribute("data-board-state"),
    count: Number(n.querySelector(".ct")?.textContent ?? "NaN"),
  }));

  const check = () => {
    const now = performance.now();
    const nodeEls = document.querySelectorAll(".node[data-board-coord]");
    if (marks.tree === null && nodeEls.length > 0) marks.tree = now;
    if (marks.firstCard === null && document.querySelector(".card")) {
      marks.firstCard = now;
      marks.firstCards = cards();
      marks.firstCardCount = marks.firstCards.length;
      marks.firstCardNodes = nodes();
      marks.firstCardWsMsgs = marks.ws.msgs;
      marks.firstCardWsFirstAt = marks.ws.firstMsgAt;
    }
    if (
      marks.settled === null &&
      nodeEls.length > 0 &&
      document.querySelectorAll('.node[data-board-coord][data-board-state="stale"]').length === 0
    ) {
      marks.settled = now;
    }
  };
  new MutationObserver(check).observe(document, { subtree: true, childList: true, attributes: true });
  check();
})();
`;

/** The injected signer: a REAL secp256k1 signer and a REAL NIP-44 v2 unwrap,
 * built out of this app's own modules so a key used here and a key used by the
 * page cannot disagree. Identical in shape to live-portfolio.mjs's. */
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

// ── rd helpers ──────────────────────────────────────────────────────────────

function rd(bin, cwd, home, args) {
  return execFileSync(bin, args, {
    cwd,
    env: { ...process.env, RD_HOME: home, RD_NOSTR_RELAY_URL: RELAY },
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: 512 * 1024 * 1024,
  });
}

/** rdEcho runs an rd command and PRINTS it verbatim: the mutation this proof
 * turns on is evidence, so it appears as a transcript and not as a claim. */
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

function rdHome() {
  if (process.env.RD_HOME) return process.env.RD_HOME;
  const xdg = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(xdg, "rd");
}

// ── one visit ───────────────────────────────────────────────────────────────

/**
 * visit performs ONE full page load and returns what the page recorded about
 * it. A CROSS-DOCUMENT navigation via about:blank, deliberately: the app strips
 * its own fragment with replaceState on load, so navigating straight from one
 * fragment url to the same one can race the stripped url.
 *
 * The login click and the sentinel are set in ONE evaluate so that nothing —
 * not even a Node round-trip — can happen between "this session asked for the
 * board" and "this document is being watched".
 */
async function visit(cdp, url, sentinel) {
  await cdp.send("Page.navigate", { url: "about:blank" });
  await sleep(300);
  await cdp.send("Page.navigate", { url });
  await waitFor(cdp, `document.querySelector("button")`, "the login page", 60000, 100);
  await cdp.evaluate(`
    const btn = [...document.querySelectorAll("button")].find((b) => /extension/i.test(b.textContent ?? ""));
    if (!btn) throw new Error("no NIP-07 login button");
    if (btn.disabled) throw new Error("the page did not see the injected window.nostr");
    const m = window.__rdmarks;
    m.cacheKeysAtLogin = Object.keys(localStorage).filter((k) => k.startsWith("rd.board.cache"));
    m.cacheBytesAtLogin = m.cacheKeysAtLogin.reduce((n, k) => n + (localStorage.getItem(k) ?? "").length, 0);
    m.login = performance.now();
    window.__cacheSentinel = ${JSON.stringify(sentinel)};
    btn.click();
    return true;
  `);
  return cdp;
}

/** marksOf reads the in-page instrument, with every instant re-based on the
 * login click — the moment the reader asked for the board. */
async function marksOf(cdp) {
  const m = await cdp.evaluate("return JSON.stringify(window.__rdmarks);").then(JSON.parse);
  const rel = (t) => (t === null || m.login === null ? null : Math.round(t - m.login));
  return {
    raw: m,
    treeMs: rel(m.tree),
    firstCardMs: rel(m.firstCard),
    settledMs: rel(m.settled),
    firstRelayFrameMs: rel(m.ws.firstMsgAt),
    relayFrameBeforeFirstCardMs: rel(m.firstCardWsFirstAt),
    firstCardCount: m.firstCardCount,
    firstCardWsMsgs: m.firstCardWsMsgs,
    totalWsMsgs: m.ws.msgs,
    cacheKeys: m.cacheKeysAtLogin,
    cacheBytes: m.cacheBytesAtLogin,
    firstCards: m.firstCards,
    firstCardNodes: m.firstCardNodes,
    sockets: m.ws.created.length,
  };
}

const fmt = (ms) => (ms === null ? "     —" : `${(ms / 1000).toFixed(2)}s`.padStart(6));

// ── the run ─────────────────────────────────────────────────────────────────

async function main() {
  if (!existsSync(CHROME)) throw new Error(`no Chromium at ${CHROME} (set CHROME_PATH)`);
  const esbuild = (await import("esbuild")).default ?? (await import("esbuild"));
  const idPath = path.join(rdHome(), "nostr-identity.json");
  const identity = JSON.parse(readFileSync(idPath, "utf8"));
  if (!identity.secret_hex) throw new Error(`${idPath}: no secret_hex`);

  const tmp = mkdtempSync(path.join(os.tmpdir(), "rd-fe4-cache-"));
  const cleanup = [];

  try {
    log(`relay:    ${RELAY}`);
    log(`viewer:   ${identity.pubkey_hex}`);
    log(`scratch:  ${tmp}`);

    step("build rd from this tree");
    const rdBin = path.join(tmp, "rd");
    execFileSync("go", ["build", "-o", rdBin, "./cmd/rd"], { cwd: REPO_ROOT, stdio: "inherit" });

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
      const type = file.endsWith(".js")
        ? "text/javascript"
        : file.endsWith(".css")
          ? "text/css"
          : file.endsWith(".json")
            ? "application/json"
            : "text/html";
      res.writeHead(200, { "content-type": type }).end(readFileSync(file));
    });
    await new Promise((r) => server.listen(0, "127.0.0.1", r));
    cleanup.push(() => server.close());
    const origin = `http://127.0.0.1:${server.address().port}`;

    step("launch Chromium (ONE profile — the cold visit's localStorage is the warm visit's cache)");
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
    const cdp = await CDP.connect(wsUrl);
    const { targetId } = await cdp.send("Target.createTarget", { url: "about:blank" });
    const { sessionId } = await cdp.send("Target.attachToTarget", { targetId, flatten: true });
    cdp.sessionId = sessionId;
    await cdp.send("Page.enable");
    await cdp.send("Runtime.enable");
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", { source: INSTRUMENT_JS });
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
      source: await injectedSignerJs(esbuild, identity.secret_hex),
    });

    // ── PART A ───────────────────────────────────────────────────────────────
    let cold;
    let warm;
    if (ONLY !== "b") {
      step("PART A — COLD vs WARM on the real portfolio (read-only; no fragment, the deployed path)");
      const url = `${origin}/board/`;

      log("\n  COLD: a browser that has never opened this workspace");
      await visit(cdp, url, `cold-${RUN}`);
      await waitFor(cdp, `document.querySelector(".card")`, "the first item card (cold)", 1800000, 100);
      await waitFor(
        cdp,
        `document.querySelectorAll('.node[data-board-coord]').length > 0 &&
         document.querySelectorAll('.node[data-board-coord][data-board-state="stale"]').length === 0`,
        "every board to finish loading (cold)",
        1800000,
        500,
      );
      // The cache is written by view.save() on the same tick the load settles;
      // one more beat so the write has certainly landed before the tab moves on.
      await sleep(1500);
      cold = await marksOf(cdp);
      const coldTotals = await cdp.evaluate(`
        return JSON.stringify({
          cards: document.querySelectorAll(".card").length,
          boards: document.querySelectorAll(".node[data-board-coord]").length,
          cacheBytes: Object.keys(localStorage).filter((k) => k.startsWith("rd.board.cache"))
            .reduce((n, k) => n + (localStorage.getItem(k) ?? "").length, 0),
        });
      `).then(JSON.parse);
      log(`  cold: tree ${fmt(cold.treeMs)}  first item ${fmt(cold.firstCardMs)}  settled ${fmt(cold.settledMs)}`);
      log(`        ${coldTotals.boards} boards, ${coldTotals.cards} cards on screen, cache now ${(coldTotals.cacheBytes / 1024).toFixed(0)} KiB`);
      record(
        "the COLD visit really was cold — no cache entry existed when it painted",
        cold.cacheKeys.length === 0,
        `cache keys at login: ${JSON.stringify(cold.cacheKeys)}`,
      );

      log("\n  WARM: the same profile, the same url, a second visit");
      await visit(cdp, url, `warm-${RUN}`);
      await waitFor(cdp, `document.querySelector(".card")`, "the first item card (warm)", 300000, 50);
      const warmEarly = await marksOf(cdp);
      await waitFor(
        cdp,
        `document.querySelectorAll('.node[data-board-coord]').length > 0 &&
         document.querySelectorAll('.node[data-board-coord][data-board-state="stale"]').length === 0`,
        "every board to finish loading (warm)",
        1800000,
        500,
      );
      warm = await marksOf(cdp);
      const warmTotals = await cdp.evaluate(`
        return JSON.stringify({
          cards: document.querySelectorAll(".card").length,
          boards: document.querySelectorAll(".node[data-board-coord]").length,
        });
      `).then(JSON.parse);
      log(`  warm: tree ${fmt(warm.treeMs)}  first item ${fmt(warm.firstCardMs)}  settled ${fmt(warm.settledMs)}`);
      log(`        ${warmTotals.boards} boards, ${warmTotals.cards} cards on screen at settle`);
      log(`        cache read at login: ${warm.cacheKeys.length} key(s), ${(warm.cacheBytes / 1024).toFixed(0)} KiB`);
      log(`        at the first card: ${warmEarly.firstCardCount} cards, ${warmEarly.firstCardWsMsgs} relay frames received, ${warmEarly.sockets} socket(s) opened`);
      log(`        boards at the first card: ${JSON.stringify(warmEarly.firstCardNodes.slice(0, 4))}…`);

      record(
        "the WARM visit painted real items from cache",
        warm.cacheKeys.length > 0 && warm.firstCardCount > 0,
        `${warm.firstCardCount} cards at the first paint, from ${(warm.cacheBytes / 1024).toFixed(0)} KiB of localStorage`,
      );
      record(
        "the warm paint landed BEFORE any relay round-trip completed (condition 1)",
        warm.firstCardCount > 0 && warm.raw.firstCardWsMsgs === 0,
        `${warm.raw.firstCardWsMsgs} relay frames had arrived when the first card appeared; first frame at ${fmt(warm.firstRelayFrameMs)}`,
      );
      record(
        "every board painted from cache says so on its node ('from cache')",
        warm.firstCardNodes.length > 0 && warm.firstCardNodes.every((n) => n.state === "stale"),
        `states at the first paint: ${JSON.stringify([...new Set(warm.firstCardNodes.map((n) => n.state))])}`,
      );
      record(
        "warm time-to-first-item is faster than cold",
        cold.firstCardMs !== null && warm.firstCardMs !== null && warm.firstCardMs < cold.firstCardMs,
        `cold ${fmt(cold.firstCardMs)} vs warm ${fmt(warm.firstCardMs)}`,
      );
    }

    // ── PART B ───────────────────────────────────────────────────────────────
    if (ONLY !== "a") {
      step("PART B — condition 4, by the method the condition names: the rd CLI mutates, the OPEN page converges");

      log("\n  provision a throwaway PUBLIC board (nothing here touches a real project board)");
      const writerHome = path.join(tmp, "writer-home");
      mkdirSync(writerHome, { recursive: true });
      writeFileSync(path.join(writerHome, "nostr-identity.json"), JSON.stringify(identity), { mode: 0o600 });
      const projectDir = path.join(tmp, BOARD_D);
      mkdirSync(projectDir, { recursive: true });
      const initOut = JSON.parse(
        rd(rdBin, projectDir, writerHome, [
          "init",
          "--public",
          "--no-commit-binding",
          "--relay",
          RELAY,
          "--name",
          BOARD_D,
          "--json",
        ]),
      );
      const coord = initOut.board;
      log(`  board ${coord}`);

      const ids = { closed: `${BOARD_D}-closed`, open: `${BOARD_D}-open` };
      const SEED_CLOSED = "seeded before the browser ever cached this board";
      const SEED_OPEN = "seeded, and renamed later while the page is open";
      rd(rdBin, projectDir, writerHome, ["create", SEED_CLOSED, "--id", ids.closed, "--type", "task", "--priority", "p2"]);
      rd(rdBin, projectDir, writerHome, ["create", SEED_OPEN, "--id", ids.open, "--type", "task", "--priority", "p2"]);
      rd(rdBin, projectDir, writerHome, ["relay", "flush"]);

      const boardUrl = `${origin}/board/#board=${encodeURIComponent(coord)}&relays=${encodeURIComponent(RELAY)}`;

      log("\n  visit 1 (cold): the board loads and writes its cache");
      await visit(cdp, boardUrl, `b-cold-${RUN}`);
      await waitFor(cdp, `document.querySelector(".card")`, "the seeded cards", 180000, 100);
      await waitFor(
        cdp,
        `document.querySelectorAll('.node[data-board-coord][data-board-state="stale"]').length === 0`,
        "the board to settle",
        180000,
        250,
      );
      await sleep(1500);
      const bCold = await marksOf(cdp);
      const cachedBytes = await cdp.evaluate(`
        return Object.keys(localStorage).filter((k) => k.startsWith("rd.board.cache"))
          .reduce((n, k) => n + (localStorage.getItem(k) ?? "").length, 0);
      `);
      log(`  cold: first item ${fmt(bCold.firstCardMs)}, settled ${fmt(bCold.settledMs)}, cache now ${cachedBytes} bytes`);

      log("\n  the browser LEAVES the board — the cache is all that is left of it");
      await cdp.send("Page.navigate", { url: "about:blank" });
      await sleep(500);

      // ── B-i: the CLI writes while the browser is closed ────────────────────
      const CLI_CLOSED_TITLE = "renamed by the rd CLI while the browser was CLOSED";
      rdEcho(rdBin, projectDir, writerHome, ["update", ids.closed, "--title", CLI_CLOSED_TITLE], "the CLI writes");
      rdEcho(rdBin, projectDir, writerHome, ["relay", "flush"], "nothing should be queued — the CLI publishes directly");

      log("\n  visit 2 (warm, and now provably STALE): the cache holds a title the relay has superseded");
      const sentinel = `fe4-${RUN}-${Math.random().toString(36).slice(2)}`;
      await visit(cdp, boardUrl, sentinel);
      await waitFor(cdp, `document.querySelector(".card")`, "the warm paint", 120000, 50);
      const bWarm = await marksOf(cdp);
      const paintedTitle = bWarm.firstCards.find((c) => c.id === ids.closed)?.title ?? "(absent)";
      log(`  first paint at ${fmt(bWarm.firstCardMs)} showed ${JSON.stringify(paintedTitle)}`);
      log(`  ${bWarm.firstCardWsMsgs} relay frames had arrived at that instant`);

      // THE ANTI-TAUTOLOGY. "the page shows the new title" proves nothing about
      // a cache unless the page FIRST showed the old one, from the cache, in
      // this same document.
      record(
        "the warm paint showed the STALE cached title, before the relay answered",
        paintedTitle === SEED_CLOSED && bWarm.firstCardWsMsgs === 0,
        `painted ${JSON.stringify(paintedTitle)} with ${bWarm.firstCardWsMsgs} relay frames in`,
      );

      const converged = await waitForTitle(cdp, ids.closed, CLI_CLOSED_TITLE, CONVERGE_DEADLINE_MS);
      record(
        "the stale cached card LOST to the newer event, with no reload (condition 4)",
        converged.ok,
        converged.ok
          ? `converged ${converged.ms}ms after the warm paint`
          : `still showing ${JSON.stringify(converged.title)} after ${CONVERGE_DEADLINE_MS}ms`,
      );
      const s1 = await cdp.evaluate("return window.__cacheSentinel ?? null;");
      record(
        "…and it was the SAME document — the page was never reloaded",
        s1 === sentinel,
        s1 === sentinel ? "sentinel intact" : `sentinel is ${JSON.stringify(s1)} — the document was replaced`,
      );

      // ── B-ii: the CLI writes while the browser is OPEN ─────────────────────
      log("\n  and now the same thing with the page ALREADY on screen (the live subscription)");
      const CLI_OPEN_TITLE = "renamed by the rd CLI while the browser was OPEN";
      const beforeOpen = await titleOf(cdp, ids.open);
      record(
        "before the CLI ran, the open board showed the seeded title",
        beforeOpen === SEED_OPEN,
        `showing ${JSON.stringify(beforeOpen)}`,
      );
      rdEcho(rdBin, projectDir, writerHome, ["update", ids.open, "--title", CLI_OPEN_TITLE], "the CLI writes");
      rdEcho(rdBin, projectDir, writerHome, ["relay", "flush"], "nothing should be queued");
      const convergedOpen = await waitForTitle(cdp, ids.open, CLI_OPEN_TITLE, CONVERGE_DEADLINE_MS);
      record(
        "the CLI's change reached the OPEN page with no reload",
        convergedOpen.ok,
        convergedOpen.ok ? `${convergedOpen.ms}ms after the CLI's write returned` : `not seen within ${CONVERGE_DEADLINE_MS}ms`,
      );
      const s2 = await cdp.evaluate("return window.__cacheSentinel ?? null;");
      record(
        "…same document again — still no reload anywhere in part B",
        s2 === sentinel,
        s2 === sentinel ? "sentinel intact" : `sentinel is ${JSON.stringify(s2)} — the document was replaced`,
      );
      log(`\n  board used: ${coord}`);
    }

    step("SUMMARY");
    if (cold && warm) {
      log("  the operator's real portfolio, one build, one relay, one minute apart:");
      log("                          cold     warm");
      log(`    time to first item   ${fmt(cold.firstCardMs)}   ${fmt(warm.firstCardMs)}`);
      log(`    time to board tree   ${fmt(cold.treeMs)}   ${fmt(warm.treeMs)}`);
      log(`    time to settled      ${fmt(cold.settledMs)}   ${fmt(warm.settledMs)}`);
      log(`    relay frames before the first item:  cold ${cold.firstCardWsMsgs}, warm ${warm.firstCardWsMsgs}`);
      // CONDITION 2'S KNOWN DEVIATION, MADE VISIBLE RATHER THAN DESCRIBED. The
      // warm visit refetches every board IN FULL — it does not scope the
      // initial fetch by the cached high-water mark, on purpose and for the
      // reason written on boardcache.ts's CachedBoard.high. So these two totals
      // are expected to be the SAME, and printing them is how a later reader
      // finds out if that ever silently changed.
      log(`    relay frames over the whole visit:   cold ${cold.totalWsMsgs}, warm ${warm.totalWsMsgs}`);
    }
    for (const r of results) log(`  ${r.ok ? "PASS" : "FAIL"}  ${r.name}${r.detail ? ` — ${r.detail}` : ""}`);
    const bad = results.filter((r) => !r.ok).length;
    log(`\n${results.length - bad}/${results.length} assertions held`);
    process.exitCode = bad === 0 ? 0 : 1;
  } finally {
    for (const fn of cleanup.reverse()) {
      try {
        fn();
      } catch {
        /* best effort */
      }
    }
    if (KEEP) log(`\nkept: ${tmp}`);
    else rmSync(tmp, { recursive: true, force: true });
  }
}

/** titleOf is what the LIVE DOM says this item's title is, right now. */
async function titleOf(cdp, id) {
  return cdp.evaluate(`
    const card = [...document.querySelectorAll(".card")].find(
      (c) => c.querySelector(".card-id")?.textContent?.trim() === ${JSON.stringify(id)});
    return card?.querySelector(".card-title")?.textContent?.trim() ?? null;
  `);
}

/** waitForTitle polls the OPEN document — no navigation, no reload — until the
 * card carries the expected title. */
async function waitForTitle(cdp, id, expected, timeoutMs) {
  const start = Date.now();
  for (;;) {
    const title = await titleOf(cdp, id);
    if (title === expected) return { ok: true, ms: Date.now() - start, title };
    if (Date.now() - start > timeoutMs) return { ok: false, ms: Date.now() - start, title };
    await sleep(400);
  }
}

main().catch((err) => {
  console.error(`\nFAILED: ${err.stack ?? err.message}`);
  process.exit(1);
});
