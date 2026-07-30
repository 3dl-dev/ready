#!/usr/bin/env node
// live-stranger-walk.mjs — ready-48f's EIGHT-STEP STRANGER WALK, scripted end to
// end, with no manual step anywhere in it.
//
// THE WALK, exactly as ready-48f states it:
//
//   1  a FRESH nostr key on a FRESH browser profile receives an `rd board share` URL
//   2  it is opened OVER HTTPS and logged in through a REAL NIP-07 EXTENSION
//   3  the awaiting-authorization state names the fresh pubkey, and the board shows
//      PLACEHOLDERS — not plaintext, not blanks
//   4  the owner runs `rd grant --claim <nonce> <pubkey>` from the CLI
//   5  WITHOUT RELOADING, the open page picks up the kind-39301 grant, unwraps the
//      CEK through the extension, and the titles fill in live
//   6  what the page shows matches `rd list --json` for that board, read by an
//      independent rd
//   7  a SECOND fresh key presented with the SAME link gains no access — the claim
//      is spent
//   8  the zero-wait path: `rd board share <npub>` for a known key produces a link
//      that lands on a POPULATED board with no second command
//
// WHAT IS REAL HERE, AND WHY EACH PIECE HAD TO BE MADE REAL. The three clauses
// ready-48f was deferred over were each read as "needs a human". None of them
// did:
//
//  A REAL NIP-07 EXTENSION, not an injected window.nostr. Chromium loads unpacked
//  extensions (`--disable-extensions-except=<dir> --load-extension=<dir>`) in
//  `--headless=new`, and the whole surface is drivable over CDP. This script
//  clones nos2x (github.com/fiatjaf/nos2x, pinned by commit below), builds its
//  MV3 bundles with esbuild's API, loads it unpacked, and seeds THE EXTENSION'S
//  OWN `chrome.storage.local` — `private_key` plus the per-host `policies` record
//  its prompt flow writes — from an extension page over CDP. Every getPublicKey
//  and every nip44.decrypt then travels the real path: page -> nos2x's injected
//  `nostr-provider.js` -> window.postMessage -> content script -> MV3 service
//  worker -> nostr-tools. assertRealExtension() below refuses to proceed unless
//  the provider on the page is nos2x's own (its `_call`/`_requests` internals),
//  so an injected stand-in cannot silently satisfy this script. That is the gap
//  ready-bff's option (b) left open and ready-35a carried as a human release gate.
//
//  A MACHINE THAT IS NOT THE OWNER'S. The property that clause protects is that
//  the RELAY is the only channel. Each stranger here gets: a freshly generated
//  secp256k1 key that has never been granted anything, a Chromium USER PROFILE
//  DIRECTORY created for this run that has never seen the board, and (for the
//  CLI-side comparison) a clean RD_HOME whose nostr-log.jsonl does not exist
//  until `rd sync` fills it from the relay. Nothing but the relay carries state
//  between the owner and a stranger.
//
//  THE OWNER RUNNING `rd grant --claim`. That is a CLI command; this script runs
//  it, with the nonce decoded from the token the share link actually carried.
//
//  OVER HTTPS. The bundle is served by a real TLS server (self-signed, generated
//  per run); the page is a secure context (`window.isSecureContext` is asserted),
//  which is also what the relay leg requires — an https page may not open ws://,
//  and wss://relay.3dl.network is what the link carries.
//
// WHAT IS *NOT* REAL, stated plainly: the TLS certificate is self-signed and
// Chromium is launched with --ignore-certificate-errors, and the board host is
// 127.0.0.1 rather than ready.3dl.dev. Nothing else. The relay is the live
// wss://relay.3dl.network, the CLI is the real `rd` built from this tree, the
// signer is a real extension holding a real secret, and every event is really
// signed, really published and really read back.
//
// WHY THE OWNER'S KEY OWNS THE BOARD. wss://relay.3dl.network enforces a tenant
// WRITE allowlist, so the board owner must be a key that already holds write
// access — this script reads the local machine's rd signing key for that, exactly
// as live-parity.mjs and live-write-roundtrip.mjs do, and confines every event it
// writes to a FRESH, per-run board. The strangers never write: their whole role
// is to READ, which the relay serves to anyone.
//
// STEP 7 IS A DEMONSTRATION, NOT A DERIVATION. Single-use claim binding is
// already pinned as a conformance vector both rd and the browser consume byte for
// byte (claim_single_use_survives_revoke_rejects_reuse,
// testdata/fold.vectors.json). This script's job is to show it holding end to end
// through the real UI. If the live behaviour ever disagreed with that vector,
// that disagreement is the finding.
//
// THIS IS A MANUAL PROOF, NOT A CI JOB: it needs the live relay, a Chromium on
// disk, network access to fetch the extension's source, and the local machine's
// allowlisted rd key — none of which CI has.
//
// Usage:
//   node scripts/live-stranger-walk.mjs [--keep] [--relay wss://…]
// Exits non-zero on the first assertion that does not hold.

import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from "node:fs";
import https from "node:https";
import os from "node:os";
import path from "node:path";
import { createServer } from "vite";
// ready-153: the throwaway board this run provisions must not survive it. The
// contract — archive, prove the marker landed on the relay, and bracket the
// run with the owner's unarchived board count — lives in one module that
// scripts/throwaway-board.test.mjs exercises hermetically in CI.
import { openThrowawayBoardGuard, reportCleanup } from "./throwaway-board.mjs";

const BOARD_DIR = path.resolve(import.meta.dirname, "..");
const REPO_ROOT = path.resolve(BOARD_DIR, "../..");
const CHROME =
  process.env.CHROME_PATH ?? path.join(os.homedir(), ".cache/ms-playwright/chromium-1228/chrome-linux64/chrome");

const argv = process.argv.slice(2);
const KEEP = argv.includes("--keep");
const RELAY = argv.includes("--relay") ? argv[argv.indexOf("--relay") + 1] : "wss://relay.3dl.network";

/** nos2x, PINNED. A floating checkout would make this walk's signer drift under
 * it; the commit is what makes the transcript reproducible. */
const NOS2X_REPO = "https://github.com/fiatjaf/nos2x.git";
const NOS2X_COMMIT = "014493f9602d0a3826ef3eab2bdd4901ee315cce";
/** nos2x declares no lockfile and floats `nostr-tools: ^2.12.0`, which today
 * resolves @noble/curves 2.x — whose schnorr rejects the HEX secret key nos2x
 * stores and passes straight through (`expected Uint8Array, got type=string`).
 * Pinning the two @noble packages to the 1.x line the extension was released
 * against is what makes the shipped extension work, and is a dependency fact
 * about nos2x, not a change to it: not one byte of its source is patched here. */
const NOS2X_OVERRIDES = { "@noble/curves": "1.9.7", "@noble/hashes": "1.8.0" };

const RUN = Date.now().toString(36);
const BOARD_D = `s48f${RUN}`;

/** How long a live, no-reload pickup may take before it is called a failure. The
 * relay pushes within milliseconds; this is a generous bound on a scale-to-zero
 * relay, and the OBSERVED time is printed so a regression that merely got slow is
 * visible rather than absorbed. */
const LIVE_DEADLINE_MS = 45000;

const PLACEHOLDER = "[encrypted]";

const log = (...a) => console.log(...a);
const step = (s) => console.log(`\n── ${s}`);

let failures = 0;
const results = [];
function record(name, ok, detail = "") {
  if (!ok) failures++;
  results.push({ name, ok, detail });
  log(`  ${ok ? "PASS" : "FAIL"}  ${name}${detail ? ` — ${detail}` : ""}`);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

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
  send(method, params = {}, sessionId = this.sessionId) {
    const id = this.next++;
    const frame = { id, method, params };
    if (sessionId) frame.sessionId = sessionId;
    this.ws.send(JSON.stringify(frame));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }
  async evaluate(expression, sessionId = this.sessionId) {
    const r = await this.send(
      "Runtime.evaluate",
      { expression: `(async () => { ${expression} })()`, awaitPromise: true, returnByValue: true },
      sessionId,
    );
    if (r.exceptionDetails) {
      throw new Error(`page: ${r.exceptionDetails.exception?.description ?? r.exceptionDetails.text}`);
    }
    return r.result.value;
  }
}

async function waitFor(browser, expr, what, timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await browser.evaluate(`return !!(${expr});`)) return;
    if (Date.now() > deadline) {
      const dump = await browser.evaluate("return document.body.innerText.slice(0, 900);");
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

/** rdEcho runs an rd command and PRINTS the command and its output verbatim. The
 * done condition asks for a TRANSCRIPT, not a description of one, so every
 * command whose output is evidence goes through here. */
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

/** rdExpectFailure runs an rd command that MUST fail and returns its stderr. A
 * command that succeeds here is the assertion failing, so it throws. */
function rdExpectFailure(bin, cwd, home, args, label = "") {
  log(`\n$ rd ${args.join(" ")}${label ? `   # ${label}` : ""}`);
  try {
    const out = rd(bin, cwd, home, args);
    log(`  (UNEXPECTEDLY SUCCEEDED)\n${out}`);
    return { failed: false, message: out };
  } catch (err) {
    const message = `${err.stderr ?? ""}${err.stdout ?? ""}`.trim();
    log(
      message
        .split("\n")
        .map((l) => (l === "" ? "" : `  ${l}`))
        .join("\n"),
    );
    return { failed: true, message };
  }
}

/**
 * newReader builds a reader that has NEVER seen this browser's events: a
 * brand-new RD_HOME whose rd.json trusts the BOARD OWNER and nobody else, and a
 * brand-new project directory whose nostr-log.jsonl does not exist until `rd
 * sync` pulls it from the relay. The RELAY is therefore the only possible source
 * of everything it projects.
 */
function newReader(bin, tmp, coord, owner, tag, readerSecret) {
  const home = path.join(tmp, `reader-${tag}-home`);
  const dir = path.join(tmp, `reader-${tag}`);
  mkdirSync(home, { recursive: true });
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
        public: false,
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
 * bundles, so a key minted here and a key used there cannot disagree. */
async function mintKey(vite) {
  const mod = await vite.ssrLoadModule("/src/lib/schnorrsign.ts");
  const secret = mod.randomSecretHex();
  return { secret, pubkey: mod.xOnlyPubkey(secret) };
}

async function npubOf(vite, pubkeyHex) {
  const mod = await vite.ssrLoadModule("/src/lib/npub.ts");
  return mod.encodeNpub(pubkeyHex);
}

/** decodeClaimToken reads the rd1_ token out of a share URL's fragment through
 * the PAGE'S OWN parser, so the nonce this script hands to `rd grant --claim` is
 * the nonce the browser would have read from the same bytes. */
async function decodeShareLink(vite, url) {
  const mod = await vite.ssrLoadModule("/src/lib/fragment.ts");
  const frag = url.slice(url.indexOf("#") + 1);
  return mod.parseFragment(frag);
}

// ── the real NIP-07 extension ───────────────────────────────────────────────

/**
 * buildNos2x fetches nos2x at its pinned commit and builds its MV3 bundles with
 * esbuild's API — the same entry points, outdir and `define` its own build.js
 * declares. Nothing in extension/ is patched: the built directory is what
 * `--load-extension` is pointed at.
 *
 * Cached under the OS temp dir keyed by commit, so a second run of this script
 * does not re-clone or re-install.
 */
async function buildNos2x(esbuild) {
  const dir = path.join(os.tmpdir(), `rd-nos2x-${NOS2X_COMMIT.slice(0, 12)}`);
  const ext = path.join(dir, "extension");
  if (!existsSync(path.join(ext, "background.build.js"))) {
    if (!existsSync(path.join(dir, ".git"))) {
      rmSync(dir, { recursive: true, force: true });
      execFileSync("git", ["clone", "--quiet", NOS2X_REPO, dir], { stdio: "inherit" });
      execFileSync("git", ["checkout", "--quiet", NOS2X_COMMIT], { cwd: dir, stdio: "inherit" });
    }
    const pkgPath = path.join(dir, "package.json");
    const pkg = JSON.parse(readFileSync(pkgPath, "utf8"));
    pkg.overrides = NOS2X_OVERRIDES;
    writeFileSync(pkgPath, JSON.stringify(pkg, null, 2));
    execFileSync("npm", ["install", "--no-audit", "--no-fund"], { cwd: dir, stdio: "inherit" });
    await esbuild.build({
      bundle: true,
      absWorkingDir: dir,
      entryPoints: {
        "popup.build": "./extension/popup.jsx",
        "styles.build": "./extension/styles.css",
        "prompt.build": "./extension/prompt.jsx",
        "options.build": "./extension/options.jsx",
        "background.build": "./extension/background.js",
        "content-script.build": "./extension/content-script.js",
      },
      outdir: "./extension",
      sourcemap: false,
      define: { window: "self", global: "self" },
    });
  }
  const manifest = JSON.parse(readFileSync(path.join(ext, "manifest.json"), "utf8"));
  return { dir: ext, version: manifest.version, name: manifest.name };
}

/**
 * A Stranger is one machine-that-is-not-the-owner's: its own fresh key, its own
 * Chromium process, its own never-before-used user profile directory, and the
 * real extension loaded unpacked with that key seeded into the extension's own
 * storage.
 */
async function launchStranger(tmp, tag, key, extDir, host) {
  const profile = path.join(tmp, `profile-${tag}`);
  mkdirSync(profile, { recursive: true });
  const chrome = spawn(
    CHROME,
    [
      "--headless=new",
      "--remote-debugging-port=0",
      "--no-sandbox",
      "--disable-gpu",
      // Self-signed cert, generated for this run. This is the ONE thing the
      // walk fakes about https; the context is still secure (asserted below).
      "--ignore-certificate-errors",
      `--disable-extensions-except=${extDir}`,
      `--load-extension=${extDir}`,
      `--user-data-dir=${profile}`,
      "about:blank",
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  const wsUrl = await new Promise((resolve, reject) => {
    let buf = "";
    const t = setTimeout(() => reject(new Error(`chromium (${tag}) printed no devtools url:\n${buf}`)), 30000);
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

  // Find nos2x by the service worker its own manifest names. Chromium also runs
  // a component extension of its own, so identifying "an extension" is not
  // enough — this matches the background bundle nos2x declares.
  let extId = "";
  for (let i = 0; i < 60 && !extId; i++) {
    const { targetInfos } = await browser.send("Target.getTargets");
    const t = targetInfos.find(
      (t) => t.url.startsWith("chrome-extension://") && t.url.endsWith("/background.build.js"),
    );
    if (t) extId = new URL(t.url).host;
    else await sleep(250);
  }
  if (!extId) throw new Error(`${tag}: the nos2x service worker never appeared — the extension did not load`);

  // SEED THE EXTENSION'S OWN STORAGE, from an extension page, over CDP: the
  // secret key exactly as nos2x's options page writes it (64-hex), and the
  // per-host policy records its prompt flow writes on "allow" — so the signer
  // answers without a human clicking a popup, and answers through the same code
  // path a human-approved install would.
  const { targetId } = await browser.send("Target.createTarget", { url: `chrome-extension://${extId}/options.html` });
  const { sessionId } = await browser.send("Target.attachToTarget", { targetId, flatten: true });
  await browser.send("Runtime.enable", {}, sessionId);
  const stored = await browser.evaluate(
    `
    const host = ${JSON.stringify(host)};
    const policies = { [host]: { true: {} } };
    for (const t of ["getPublicKey", "signEvent", "nip04.encrypt", "nip04.decrypt", "nip44.encrypt", "nip44.decrypt"]) {
      policies[host].true[t] = { conditions: {}, created_at: Math.round(Date.now() / 1000) };
    }
    await chrome.storage.local.set({ private_key: ${JSON.stringify(key.secret)}, policies });
    const back = await chrome.storage.local.get(["private_key", "policies"]);
    return JSON.stringify({ keyStored: typeof back.private_key === "string" && back.private_key.length === 64,
                            hosts: Object.keys(back.policies ?? {}) });
  `,
    sessionId,
  );
  await browser.send("Target.closeTarget", { targetId });
  const seeded = JSON.parse(stored);
  if (!seeded.keyStored || !seeded.hosts.includes(host)) {
    throw new Error(`${tag}: extension storage did not take the seed: ${stored}`);
  }

  // The tab the walk actually happens in.
  const page = await browser.send("Target.createTarget", { url: "about:blank" });
  const att = await browser.send("Target.attachToTarget", { targetId: page.targetId, flatten: true });
  browser.sessionId = att.sessionId;
  await browser.send("Page.enable");
  await browser.send("Runtime.enable");
  return { tag, key, browser, profile, extId, close: () => chrome.kill() };
}

/**
 * assertRealExtension refuses to let this script claim an extension handshake it
 * did not have. window.nostr is checked to be NOS2X'S OWN provider — the
 * `_call`/`_requests` internals of extension/nostr-provider.js, which an injected
 * signer would have no reason to carry — and the pubkey it answers with must be
 * the one seeded into THIS profile's extension storage, so a stale or shared
 * signer is caught too.
 */
async function assertRealExtension(s) {
  const probe = JSON.parse(
    await s.browser.evaluate(`
      const n = window.nostr;
      const shape = {
        present: typeof n,
        nos2xProvider: typeof n?._call === "function" && typeof n?._requests === "object",
        nip44Decrypt: typeof n?.nip44?.decrypt === "function",
        secureContext: window.isSecureContext,
        protocol: location.protocol,
      };
      shape.pubkey = await n.getPublicKey();
      return JSON.stringify(shape);
    `),
  );
  const problems = [];
  if (probe.present !== "object") problems.push(`window.nostr is ${probe.present}`);
  if (!probe.nos2xProvider) problems.push("window.nostr is not nos2x's provider (no _call/_requests)");
  if (!probe.nip44Decrypt) problems.push("the provider offers no nip44.decrypt");
  if (!probe.secureContext) problems.push("the page is not a secure context");
  if (probe.protocol !== "https:") problems.push(`protocol is ${probe.protocol}`);
  if (probe.pubkey !== s.key.pubkey) problems.push(`extension answered ${probe.pubkey}, seeded ${s.key.pubkey}`);
  return { ok: problems.length === 0, detail: problems.join("; ") || `nos2x answered ${probe.pubkey} over https`, probe };
}

/**
 * loginWithExtension clicks the page's own NIP-07 button — the same control a
 * human clicks — in ONE document, with NO reload.
 *
 * WHAT IT MEASURES ALONG THE WAY, AND WHY THAT IS EVIDENCE. A REAL extension
 * injects window.nostr ASYNCHRONOUSLY: nos2x's content script runs at
 * document_end and appends a <script src=chrome-extension://…/nostr-provider.js>
 * which must then be fetched and executed. The board's bundle is a deferred
 * module and can render its login form first. main.ts used to sample
 * hasNip07Extension() ONCE, at render, and leave the only NIP-07 control on the
 * page disabled for the life of that document — measured dead on 6 loads out of
 * 6 on a cold profile, recoverable only by a reload that could lose again. A
 * CDP-injected window.nostr is installed before any page script and cannot
 * observe this at all, which is why no previous proof found it.
 *
 * `disabledAtFirstPaint` reports whether the race happened on this run;
 * `recoveredMs` reports how long the page took to notice the extension WITHOUT a
 * reload. A run where the race happened and the button never recovered is the
 * regression; a run where it recovered is the fix holding on real timing.
 */
async function loginWithExtension(s, url, timeoutMs = 20000) {
  await s.browser.send("Page.navigate", { url: "about:blank" });
  await sleep(300);
  await s.browser.send("Page.navigate", { url });
  await waitFor(s.browser, `document.querySelector("button")`, `${s.tag}: the login page`, 40000);
  const out = JSON.parse(
    await s.browser.evaluate(`
      const btn = [...document.querySelectorAll("button")].find((b) => /extension/i.test(b.textContent ?? ""));
      if (!btn) throw new Error("no NIP-07 login button");
      const disabledAtFirstPaint = btn.disabled;
      const t0 = performance.now();
      const deadline = t0 + ${timeoutMs};
      while (btn.disabled && performance.now() < deadline) {
        await new Promise((r) => setTimeout(r, 100));
      }
      if (btn.disabled) {
        return JSON.stringify({ clicked: false, disabledAtFirstPaint, nostr: typeof window.nostr,
                                recoveredMs: -1, title: btn.title });
      }
      btn.click();
      return JSON.stringify({ clicked: true, disabledAtFirstPaint, nostr: typeof window.nostr,
                              recoveredMs: Math.round(performance.now() - t0), title: btn.title });
    `),
  );
  if (!out.clicked) {
    throw new Error(
      `${s.tag}: the page's NIP-07 button never became clickable in one document ` +
        `(window.nostr is ${out.nostr}, button title ${JSON.stringify(out.title)})`,
    );
  }
  return out;
}

/** boardState is what the LIVE DOM shows right now. The only witness for "the
 * board shows X". */
async function boardState(s) {
  return JSON.parse(
    await s.browser.evaluate(`
      const cards = [...document.querySelectorAll(".card")].map((c) => ({
        id: c.querySelector(".card-id")?.textContent?.trim() ?? "",
        title: c.querySelector(".card-title")?.textContent?.trim() ?? "",
        column: c.closest(".column")?.dataset.column ?? "(none)",
      }));
      return JSON.stringify(cards);
    `),
  );
}

async function pageText(s) {
  return s.browser.evaluate("return document.body.innerText.slice(0, 1200);");
}

async function main() {
  if (!existsSync(CHROME)) throw new Error(`no Chromium at ${CHROME} (set CHROME_PATH)`);

  const tmp = mkdtempSync(path.join(os.tmpdir(), "rd-48f-"));
  const cleanup = [];
  // Hoisted out of the try block so the `finally` clause below can archive the
  // throwaway board EVEN WHEN THE SCRIPT FAILS PARTWAY — a red run must not
  // leave a permanent stray in the owner's portfolio (ready-153).
  let rdBin, projectDir, writerHome;

  const idPath = path.join(
    process.env.RD_HOME ?? path.join(process.env.XDG_CONFIG_HOME ?? path.join(os.homedir(), ".config"), "rd"),
    "nostr-identity.json",
  );
  const identity = JSON.parse(readFileSync(idPath, "utf8"));

  // ready-153: opened BEFORE anything is created, so it holds the owner's
  // unarchived board count as it was before this process existed, and BOARD_D
  // is registered before `rd init` runs — a run that dies inside `rd init` has
  // no coordinate to hand back but may already have published the board.
  // Until this landed, every run of this script added one permanent node to
  // the owner's portfolio and nothing removed it.
  step("ready-153: count this key's unarchived boards BEFORE the run");
  const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: identity.pubkey_hex, log });
  guard.expect(BOARD_D);

  try {
    step("build rd from this tree");
    rdBin = path.join(tmp, "rd");
    execFileSync("go", ["build", "-o", rdBin, "./cmd/rd"], { cwd: REPO_ROOT, stdio: "inherit" });

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

    step("fetch and build the REAL NIP-07 extension (nos2x, unpacked)");
    const ext = await buildNos2x(esbuild);
    log(`  ${ext.name} ${ext.version} @ ${NOS2X_COMMIT.slice(0, 12)} -> ${ext.dir}`);

    step("provision the throwaway CONFIDENTIAL board (owner key, fresh board-d)");
    writerHome = path.join(tmp, "writer-home");
    mkdirSync(writerHome, { recursive: true });
    writeFileSync(path.join(writerHome, "nostr-identity.json"), JSON.stringify(identity), { mode: 0o600 });

    projectDir = path.join(tmp, BOARD_D);
    mkdirSync(projectDir, { recursive: true });
    const initOut = JSON.parse(
      rd(rdBin, projectDir, writerHome, [
        "init",
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

    step("seed the items the stranger is supposed to end up seeing");
    const SEEDS = [
      { id: `${BOARD_D}-alpha`, title: "stranger walk: the first card" },
      { id: `${BOARD_D}-beta`, title: "stranger walk: the second card" },
      { id: `${BOARD_D}-gamma`, title: "stranger walk: the third card" },
    ];
    for (const s of SEEDS) {
      rd(rdBin, projectDir, writerHome, ["create", s.title, "--id", s.id, "--type", "task", "--priority", "p2"]);
    }
    rd(rdBin, projectDir, writerHome, ["relay", "flush"]);
    log(`  ${SEEDS.length} items published`);

    step("build the shipped bundle and serve it OVER HTTPS");
    execFileSync("npx", ["vite", "build", "--outDir", path.join(tmp, "dist"), "--emptyOutDir"], {
      cwd: BOARD_DIR,
      stdio: "inherit",
    });
    const dist = path.join(tmp, "dist");
    const keyPem = path.join(tmp, "tls-key.pem");
    const certPem = path.join(tmp, "tls-cert.pem");
    execFileSync(
      "openssl",
      [
        "req", "-x509", "-newkey", "rsa:2048", "-nodes",
        "-keyout", keyPem, "-out", certPem, "-days", "1",
        "-subj", "/CN=127.0.0.1",
        "-addext", "subjectAltName=IP:127.0.0.1,DNS:localhost",
      ],
      { stdio: ["ignore", "ignore", "ignore"] },
    );
    const server = https.createServer(
      { key: readFileSync(keyPem), cert: readFileSync(certPem) },
      (req, res) => {
        let rel = decodeURIComponent((req.url ?? "/").split("?")[0].split("#")[0]);
        // The production bundle is built with base "/board/", so the server
        // mounts dist at that same path — serving it at "/" would 404 every
        // asset. "/board" with no trailing slash is the link's own shape.
        if (rel === "/board" || rel === "/board/") rel = "/";
        else if (rel.startsWith("/board/")) rel = rel.slice("/board".length);
        const file = path.join(dist, rel === "/" ? "index.html" : rel);
        if (!file.startsWith(dist) || !existsSync(file)) {
          res.writeHead(404).end("not found");
          return;
        }
        const type = file.endsWith(".js") ? "text/javascript" : file.endsWith(".css") ? "text/css" : "text/html";
        res.writeHead(200, { "content-type": type }).end(readFileSync(file));
      },
    );
    await new Promise((r) => server.listen(0, "127.0.0.1", r));
    cleanup.push(() => server.close());
    const port = server.address().port;
    const hostPort = `127.0.0.1:${port}`;
    const boardHost = `https://${hostPort}/board`;
    log(`  serving the built bundle at ${boardHost} (TLS, self-signed for this run)`);

    // ── STEP 1 ────────────────────────────────────────────────────────────────
    step("STEP 1 — a fresh key on a fresh profile receives an `rd board share` URL");
    const shareOut = rdEcho(rdBin, projectDir, writerHome, ["board", "share", "--host", boardHost], "unknown-key share");
    const shareURL = shareOut.trim().split("\n").pop().trim();
    const parsedShare = await decodeShareLink(vite, shareURL);
    record(
      "`rd board share` minted a claim-nonce link for this board",
      parsedShare.kind === "claim" && parsedShare.payload.board === coord && !!parsedShare.payload.claim,
      `kind=${parsedShare.kind} board=${parsedShare.payload?.board}`,
    );
    record(
      "the share link carries NO key material",
      !/[?&#]cek=|[?&#]pk=|[?&#]keys=/.test(shareURL),
      shareURL.replace(/rd1_[A-Za-z0-9_-]+/, "rd1_<token>"),
    );
    const nonce = parsedShare.payload.claim;

    const alice = await mintKey(vite);
    const aliceNpub = await npubOf(vite, alice.pubkey);
    log(`  stranger A pubkey ${alice.pubkey}`);
    log(`  stranger A npub   ${aliceNpub}`);
    const a = await launchStranger(tmp, `A-${RUN}`, alice, ext.dir, hostPort);
    cleanup.push(() => a.close());
    log(`  stranger A profile ${a.profile} (created for this run; has never seen this board)`);
    log(`  stranger A extension ${a.extId}`);

    // ── STEP 2 ────────────────────────────────────────────────────────────────
    step("STEP 2 — open it over https and log in through the REAL extension");
    const aLogin = await loginWithExtension(a, shareURL);
    const realExt = await assertRealExtension(a);
    record("stranger A logged in through a REAL NIP-07 extension over https", realExt.ok, realExt.detail);
    record(
      "the page's NIP-07 button became clickable in ONE document, with no reload",
      aLogin.clicked,
      aLogin.disabledAtFirstPaint
        ? `the real extension lost the injection race at first paint; the page noticed it ${aLogin.recoveredMs}ms later, with no reload`
        : "the extension had already injected window.nostr when the login form rendered",
    );

    // The no-reload sentinel for step 5. Set AFTER load on the page object; a
    // reload, a navigation or a document replacement clears it.
    const SENTINEL = `ready48f-${RUN}-${Math.random().toString(36).slice(2)}`;
    await a.browser.evaluate(`window.__ready48f = ${JSON.stringify(SENTINEL)}; return true;`);

    // ── STEP 3 ────────────────────────────────────────────────────────────────
    step("STEP 3 — awaiting-authorization names the fresh pubkey, and the board shows PLACEHOLDERS");
    await sleep(4000);
    const awaitingText = await pageText(a);
    log(`  page text:\n${awaitingText.split("\n").map((l) => `    ${l}`).join("\n")}`);
    record(
      "the awaiting-authorization state names stranger A's own npub",
      awaitingText.includes(aliceNpub),
      `looking for ${aliceNpub}`,
    );

    // Give the board leg as long as the live deadline: this is the "reaches a
    // populated board" half of step 3, and a slow relay must not read as a
    // structural absence.
    let beforeGrant = [];
    const boardDeadline = Date.now() + LIVE_DEADLINE_MS;
    for (;;) {
      beforeGrant = await boardState(a);
      if (beforeGrant.length > 0 || Date.now() > boardDeadline) break;
      await sleep(1000);
    }
    log(`  board BEFORE the grant (live DOM): ${JSON.stringify(beforeGrant)}`);
    const seedTitles = new Set(SEEDS.map((s) => s.title));
    record(
      "the ungranted stranger sees the board's cards, not a blank page",
      beforeGrant.length === SEEDS.length,
      `${beforeGrant.length} cards rendered, ${SEEDS.length} expected`,
    );
    record(
      "every title the ungranted stranger sees is the PLACEHOLDER, never plaintext",
      beforeGrant.length > 0 && beforeGrant.every((c) => c.title === PLACEHOLDER),
      `titles=${JSON.stringify(beforeGrant.map((c) => c.title))}`,
    );
    record(
      "no seeded plaintext title appears anywhere on the ungranted page",
      ![...seedTitles].some((t) => awaitingText.includes(t)),
      "",
    );

    // ── STEP 4 ────────────────────────────────────────────────────────────────
    step("STEP 4 — the owner runs `rd grant --claim <nonce> <pubkey>`");
    const grantAt = Date.now();
    rdEcho(
      rdBin,
      projectDir,
      writerHome,
      ["grant", alice.pubkey, "contributor", "--claim", nonce],
      "binds the one-use nonce to stranger A's key",
    );
    rdEcho(rdBin, projectDir, writerHome, ["relay", "flush"], "nothing should be queued — rd publishes directly");

    // ── STEP 5 ────────────────────────────────────────────────────────────────
    step("STEP 5 — WITHOUT RELOADING, the open page picks up the grant and the titles fill in");
    let after = beforeGrant;
    let liveMs = -1;
    for (;;) {
      after = await boardState(a);
      const filled = after.length > 0 && after.every((c) => seedTitles.has(c.title));
      if (filled) {
        liveMs = Date.now() - grantAt;
        break;
      }
      if (Date.now() - grantAt > LIVE_DEADLINE_MS) break;
      await sleep(500);
    }
    log(`  board AFTER  the grant (live DOM): ${JSON.stringify(after)}`);
    record(
      "the titles filled in live, with no reload",
      liveMs >= 0,
      liveMs >= 0
        ? `${liveMs}ms after the grant was published`
        : `still ${JSON.stringify(after.map((c) => c.title))} after ${LIVE_DEADLINE_MS}ms`,
    );
    const sentinel = await a.browser.evaluate("return window.__ready48f ?? null;");
    record(
      "the page was NEVER reloaded — same document, same JS context",
      sentinel === SENTINEL,
      sentinel === SENTINEL ? "sentinel intact" : `sentinel is ${JSON.stringify(sentinel)} — the document was replaced`,
    );

    // ── STEP 6 ────────────────────────────────────────────────────────────────
    step("STEP 6 — what the page shows matches `rd list --json` for that board");
    const reader = newReader(rdBin, tmp, coord, owner, `alice-${RUN}`, alice);
    log(`  RD_HOME=${reader.home}  (rd.json trusted_pubkeys=["${owner}"])`);
    log(`  project=${reader.dir}  (nostr-log.jsonl created by 'rd sync' from ${RELAY} and nothing else)`);
    rdEcho(rdBin, reader.dir, reader.home, ["list", "--json", "--all"], "the independent reader's view");
    const byRd = readerItems(rdBin, reader);
    const domTitles = Object.fromEntries((await boardState(a)).map((c) => [c.id, c.title]));
    const rdTitles = Object.fromEntries([...byRd.values()].map((i) => [i.id, i.title]));
    const mismatched = Object.keys({ ...domTitles, ...rdTitles }).filter((id) => domTitles[id] !== rdTitles[id]);
    record(
      "the visible items match `rd list --json` for that board, id for id and title for title",
      mismatched.length === 0 && Object.keys(domTitles).length === SEEDS.length,
      mismatched.length === 0
        ? `${Object.keys(domTitles).length} items agree`
        : `disagree on ${JSON.stringify(mismatched)}: page=${JSON.stringify(domTitles)} rd=${JSON.stringify(rdTitles)}`,
    );

    // ── STEP 7 ────────────────────────────────────────────────────────────────
    step("STEP 7 — a SECOND fresh key presented with the SAME link gains no access");
    const bob = await mintKey(vite);
    const bobNpub = await npubOf(vite, bob.pubkey);
    log(`  stranger B pubkey ${bob.pubkey}`);
    log(`  stranger B npub   ${bobNpub}`);
    const b = await launchStranger(tmp, `B-${RUN}`, bob, ext.dir, hostPort);
    cleanup.push(() => b.close());
    log(`  stranger B profile ${b.profile} (its own Chromium, its own profile, its own extension storage)`);
    const bLogin = await loginWithExtension(b, shareURL);
    log(`  stranger B: disabled at first paint=${bLogin.disabledAtFirstPaint}, clickable after ${bLogin.recoveredMs}ms`);
    const realExtB = await assertRealExtension(b);
    record("stranger B logged in through its own REAL extension over https", realExtB.ok, realExtB.detail);

    const spent = rdExpectFailure(
      rdBin,
      projectDir,
      writerHome,
      ["grant", bob.pubkey, "contributor", "--claim", nonce],
      "the SAME nonce, a DIFFERENT key",
    );
    record(
      "`rd grant --claim` refuses to bind the spent nonce to a second key",
      spent.failed && /already consumed by pubkey/.test(spent.message),
      spent.failed ? spent.message.split("\n")[0] : "the command SUCCEEDED — single-use claim binding did not hold",
    );

    await sleep(Math.max(6000, 0));
    const bState = await boardState(b);
    const bText = await pageText(b);
    log(`  stranger B board (live DOM): ${JSON.stringify(bState)}`);
    record(
      "stranger B's page never shows a decrypted title",
      bState.every((c) => c.title === PLACEHOLDER) && ![...seedTitles].some((t) => bText.includes(t)),
      `titles=${JSON.stringify(bState.map((c) => c.title))}`,
    );
    const bReader = newReader(rdBin, tmp, coord, owner, `bob-${RUN}`, bob);
    const bReaderTitles = [...readerItems(rdBin, bReader).values()].map((i) => i.title);
    record(
      "an independent rd holding stranger B's key reads only placeholders off the relay",
      bReaderTitles.length > 0 && bReaderTitles.every((t) => t === PLACEHOLDER),
      `rd list titles=${JSON.stringify(bReaderTitles)}`,
    );

    // ── STEP 8 ────────────────────────────────────────────────────────────────
    step("STEP 8 — the zero-wait path: `rd board share <npub>` lands on a POPULATED board");
    const carol = await mintKey(vite);
    const carolNpub = await npubOf(vite, carol.pubkey);
    log(`  stranger C pubkey ${carol.pubkey}`);
    log(`  stranger C npub   ${carolNpub}`);
    const zeroWaitOut = rdEcho(
      rdBin,
      projectDir,
      writerHome,
      ["board", "share", carolNpub, "--host", boardHost],
      "grants AND prints the link, one command",
    );
    const zeroWaitURL = zeroWaitOut.trim().split("\n").pop().trim();
    const parsedZero = await decodeShareLink(vite, zeroWaitURL);
    record(
      "the known-key share link is a plain board link with no claim-nonce and no key material",
      parsedZero.kind === "board" && parsedZero.board === coord && parsedZero.keys?.ceks?.length === undefined,
      `kind=${parsedZero.kind} board=${parsedZero.board}`,
    );
    rd(rdBin, projectDir, writerHome, ["relay", "flush"]);

    const c = await launchStranger(tmp, `C-${RUN}`, carol, ext.dir, hostPort);
    cleanup.push(() => c.close());
    log(`  stranger C profile ${c.profile}`);
    const cLogin = await loginWithExtension(c, zeroWaitURL);
    log(`  stranger C: disabled at first paint=${cLogin.disabledAtFirstPaint}, clickable after ${cLogin.recoveredMs}ms`);
    const realExtC = await assertRealExtension(c);
    record("stranger C logged in through its own REAL extension over https", realExtC.ok, realExtC.detail);

    let cState = [];
    const cDeadline = Date.now() + LIVE_DEADLINE_MS;
    for (;;) {
      cState = await boardState(c);
      if (cState.length === SEEDS.length && cState.every((x) => seedTitles.has(x.title))) break;
      if (Date.now() > cDeadline) break;
      await sleep(500);
    }
    log(`  stranger C board (live DOM): ${JSON.stringify(cState)}`);
    record(
      "one command, no second step: stranger C's first visit shows the POPULATED board",
      cState.length === SEEDS.length && cState.every((x) => seedTitles.has(x.title)),
      `${cState.length} cards, titles=${JSON.stringify(cState.map((x) => x.title))}`,
    );

    // ── SUMMARY ───────────────────────────────────────────────────────────────
    step("SUMMARY");
    for (const r of results) log(`  ${r.ok ? "PASS" : "FAIL"}  ${r.name}${r.detail ? ` — ${r.detail}` : ""}`);
    log("");
    log(`board:      ${coord} (CONFIDENTIAL)`);
    log(`relay:      ${RELAY}`);
    log(`host:       ${boardHost} (TLS, self-signed per run)`);
    log(`extension:  ${ext.name} ${ext.version} @ ${NOS2X_COMMIT.slice(0, 12)}, loaded unpacked`);
    log(`stranger A: ${alice.pubkey}  profile ${path.basename(`profile-A-${RUN}`)}`);
    log(`stranger B: ${bob.pubkey}  profile ${path.basename(`profile-B-${RUN}`)}`);
    log(`stranger C: ${carol.pubkey}  profile ${path.basename(`profile-C-${RUN}`)}`);
    log(`items:      ${SEEDS.length} seeded`);
    log(`${results.filter((r) => r.ok).length}/${results.length} assertions held`);
  } finally {
    for (const c of cleanup.reverse()) {
      try {
        c();
      } catch {
        /* best effort */
      }
    }
    // ready-153: this board exists ONLY to be thrown away. Cleaned up here, in
    // `finally` rather than after a happy-path return, so a run that fails
    // partway (or even before Chromium ever opens) still does not leave a
    // permanent stray in the owner's portfolio — and BEFORE the rmSync below,
    // because `rd board archive` runs inside the project dir under tmp.
    //
    // guard.close() archives whatever the RELAY says this run published,
    // reads the archived marker back off the relay (an `rd board archive`
    // that exits 0 without the event landing is not success), and re-checks
    // the owner's unarchived board count against the one taken before the
    // run. Its failures count toward this script's exit code — an archive
    // problem that is only logged lets the process exit 0 with the stray
    // still in the portfolio.
    step("ready-153: archive the throwaway board, and prove the owner's board count is unchanged");
    failures += reportCleanup(await guard.close({ rdBin, cwd: projectDir, home: writerHome }), log);
    if (KEEP) log(`\nkept: ${tmp}`);
    else rmSync(tmp, { recursive: true, force: true });
  }

  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error(`\nFAILED: ${err.stack ?? err.message}`);
  process.exit(1);
});
