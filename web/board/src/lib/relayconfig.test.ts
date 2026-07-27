import { describe, expect, it } from "vitest";
import { loadOwnBoardsRelays } from "./relayconfig";

function fakeFetch(body: unknown, ok = true, status = 200): typeof fetch {
  return (async () =>
    ({
      ok,
      status,
      json: async () => body,
    }) as Response) as typeof fetch;
}

describe("loadOwnBoardsRelays", () => {
  it("returns the configured relay list", async () => {
    const relays = await loadOwnBoardsRelays(fakeFetch({ ownBoardsRelays: ["wss://relay.3dl.network"] }));
    expect(relays).toEqual(["wss://relay.3dl.network"]);
  });

  it("throws on a non-OK response rather than silently using no relays", async () => {
    await expect(loadOwnBoardsRelays(fakeFetch({}, false, 404))).rejects.toThrow(/404/);
  });

  it("throws when ownBoardsRelays is missing or empty (never falls back to a hardcoded public list)", async () => {
    await expect(loadOwnBoardsRelays(fakeFetch({}))).rejects.toThrow(/ownBoardsRelays/);
    await expect(loadOwnBoardsRelays(fakeFetch({ ownBoardsRelays: [] }))).rejects.toThrow(/ownBoardsRelays/);
  });
});
