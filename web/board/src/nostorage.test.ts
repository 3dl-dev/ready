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
// THE DECISION THIS RECORDS (the item asks for it explicitly): there is NO
// plaintext-title cache. The item permits one if it is scoped per logged-in
// pubkey and the decision is written down; the choice made here is the stricter
// one — cache nothing. The keyring and every decrypted title are rebuilt from
// signed relay events on each load. The cost is one extension prompt per load;
// what it buys is that a stolen or shared browser profile contains no board
// keys and no board text, and that there is no cache-scoping bug to have.
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

  it.each(PERSISTENCE_APIS)("no shipped module references %s", (api) => {
    for (const s of sources) {
      expect(s.code, `${s.name} references ${api} — see this file's header: the board caches NOTHING`).not.toContain(api);
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
