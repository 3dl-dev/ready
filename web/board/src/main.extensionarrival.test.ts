// @vitest-environment jsdom
//
// ready-48f — THE LOGIN PAGE SURVIVES A REAL EXTENSION'S ARRIVAL RACE.
//
// THE DEFECT, MEASURED, NOT HYPOTHESISED. A NIP-07 extension injects
// window.nostr ASYNCHRONOUSLY: nos2x's content script runs at document_end and
// appends a `<script src="chrome-extension://…/nostr-provider.js">` that must
// still be fetched and executed. The board's own bundle is a deferred module and
// routinely wins that race. renderLogin sampled hasNip07Extension() ONCE, at
// render, so on a load the extension merely lost, the page's only NIP-07 control
// was disabled for the life of that document — the human's sole recovery a
// reload that could lose again. Measured on a cold Chromium profile with nos2x
// 2.5.2 loaded unpacked: dead on 6 loads out of 6
// (scripts/live-stranger-walk.mjs, before the fix).
//
// WHY NO EARLIER PROOF COULD HAVE CAUGHT IT. Every automated proof in this repo
// installed window.nostr with Page.addScriptToEvaluateOnNewDocument — before any
// page script runs — so the race is unobservable by construction there. That is
// the concrete cost of the "injected signer is close enough" substitution, and
// the reason ready-48f insisted on a real extension.
//
// This is the deterministic half: the DOM control, driven through the real
// main(). The live half is scripts/live-stranger-walk.mjs, which asserts the
// button becomes clickable in ONE document with no reload, against the real
// extension.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { main, type BoardDeps } from "./main";
import { neverUnwraps } from "./lib/keyunwrap";

/** A page with no fragment and no relay it could reach: this file is about the
 * login form only, so nothing here should ever get as far as a socket. */
const deps: BoardDeps = {
  keyUnwrapper: () => neverUnwraps,
  loadRelays: async () => [],
  fetchEvents: async () => [],
};

function extensionButton(): HTMLButtonElement {
  const app = document.getElementById("app")!;
  const btn = [...app.querySelectorAll("button")].find((b) => /extension/i.test(b.textContent ?? ""));
  expect(btn, "the login page must offer a NIP-07 button").toBeDefined();
  return btn as HTMLButtonElement;
}

describe("ready-48f: the NIP-07 button when the extension arrives late", () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="app"></div>';
    delete (window as { nostr?: unknown }).nostr;
  });

  afterEach(() => {
    delete (window as { nostr?: unknown }).nostr;
  });

  it("enables the button when window.nostr appears AFTER the login form rendered", async () => {
    main(deps);

    // The state the race produces, asserted before the fix can be credited for
    // it: at render there is no provider, so the button is disabled and says so.
    const btn = extensionButton();
    expect(btn.disabled).toBe(true);
    expect(btn.title).toMatch(/No NIP-07 extension detected/);

    // The extension lands, exactly as a content-script-injected provider does.
    (window as { nostr?: unknown }).nostr = { getPublicKey: async () => "a".repeat(64) };

    // THE PROPERTY: the SAME button, in the SAME document, becomes usable — no
    // reload, no re-render, no second visit.
    const deadline = Date.now() + 4000;
    while (btn.disabled && Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 20));
    }
    expect(btn.disabled, "the button never noticed the extension").toBe(false);
    expect(btn.title).toBe("");
    // The node is the one the page rendered, not a replacement: a re-render
    // would be the reload this case exists to make unnecessary.
    expect(extensionButton()).toBe(btn);
  });

  it("leaves the button disabled, and its reason intact, when no extension ever arrives", async () => {
    main(deps);
    const btn = extensionButton();
    expect(btn.disabled).toBe(true);

    // ANTI-TAUTOLOGY for the case above: the recovery is driven by the provider
    // actually appearing, not by the passage of time. Waited longer than the
    // poll interval, well short of the 3s bound.
    await new Promise((r) => setTimeout(r, 400));
    expect(btn.disabled).toBe(true);
    expect(btn.title).toMatch(/No NIP-07 extension detected/);
  });
});
