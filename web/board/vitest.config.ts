import { defineConfig } from "vitest/config";

// Default environment is "node" (fast, and correct for the pure-logic
// modules: sha256, secp256k1, bech32, npub, nostrevent, boarddiscovery,
// relay, relayconfig — none of them touch the DOM). Files that need
// window/document/history (fragment.test.ts, nip07.test.ts) opt into jsdom
// per-file via a `// @vitest-environment jsdom` docblock, so the DOM
// dependency stays scoped to the tests that actually need it.
export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
