#!/usr/bin/env node
// live-portfolio.mjs — ready-27b's GROUND-SOURCE PROOF.
//
// THE CLAIM IT SUPPORTS, exactly: open ONE page in a real browser against the
// live relay and see EVERY project's work — the whole portfolio, not one board
// — with each board's count equal to what `rd list --json` projects for that
// same board, and with every board this page could not show in full saying so
// BY NAME.
//
// WHAT IS REAL HERE, AND WHAT IS NOT.
//
//  REAL: the browser (headless Chromium over CDP, the shipped `vite build`
//  bundle served over HTTP, the same relays.json the deployed page loads), the
//  relay (wss://relay.3dl.network), the board discovery (the page's own
//  kind-only paged walk), the decryption (real NIP-44 v2 unwrap of real
//  owner-signed grants), and the reader (the real `rd` binary built from this
//  tree, one CLEAN RD_HOME + EMPTY log per board, so the relay is its only
//  possible source).
//
//  NOT REAL: the NIP-07 extension. Per ready-bff's ruling window.nostr is
//  INJECTED over CDP by a genuine secp256k1 + NIP-44 signer holding the local
//  machine's own rd key, rather than supplied by nos2x/Alby. That exercises the
//  page's read path and the grant-unwrap contract completely; it does NOT
//  exercise the extension handshake (ready-35a's one-run human gate).
//
// THE SECRET IS NEVER PRINTED, never written to disk by this script, and never
// leaves the page it is injected into. It is the same key `rd` itself signs
// with (~/.config/rd/nostr-identity.json), read for the same one-time,
// same-machine reason live-parity.mjs reads it.
//
// THREE INDEPENDENT SOURCES, WHICH IS THE WHOLE POINT:
//
//   1 THE ORACLE — this script's own paged kind-30301 walk over the relay,
//     written here and not shared with the app, so "the page found every board"
//     is not the page marking its own homework. NEVER an `authors` filter: that
//     filter under-returns on this relay (measured 42 of 56 boards for a paged
//     kind+authors query against 56 of 56 for a paged kind-only query, same
//     relay, same run — ready-5c5). The walk is kind-only, paged backwards with
//     `until` at limit 500, and ownership is applied CLIENT-SIDE.
//   2 THE PAGE — what a person actually sees: one tree node per board, each
//     carrying its coordinate, its load state and its count.
//   3 rd — `rd list --json --all --offline` in the operator's OWN project
//     directory for each board, which is the count the item's done condition
//     names ("the counts agree with `rd list --json` per project"). --offline so
//     the answer is the project's own log and this script mutates nothing.
//
// WHY NOT A FRESH-CLONE READER FOR SOURCE 3, which would be more independent:
// `rd sync` does not page. Measured on this relay 2026-07-30, a brand-new
// RD_HOME + empty log syncing the 3dl board reported `need=500 downloaded=481`
// and projected 201 items where the same board's own project directory holds
// 558 — the relay's per-REQ cap of 500, unpaged, on the GO side. It is the exact
// defect ready-5c5 fixed in web/board/src/lib/relay.ts and it is still present
// in the CLI's inbound path, so a fresh-clone reader is not a usable oracle for
// a count today. `--fresh-reader` runs it anyway, ADVISORY ONLY, because the
// truncation it exhibits is the evidence for that finding.
//
// WHAT MAKES THE RUN FAIL:
//   - a board the oracle found (unarchived) that the page does not show AT ALL
//     (the silent vanish this item exists to prevent);
//   - a board the page shows as "open" whose count differs from `rd list`'s in
//     that board's own project directory;
//   - a board the page could not show in full that it does not NAME in its
//     per-board status list (an unexplained gap is the same lie as a vanish).
//
// A board the page marks sealed / key-unreadable / did-not-load is REPORTED
// with its count delta and does not fail the run: the page is not claiming to
// show that board's work, it is claiming to say so, and that claim is checked.
//
//   node scripts/live-portfolio.mjs [--relay wss://…] [--keep] [--boards N]
//                                   [--projects ~/projects] [--fresh-reader]
//
// DEPENDENCIES: node, go, npx vite (devDependency), esbuild (vite's own).

import { execFileSync, spawn } from "node:child_process";
import {
  mkdtempSync,
  mkdirSync,
  writeFileSync,
  readFileSync,
  readdirSync,
  rmSync,
  existsSync,
  copyFileSync,
} from "node:fs";
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
/** Cap the number of boards re-read by rd, for a fast smoke run. The PAGE always
 * loads the whole portfolio; this only bounds step 3. */
const BOARD_CAP = argv.includes("--boards") ? Number(argv[argv.indexOf("--boards") + 1]) : Infinity;
/** Where the operator's project directories live — each one's
 * .ready/config.json pins the board coordinate it belongs to. */
const PROJECTS_DIR = argv.includes("--projects")
  ? path.resolve(argv[argv.indexOf("--projects") + 1])
  : path.join(os.homedir(), "projects");
/** Also run the fresh-clone reader. ADVISORY ONLY — see this file's header for
 * why its counts cannot currently be believed. */
const FRESH_READER = argv.includes("--fresh-reader");

const KIND_BOARD = 30301;
/** One REQ page. The relay caps a single REQ at 500 (measured), so asking for
 * more is harmless and asking for less would page more often. */
const PAGE_LIMIT = 500;

const log = (...a) => console.log(...a);
const step = (s) => console.log(`\n── ${s}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ── the local machine's rd identity ─────────────────────────────────────────

function rdHome() {
  if (process.env.RD_HOME) return process.env.RD_HOME;
  const xdg = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(xdg, "rd");
}

function loadLocalKey() {
  const keyPath = path.join(rdHome(), "nostr-identity.json");
  const raw = JSON.parse(readFileSync(keyPath, "utf8"));
  if (!raw.secret_hex || !raw.pubkey_hex) throw new Error(`${keyPath}: missing secret_hex/pubkey_hex`);
  return { secretHex: raw.secret_hex, pubkeyHex: raw.pubkey_hex, keyPath };
}

// ── SOURCE 1: the oracle — this script's own paged, kind-only board walk ─────

/**
 * discoverBoards walks the relay for kind-30301 board definitions and returns
 * the coordinates authored by `ownerPubkey`.
 *
 * NO `authors` FILTER — see this file's header. The REQ names only the kind and
 * pages backwards with `until`, exactly as lib/relay.ts does for the same reason;
 * ownership is applied here, after the fact, on the events that came back.
 *
 * Signatures are NOT verified here, deliberately. This is the ORACLE for "which
 * boards exist on the relay", and it is compared against a page that DOES verify
 * every one — so a forged 30301 can only ever make the oracle claim a board the
 * page correctly dropped, which surfaces as a loud mismatch to investigate
 * rather than as a silent agreement. The page's verification is pinned by its
 * own suite (boarddiscovery.test.ts).
 */
async function discoverBoards(relay, ownerPubkey) {
  const seen = new Map(); // id -> event
  let until;
  for (let page = 0; page < 40; page++) {
    const filter = { kinds: [KIND_BOARD], limit: PAGE_LIMIT };
    if (until !== undefined) filter.until = until;
    const got = await reqOnce(relay, filter);
    let added = 0;
    let oldest = until;
    for (const e of got) {
      if (!seen.has(e.id)) {
        seen.set(e.id, e);
        added++;
      }
      if (oldest === undefined || e.created_at < oldest) oldest = e.created_at;
    }
    log(`  page ${page + 1}: ${got.length} events, ${added} new, ${seen.size} total`);
    // A page that added nothing new is the end of the walk; so is a short page.
    if (added === 0 || got.length < PAGE_LIMIT) break;
    if (oldest === undefined) break;
    until = oldest - 1;
  }
  const mine = [];
  for (const e of seen.values()) {
    if (e.pubkey !== ownerPubkey) continue;
    const d = (e.tags ?? []).find((t) => t[0] === "d")?.[1];
    if (!d) continue;
    const title = (e.tags ?? []).find((t) => t[0] === "title")?.[1] ?? "";
    const archived = ((e.tags ?? []).find((t) => t[0] === "archived")?.[1] ?? "") !== "";
    mine.push({ coord: `${KIND_BOARD}:${e.pubkey}:${d}`, boardD: d, title, archived, createdAt: e.created_at });
  }
  // LATEST-WINS PER COORDINATE, then drop the archived ones — the same two rules
  // the page applies (boarddiscovery.ts's newerBoardEvent + isArchivedBoard) and
  // that `rd board archive` is the other half of. An oracle that skipped them
  // would report every deliberately-hidden board as a board the page LOST: the
  // first run of this script did exactly that and accused the page of dropping
  // 31 boards, all of which were archived test scratch (rdtest*, e2eproj) plus
  // four real ones the operator had archived on purpose.
  const byCoord = new Map();
  for (const b of mine) {
    const prev = byCoord.get(b.coord);
    if (!prev || b.createdAt > prev.createdAt) byCoord.set(b.coord, b);
  }
  const live = [...byCoord.values()].filter((b) => !b.archived);
  const archivedCount = byCoord.size - live.length;
  if (archivedCount > 0) log(`  ${archivedCount} archived board(s) dropped, as the page drops them`);
  return live.sort((a, b) => a.boardD.localeCompare(b.boardD));
}

/**
 * projectDirsByBoard maps board coordinate -> the operator's project directory
 * pinned to it. The pin is .ready/config.json's "board" (SyncConfig.Board), the
 * same field rd itself resolves the board from, with the committed
 * .ready/board.json binding as the fallback for a project that predates it.
 */
function projectDirsByBoard(root) {
  const out = new Map();
  let entries = [];
  try {
    entries = readdirSync(root, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const ent of entries) {
    if (!ent.isDirectory()) continue;
    const dir = path.join(root, ent.name);
    for (const file of ["config.json", "board.json"]) {
      const p = path.join(dir, ".ready", file);
      if (!existsSync(p)) continue;
      try {
        const coord = JSON.parse(readFileSync(p, "utf8")).board;
        if (typeof coord === "string" && coord !== "" && !out.has(coord)) out.set(coord, dir);
      } catch {
        /* not a readable config — treat as no pin */
      }
    }
  }
  return out;
}

/**
 * projectCount is `rd list --json --all --offline` in the project's OWN
 * directory: the count the item's done condition is written in.
 *
 * --offline reads that project's local log and nothing else, so this script
 * neither syncs nor writes anything in the operator's real directories. --all
 * because the page's per-board count is every item the fold returns, terminal
 * ones included (render.ts's countByProject), and `rd list` excludes terminal
 * items by default — comparing those two would be comparing different questions.
 */
function projectCount(rdBin, dir) {
  const out = execFileSync(rdBin, ["list", "--json", "--all", "--offline"], {
    cwd: dir,
    env: { ...process.env },
    encoding: "utf8",
    // A 1,000-item board's JSON is several megabytes — well past execFileSync's
    // 1 MiB default, which fails with a null stderr and so reads as "rd returned
    // nothing" rather than as the buffer overflow it is. The first run of this
    // script reported `rd null` for exactly the six biggest boards because of it.
    maxBuffer: 512 * 1024 * 1024,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const parsed = JSON.parse(out);
  const items = Array.isArray(parsed) ? parsed : (parsed.items ?? []);
  return items.length;
}

/** reqOnce opens one socket, runs one REQ, and resolves at EOSE. */
function reqOnce(relay, filter) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(relay);
    const out = [];
    const sub = `oracle${Math.random().toString(36).slice(2, 10)}`;
    const t = setTimeout(() => {
      try {
        ws.close();
      } catch {
        /* closed */
      }
      resolve(out);
    }, 45000);
    ws.onopen = () => ws.send(JSON.stringify(["REQ", sub, filter]));
    ws.onmessage = (m) => {
      const f = JSON.parse(m.data);
      if (f[0] === "EVENT" && f[1] === sub) out.push(f[2]);
      else if (f[0] === "EOSE" && f[1] === sub) {
        clearTimeout(t);
        try {
          ws.send(JSON.stringify(["CLOSE", sub]));
          ws.close();
        } catch {
          /* closed */
        }
        resolve(out);
      }
    };
    ws.onerror = () => {
      clearTimeout(t);
      reject(new Error(`relay ${relay}: connection failed`));
    };
  });
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
// handshake and falsely reports a relay timeout (proven in M0).
async function waitFor(cdp, expr, what, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await cdp.evaluate(`return !!(${expr});`)) return;
    if (Date.now() > deadline) {
      const dump = await cdp.evaluate("return document.body.innerText.slice(0, 900);");
      throw new Error(`timed out waiting for ${what}\n--- page text ---\n${dump}`);
    }
    await sleep(1000);
  }
}

/** The injected signer: a REAL secp256k1 signer and a REAL NIP-44 v2 unwrap,
 * built out of this app's own modules so a key used here and a key used by the
 * page cannot disagree. Identical in shape to live-write-roundtrip.mjs's. */
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

// ── SOURCE 3: one independent rd reader per board ────────────────────────────

/**
 * independentCount is `rd list --json --all` for ONE board, read by an rd that
 * has never seen anything except through the relay: a brand-new RD_HOME (given
 * the local signing identity, because a CEK is wrapped TO a pubkey and a fresh
 * key would decrypt nothing) and a brand-new project directory whose
 * nostr-log.jsonl does not exist until `rd sync` creates it.
 *
 * The identity is the same one the browser is running as, which is what makes
 * the comparison meaningful: two readers, same key, same relay, one folding in
 * Go and one in the browser.
 *
 * ADVISORY ONLY TODAY, and NOT a gate — see this file's header: `rd sync` does
 * not page the relay, so on any board holding more than the relay's per-REQ cap
 * this returns a truncated projection. That truncation is the reason the flag
 * exists; it is evidence for the finding, not a count to trust.
 */
function independentCount(rdBin, tmp, board, identityPath, tag) {
  const home = path.join(tmp, `reader-${tag}-home`);
  const dir = path.join(tmp, `reader-${tag}`);
  mkdirSync(home, { recursive: true });
  copyFileSync(identityPath, path.join(home, "nostr-identity.json"));
  mkdirSync(path.join(dir, ".ready"), { recursive: true });
  writeFileSync(
    path.join(dir, ".ready", "config.json"),
    JSON.stringify(
      {
        project_name: `reader-${tag}`,
        board: board.coord,
        relay_endpoints: [{ url: RELAY, read: true, write: false }],
      },
      null,
      2,
    ),
  );
  if (existsSync(path.join(dir, ".ready", "nostr-log.jsonl"))) {
    throw new Error("reader log is not empty — this reader would not be independent");
  }
  const run = (args) =>
    execFileSync(rdBin, args, {
      cwd: dir,
      env: { ...process.env, RD_HOME: home, RD_NOSTR_RELAY_URL: RELAY },
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
  run(["sync"]);
  const parsed = JSON.parse(run(["list", "--json", "--all"]));
  const items = Array.isArray(parsed) ? parsed : (parsed.items ?? []);
  return items.length;
}

// ── the run ─────────────────────────────────────────────────────────────────

async function main() {
  const esbuild = (await import("esbuild")).default ?? (await import("esbuild"));
  const key = loadLocalKey();
  const tmp = mkdtempSync(path.join(os.tmpdir(), "rd-live-portfolio-"));
  const cleanup = [];
  let failures = 0;

  try {
    log(`relay:    ${RELAY}`);
    log(`viewer:   ${key.pubkeyHex}`);
    log(`scratch:  ${tmp}`);

    step("SOURCE 1 — the oracle: this script's own kind-only paged board walk");
    const t0 = Date.now();
    const oracle = await discoverBoards(RELAY, key.pubkeyHex);
    log(`  ${oracle.length} boards authored by this key, in ${((Date.now() - t0) / 1000).toFixed(1)}s`);
    if (oracle.length === 0) throw new Error("the oracle found no boards — nothing below could prove anything");
    for (const b of oracle) log(`    ${b.boardD.padEnd(24)} ${b.title || "(untitled)"}`);

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

    step("SOURCE 2 — the page: log in and open the WHOLE portfolio");
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
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", { source: await injectedSignerJs(esbuild, key.secretHex) });

    // NO FRAGMENT AT ALL. This is the deployed page's own-boards discovery path:
    // relays come from the same-origin relays.json the bundle ships, boards come
    // from the page's kind-only paged walk, and keys come from the signer
    // unwrapping owner-signed grants. Nothing about the portfolio is supplied by
    // the URL, which is what makes "the page found every board" a statement
    // about the PAGE.
    const paintStart = Date.now();
    await cdp.send("Page.navigate", { url: `${origin}/board/` });
    await waitFor(cdp, `document.querySelector("button")`, "the login page", 60000);
    await cdp.evaluate(`
      const btn = [...document.querySelectorAll("button")].find((b) => /extension/i.test(b.textContent ?? ""));
      if (!btn) throw new Error("no NIP-07 login button");
      if (btn.disabled) throw new Error("the page did not see the injected window.nostr");
      btn.click();
      return true;
    `);
    await waitFor(cdp, `document.querySelector(".node[data-board-coord]")`, "the portfolio's board list", 600000);
    const firstPaint = Date.now() - paintStart;
    log(`  workspace painted in ${(firstPaint / 1000).toFixed(1)}s`);

    // ready-fe4 MADE THE PAINT AND THE LOAD TWO DIFFERENT INSTANTS, and this
    // wait is why every count below is still a count.
    //
    // Until that item, the board list appeared only when every board had
    // finished loading, so "a node exists" and "the load is done" were the same
    // event and the assertions could start the moment the first node appeared.
    // The page now mounts the tree as soon as DISCOVERY answers and fills each
    // board in as it lands — measured on this relay 2026-07-30, 2.0s to the tree
    // against 97.2s for the whole load — so sampling at the old signal reads
    // every board at count 0 with a state of "stale", and this script PASSED a
    // page that had loaded nothing at all. That is a vacuous run, and it is the
    // failure mode this file exists to prevent, so the settle signal is now
    // explicit: NO board node may still be marked "stale".
    await waitFor(
      cdp,
      `document.querySelectorAll('.node[data-board-coord][data-board-state="stale"]').length === 0`,
      "every board to finish loading (no node still marked stale)",
      1800000,
    );
    log(`  every board settled in ${((Date.now() - paintStart) / 1000).toFixed(1)}s`);

    const page = await cdp.evaluate(`
      return [...document.querySelectorAll(".node[data-board-coord]")].map((n) => ({
        coord: n.getAttribute("data-board-coord"),
        state: n.getAttribute("data-board-state"),
        name: n.querySelector(".nm")?.textContent ?? "",
        count: Number(n.querySelector(".ct")?.textContent ?? "NaN"),
      }));
    `);
    const statusRows = await cdp.evaluate(`
      return [...document.querySelectorAll(".board-status-row")].map((r) => ({
        coord: r.getAttribute("data-board-coord"),
        text: r.textContent ?? "",
      }));
    `);
    const tally = await cdp.evaluate(`return document.querySelector(".tally")?.textContent ?? "";`);
    log(`  header tally: ${tally.replace(/\\s+/g, " ").trim()}`);
    log(`  ${page.length} boards on the page, ${statusRows.length} of them carrying a status row`);

    step("EVERY BOARD THE ORACLE FOUND IS ON THE PAGE");
    const pageByCoord = new Map(page.map((b) => [b.coord, b]));
    const missing = oracle.filter((b) => !pageByCoord.has(b.coord));
    if (missing.length > 0) {
      failures++;
      log(`  FAIL ${missing.length} board(s) the relay served and the page does not show at all:`);
      for (const b of missing) log(`    ${b.boardD} (${b.coord})`);
    } else {
      log(`  OK — all ${oracle.length} boards have a node in the page's left tree`);
    }
    const extra = page.filter((b) => !oracle.some((o) => o.coord === b.coord));
    if (extra.length > 0) log(`  note: ${extra.length} board(s) on the page the oracle did not attribute to this key`);

    step("EVERY DEGRADED BOARD SAYS SO, BY NAME");
    const rowByCoord = new Map(statusRows.map((r) => [r.coord, r]));
    for (const b of page) {
      if (b.state === "open") {
        if (rowByCoord.has(b.coord)) {
          failures++;
          log(`  FAIL ${b.name}: shown as open AND carrying a status row — the page contradicts itself`);
        }
        continue;
      }
      const row = rowByCoord.get(b.coord);
      if (!row) {
        failures++;
        log(`  FAIL ${b.name}: state=${b.state} with NO status row — the reader is never told why`);
        continue;
      }
      log(`  ${b.name} [${b.state}] ${row.text.slice(0, 150)}…`);
    }

    step("SOURCE 3 — `rd list --json` per project, compared with the page's counts");
    const rdBin = path.join(tmp, "rd");
    execFileSync("go", ["build", "-o", rdBin, "./cmd/rd"], { cwd: REPO_ROOT, stdio: "inherit" });
    const dirs = projectDirsByBoard(PROJECTS_DIR);
    log(`  ${dirs.size} project directories under ${PROJECTS_DIR} pin a board coordinate`);
    const targets = oracle.filter((b) => pageByCoord.has(b.coord)).slice(0, BOARD_CAP);
    const rows = [];
    for (const [i, b] of targets.entries()) {
      const shown = pageByCoord.get(b.coord);
      const dir = dirs.get(b.coord);
      let rdCount = null;
      let err = "";
      if (dir) {
        try {
          rdCount = projectCount(rdBin, dir);
        } catch (e) {
          // stderr FIRST but never alone: a failure with an empty stderr (a
          // buffer overflow, a signal) must still report something.
          const parts = [e.stderr?.toString().trim(), e.message, String(e)].filter((s) => s);
          err = (parts[0] ?? "unknown failure").split("\n")[0].slice(0, 140);
        }
      }
      let fresh = null;
      if (FRESH_READER) {
        try {
          fresh = independentCount(rdBin, tmp, b, key.keyPath, `${i}-${Date.now().toString(36)}`);
        } catch {
          fresh = null;
        }
      }
      const agree = rdCount !== null && rdCount === shown.count;
      rows.push({ name: shown.name || b.boardD, state: shown.state, page: shown.count, rd: rdCount, agree, err, dir, fresh });
      const verdict =
        dir === undefined
          ? "no local project pins this board — nothing to compare against"
          : err !== ""
            ? `rd error: ${err}`
            : agree
              ? `agree (${rdCount})`
              : `DIFFER (page ${shown.count}, rd ${rdCount})`;
      log(
        `  ${String(i + 1).padStart(2)}/${targets.length} ${(shown.name || b.boardD).padEnd(24)} [${(shown.state ?? "?").padEnd(16)}] ${verdict}` +
          (fresh === null ? "" : `  [fresh-clone reader: ${fresh}]`),
      );
      if (dir !== undefined && !agree && shown.state === "open") {
        failures++;
        log(`     FAIL an OPEN board is the page CLAIMING to show that board's work`);
      }
    }

    step("SUMMARY");
    const comparable = rows.filter((r) => r.dir !== undefined && r.err === "");
    const open = comparable.filter((r) => r.state === "open");
    const degraded = rows.filter((r) => r.state !== "open");
    log(`  boards on the page:        ${page.length}`);
    log(`  unarchived boards on relay:${String(oracle.length).padStart(4)}`);
    log(`  compared against rd:       ${comparable.length}`);
    log(`  open, counts agree:        ${open.filter((r) => r.agree).length}/${open.length}`);
    log(`  degraded and NAMED:        ${degraded.length}`);
    for (const r of degraded) {
      log(`    ${r.name.padEnd(24)} [${r.state}] page ${r.page}, rd ${r.rd === null ? "n/a" : r.rd}`);
    }
    log(failures === 0 ? "\nPASS" : `\nFAIL — ${failures} problem(s)`);
    process.exitCode = failures === 0 ? 0 : 1;
  } finally {
    for (const fn of cleanup.reverse()) {
      try {
        fn();
      } catch {
        /* best effort */
      }
    }
    if (!KEEP) rmSync(tmp, { recursive: true, force: true });
    else log(`\nkept: ${tmp}`);
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
