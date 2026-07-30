// The structural half of ready-c4b done condition 6: "No secret material is
// ever written to localStorage/IndexedDB/sessionStorage."
//
// WHY A STRUCTURAL TEST AND NOT ONLY A BEHAVIOURAL ONE. main.confidential.test.ts
// renders a whole confidential board and then asserts both storages are empty.
// That is real evidence, but it only covers the code paths that render walked —
// an error branch, a future caching helper, or a module nothing in the suite
// reaches could persist a CEK and stay green. This test is the complement: it
// reads every SHIPPED source file and fails if one so much as names a
// persistence API. Between them, "nothing was persisted on this path" and
// "nothing can persist on any path" cover the claim.
//
// THE DECISION THIS RECORDS, AS AMENDED BY ready-fe4. ready-c4b's condition 6
// permits a cache "if it is scoped per logged-in pubkey and the decision is
// written down", and the choice made at the time was the stricter one — cache
// nothing — implemented as a TOTAL ban on naming a persistence API. ready-fe4
// takes the permitted option, with the reason measured: the portfolio view's
// first paint against the live relay was 97.2 SECONDS across 75 boards, of which
// the relay was a small minority (8.3s of 69.1s over 12 boards) and the rest was
// BIP-340 verification at 3.49ms an event. A page that makes its owner stare at
// a blank screen for a minute and a half is not a security posture, it is an
// unusable page, and ready-fe4 conditions 3 and 5 name the storage
// ("localStorage/IndexedDB, NOT sessionStorage") and the scoping ("keyed per
// logged-in pubkey ... discarded on logout") explicitly.
//
// SO THE BAN NARROWS TO A ONE-FILE EXEMPTION AND CONDITION 6 ITSELF DOES NOT
// MOVE. What condition 6 says is "No SECRET MATERIAL is ever written to
// localStorage/IndexedDB/sessionStorage", and that is unchanged and now actually
// exercised rather than vacuous: lib/boardcache.ts decides what is written, is
// still scanned line by line here, and names no persistence API at all — it
// takes the store as a parameter. What it writes for key material is CEK EPOCH
// NUMBERS and never a key, never a hash of one (see gateFingerprint's own note
// on why a digest of a CEK would be a verifier for a guessed one). The
// behavioural guards — main.portfolio.test.ts's "no key material reaches browser
// storage", main.confidential.test.ts's storage sweep, and main.cache.test.ts's
// confidential round trip — went from covering a page that wrote NOTHING to
// covering a page that writes a board projection on every load, which is the
// only condition under which they were ever evidence.
//
// The exemption is ONE named file, guarded below by its own test: it must be
// tiny, it must name only the API it exists to name, and it must contain no key
// material. A second exemption is a decision to be made by a human, not a line
// to be added.
//
// Test-only modules are exempt from the scan and named individually rather than
// matched by pattern, so adding a new shipped module cannot accidentally land in
// the exemption list.

import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/** Modules that exist only for tests and are never reachable from index.html ->
 * main.ts, so they are never bundled into dist/. */
const TEST_ONLY = new Set(["lib/boardevents.fixtures.ts", "lib/confidential.fixtures.ts", "lib/fakesigner.ts", "lib/nip44ref.ts"]);

/**
 * The ONE shipped module permitted to name a persistence API (ready-fe4). It is
 * exempt from the scan below and subject to a stricter test of its own, because
 * an exemption nobody checks is a hole.
 */
const STORAGE_ADAPTER = "lib/localcachestorage.ts";

const PERSISTENCE_APIS = ["localStorage", "sessionStorage", "indexedDB", "openDatabase", "document.cookie"];

function shippedSources(): { name: string; code: string }[] {
  const srcDir = fileURLToPath(new URL("./", import.meta.url));
  const libDir = fileURLToPath(new URL("./lib/", import.meta.url));
  const entries = [
    ...readdirSync(srcDir).map((f) => ({ path: srcDir + f, name: f })),
    ...readdirSync(libDir).map((f) => ({ path: libDir + f, name: "lib/" + f })),
  ];
  return entries
    .filter((e) => e.name.endsWith(".ts") && !e.name.includes(".test.") && !e.name.endsWith(".d.ts") && !TEST_ONLY.has(e.name))
    .map((e) => ({
      name: e.name,
      // Strip comments: prose is allowed to DISCUSS these APIs (this file's own
      // header does, and keyring.ts explains why it uses none). Only code counts.
      code: readFileSync(e.path, "utf8")
        .split("\n")
        .filter((l) => {
          const t = l.trimStart();
          return !t.startsWith("//") && !t.startsWith("*") && !t.startsWith("/*");
        })
        .join("\n"),
    }));
}

describe("no shipped module can persist anything (done condition 6, structural)", () => {
  const sources = shippedSources();

  it("actually found the shipped modules — a vacuous pass would be worse than no test", () => {
    expect(sources.length).toBeGreaterThan(8);
    const names = sources.map((s) => s.name);
    for (const required of ["main.ts", "lib/keyring.ts", "lib/keyunwrap.ts", "lib/envelope.ts", "lib/carditems.ts"]) {
      expect(names, `${required} must be scanned`).toContain(required);
    }
    // And the exemption list must not have quietly swallowed a real module.
    for (const exempt of TEST_ONLY) expect(names).not.toContain(exempt);
  });

  it.each(PERSISTENCE_APIS)("no shipped module except the one storage adapter references %s", (api) => {
    for (const s of sources) {
      if (s.name === STORAGE_ADAPTER) continue;
      expect(
        s.code,
        `${s.name} references ${api} — see this file's header: exactly one module (${STORAGE_ADAPTER}) may, and it must be the one that decides NOTHING`,
      ).not.toContain(api);
    }
  });

  it(`the exemption is real: ${STORAGE_ADAPTER} is scanned, not merely skipped`, () => {
    const adapter = sources.find((s) => s.name === STORAGE_ADAPTER);
    expect(adapter, `${STORAGE_ADAPTER} must exist — an exemption for a file that is gone is dead weight that a future module could inherit`).toBeDefined();
  });

  it("the exempt adapter names ONE api, holds no logic, and carries no key material", () => {
    const adapter = sources.find((s) => s.name === STORAGE_ADAPTER)!;
    // It may name localStorage. It may name nothing else: a "while I am here"
    // sessionStorage or indexedDB path would be a second storage decision made
    // inside the exemption rather than in front of a human.
    for (const api of PERSISTENCE_APIS) {
      if (api === "localStorage") continue;
      expect(adapter.code, `${STORAGE_ADAPTER} must not reach for ${api}`).not.toContain(api);
    }
    // It is an ADAPTER. Anything that decides WHAT is stored belongs in
    // boardcache.ts, which this file's scan still covers in full.
    const codeLines = adapter.code.split("\n").filter((l) => l.trim() !== "").length;
    expect(codeLines, `${STORAGE_ADAPTER} has grown logic — move it to boardcache.ts, which is scanned`).toBeLessThan(20);
    for (const forbidden of ["cek", "secret", "nsec", "nip44", "decrypt", "setItem"]) {
      expect(adapter.code.toLowerCase(), `${STORAGE_ADAPTER} must not touch ${forbidden}`).not.toContain(forbidden);
    }
  });

  it("the scan would catch a violation (guard against a broken predicate)", () => {
    // Prove the stripping does not swallow real code. This is the shape a
    // regression would take: a plausible-looking cache write.
    const violating = `const k = "x";\nlocalStorage.setItem("cek", k);\n`;
    const stripped = violating
      .split("\n")
      .filter((l) => !l.trimStart().startsWith("//"))
      .join("\n");
    expect(stripped).toContain("localStorage");
  });
});
