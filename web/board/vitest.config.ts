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
    // scripts/**: ready-153. The live harnesses under scripts/ can never run
    // in CI (Chromium, a Go toolchain, the owner's signing key, a real relay),
    // but the cleanup contract they depend on — throwaway-board.mjs — is pure
    // logic over an injectable relay and an injectable `rd`, and the check
    // that every board-creating harness is actually bound to that contract is
    // a source-shape assertion. Both run here, on every PR. Before this, the
    // only evidence a harness cleaned up after itself was prose in a commit
    // message.
    include: ["src/**/*.test.ts", "scripts/**/*.test.mjs"],
  },
});
