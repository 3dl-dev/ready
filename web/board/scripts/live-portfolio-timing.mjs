#!/usr/bin/env node
// live-portfolio-timing.mjs — ready-fe4's MEASUREMENT INSTRUMENT.
//
// ready-27b shipped the portfolio view and measured its first paint at 97.2s
// against wss://relay.3dl.network with 75 boards. That number is the baseline
// this item has to move, so it needs an instrument that (a) runs the SHIPPED
// modules, not a re-implementation, and (b) splits the wall clock into phases,
// because "the page is slow" is not actionable and "N seconds of that is
// re-verifying the same signatures 75 times" is.
//
// WHAT IS REAL HERE: the relay (a real WebSocket to wss://relay.3dl.network),
// the discovery walk (the page's own kind-only paged fetch — NEVER an `authors`
// filter, which under-returns on this relay), the fold, the keyring, the
// confidentiality decision, and the CEK unwrap (real NIP-44 v2 against the
// local machine's own rd key, same one-time same-machine reason
// live-parity.mjs reads it). The secret is never printed and never written.
//
// WHAT IS NOT REAL: the browser. This runs the modules under Node via Vite's
// ssrLoadModule, so it measures the load pipeline and NOT paint/layout. That is
// the right instrument for this item — the 97.2s was network + signature
// verification, both of which are identical here — and live-portfolio.mjs
// remains the headless-Chromium proof of what a person sees.
//
//   node scripts/live-portfolio-timing.mjs [--relay wss://…] [--boards N]
//                                          [--repeat N]
//
// Prints a phase table and the totals. Exits 0 unless the run itself failed.

import { createServer } from "vite";
import { readFileSync } from "node:fs";
import path from "node:path";
import os from "node:os";

const BOARD_DIR = path.resolve(import.meta.dirname, "..");

function arg(name, dflt) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] !== undefined ? process.argv[i + 1] : dflt;
}

function rdHome() {
  if (process.env.RD_HOME) return process.env.RD_HOME;
  const xdg = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(xdg, "rd");
}

function loadLocalKey() {
  const raw = JSON.parse(readFileSync(path.join(rdHome(), "nostr-identity.json"), "utf8"));
  if (!raw.secret_hex || !raw.pubkey_hex) throw new Error("nostr-identity.json: missing secret_hex/pubkey_hex");
  return { secretHex: raw.secret_hex, pubkeyHex: raw.pubkey_hex };
}

// The page never runs without a document; main.ts calls main() at import. A
// null #app makes main() return immediately, which is exactly what this
// instrument wants — it drives loadBoardItems itself.
function installDomStub() {
  if (globalThis.document) return;
  globalThis.document = {
    getElementById: () => null,
    createElement: () => ({ append() {}, addEventListener() {} }),
    addEventListener() {},
    removeEventListener() {},
  };
  globalThis.window = globalThis;
}

async function main() {
  const relayUrl = arg("--relay", "wss://relay.3dl.network");
  const boardCap = Number(arg("--boards", "0"));
  const relays = [relayUrl];
  installDomStub();

  const server = await createServer({
    root: BOARD_DIR,
    configFile: false,
    logLevel: "warn",
    server: { middlewareMode: true },
    appType: "custom",
    optimizeDeps: { noDiscovery: true },
  });

  try {
    const relayMod = await server.ssrLoadModule("/src/lib/relay.ts");
    const discovery = await server.ssrLoadModule("/src/lib/boarddiscovery.ts");
    const nip44 = await server.ssrLoadModule("/src/lib/nip44.ts");
    const sha256mod = await server.ssrLoadModule("/src/lib/sha256.ts");
    const auth = await server.ssrLoadModule("/src/lib/auth.ts");
    const mainMod = await server.ssrLoadModule("/src/main.ts");

    const { secretHex, pubkeyHex } = loadLocalKey();
    // The same contract nip07KeyUnwrapper implements, backed by a real NIP-44
    // v2 open instead of an extension. Accepts BOTH wrap forms: 64 hex chars
    // (post-ready-c4b, browser-safe) and 32 raw bytes (pre-ready-c4b grants).
    const unwrap = async (counterparty, wrapped) => {
      const out = nip44.open(secretHex, counterparty, wrapped);
      if (out === null) return null;
      if (out.length === 32) return out;
      if (out.length === 64) {
        const s = new TextDecoder().decode(out);
        if (/^[0-9a-fA-F]{64}$/.test(s)) return sha256mod.hexToBytes(s.toLowerCase());
      }
      return null;
    };

    const probeEvents = [];
    let fetchCount = 0;
    let fetchedEvents = 0;
    let fetchMs = 0;
    const deps = {
      loadRelays: async () => relays,
      fetchEvents: async (r, filter, opts) => {
        fetchCount++;
        const t = Date.now();
        const out = await relayMod.fetchEventsFromRelays(r, filter, opts ?? {});
        fetchMs += Date.now() - t;
        fetchedEvents += out.length;
        if (probeEvents.length < 2000) probeEvents.push(...out.slice(0, 2000 - probeEvents.length));
        return out;
      },
      keyUnwrapper: () => unwrap,
    };
    const identity = { pubkey: pubkeyHex, auth: auth.authTransition({ type: "login", method: "extension" }) };

    const t0 = Date.now();
    const authorityEvents = await deps.fetchEvents(relays, { kinds: [30301, 39301] }, {});
    const tAuthority = Date.now();
    let boards = discovery.discoverOwnerBoards(authorityEvents, [pubkeyHex]);
    if (boardCap > 0) boards = boards.slice(0, boardCap);
    const tDiscovery = Date.now();

    const perBoard = [];
    let firstBoardAt;
    const result = await mainMod.loadBoardItems(
      boards,
      relays,
      authorityEvents,
      identity,
      deps,
      () => {},
      undefined,
      {
        onBoard: (r) => {
          if (firstBoardAt === undefined && r.items.length > 0) firstBoardAt = Date.now();
          perBoard.push({ coord: r.coord, items: r.items.length, at: Date.now() });
        },
      },
    );
    const tItems = Date.now();

    console.log(`relay                 ${relayUrl}`);
    console.log(`authority events      ${authorityEvents.length}`);
    console.log(`boards                ${boards.length}`);
    console.log(`items                 ${result.items.length}`);
    console.log(`relay fetches         ${fetchCount} (${fetchedEvents} events)`);
    console.log(`--`);
    console.log(`authority fetch       ${((tAuthority - t0) / 1000).toFixed(1)}s`);
    console.log(`discovery             ${((tDiscovery - tAuthority) / 1000).toFixed(1)}s`);
    console.log(`per-board load        ${((tItems - tDiscovery) / 1000).toFixed(1)}s`);
    if (firstBoardAt !== undefined) {
      console.log(`TIME TO FIRST BOARD   ${((firstBoardAt - t0) / 1000).toFixed(1)}s  <- what the reader waits for`);
    }
    const half = perBoard.filter((b) => b.items > 0)[Math.floor(perBoard.filter((b) => b.items > 0).length / 2)];
    if (half) console.log(`half the boards by    ${((half.at - t0) / 1000).toFixed(1)}s`);
    console.log(`  of which in relay   ${(fetchMs / 1000).toFixed(1)}s (sum of fetch durations)`);
    if (process.argv.includes("--probe-verify")) {
      const nostrevent = await server.ssrLoadModule("/src/lib/nostrevent.ts");
      nostrevent.resetVerifyMemo();
      const sample = probeEvents.slice(0, 2000);
      const tv = Date.now();
      nostrevent.verifiedEvents(sample);
      const dv = Date.now() - tv;
      console.log(`verify probe          ${sample.length} events in ${(dv / 1000).toFixed(1)}s (${(dv / sample.length).toFixed(2)}ms each)`);
    }
    console.log(`TOTAL                 ${((tItems - t0) / 1000).toFixed(1)}s`);
  } finally {
    await server.close();
  }
}

main().catch((err) => {
  console.error(err.stack ?? err);
  process.exit(1);
});
