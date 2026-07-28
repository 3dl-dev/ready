// Go-signed kind-30301 board-event fixtures, shared by boarddiscovery.test.ts
// (library layer) and main.test.ts (integration layer) so both suites exercise
// the SAME bytes rather than two independently transcribed copies.
//
// Every event below was produced by the REAL Go signer (pkg/nostr.Event.Sign)
// — see pkg/sync/boarddiscovery_test.go and the ready-dbf item's
// ground_truth_evidence for the generator. That provenance is what makes "a
// hostile relay serves a tampered event" a genuine test of schnorr
// verification rather than a tautology: alpha/beta/gamma/alphaDup/delta really
// do verify, and forgedSig/impersonator really do not, under the same BIP-340
// code path the Go CLI uses.
//
// This module is imported only by *.test.ts files. Nothing reachable from
// index.html -> main.ts imports it, so it is never bundled into dist/.
import type { NostrEvent } from "./nostrevent";

export const OWNER = "74c6846624970ce5e45f5b90abf126ba6e20e3e2a8e1c0f28519368f9a84e7b3";
export const OTHER = "e968fa6ad6581f7d7e9db8cf6de8c33f3f4fe6f915c645f3c30f68a2aee2fd20";

export const alpha: NostrEvent = {
  id: "5c27bad3029071072e7245c541d0295c55c4f58da7aad2fe8c2b25d3ec8e6565",
  pubkey: OWNER,
  created_at: 1700000001,
  kind: 30301,
  tags: [["d", "alpha"], ["title", "Alpha Board"]],
  content: "",
  sig: "048fbf69de632ed00b113c25affea7a542206ad9d1bc25c11e2731f10ef2a5394212eb2b2c43152a098229a0ed13e2458b52d0542a06ffd1b968492164899c83",
};
export const beta: NostrEvent = {
  id: "e0f1867fd97a2d71d8c6bacfda978e24bd35bffa63b8035a9fba9e98a972a740",
  pubkey: OWNER,
  created_at: 1700000002,
  kind: 30301,
  tags: [["d", "beta"], ["title", "Beta Board"]],
  content: "",
  sig: "77c9f8caf72624c9568191693c8b27134e8e9d5062db68d33526623c83a2b4e05c407b1973310f1a6acc2b08689524a1d716b125cec977b222f142d95db99076",
};
export const gamma: NostrEvent = {
  id: "3f8f7fe315e38cc2c8bd6f57a9c71c1e0eceade7ea30b00fc18be48dac46057b",
  pubkey: OWNER,
  created_at: 1700000003,
  kind: 30301,
  tags: [["d", "gamma"], ["title", "Gamma Board"]],
  content: "",
  sig: "4714e2636aa6d975edbb21ea4b557ed506a0fae3d64de807bc3f3c6c7c13af2a8b835b7db00bb48ab069e146b61106a9668f9967a2145baf7d5aea57e3964a22",
};
export const alphaDup: NostrEvent = {
  id: "5ccd6ecd228ec2a27b41a083e9cf53e3b25f1dfe730c2ae956f4506483f0f592",
  pubkey: OWNER,
  created_at: 1700000004,
  kind: 30301,
  tags: [["d", "alpha"], ["title", "Alpha Board Dup"]],
  content: "",
  sig: "b4e9fa02fdd855225ae21c34db48ec2cb0be1b80be10517318816172f5052599e43480dd4c8a22be60240e9b0911c0353eb959b73ac4b26797f8a7f3df60593b",
};
export const delta: NostrEvent = {
  id: "6775a39e2442860a68bae503e0ef413bed9d43f24a755a47f3841611595c6914",
  pubkey: OTHER,
  created_at: 1700000005,
  kind: 30301,
  tags: [["d", "delta"], ["title", "Delta Board (foreign owner)"]],
  content: "",
  sig: "fc401ec3ce0cd520453eb7eaa565d32abd154dab78f8fbeec9a00cb1c721411d5edf5774e1fd4b7223fc9cfe884e20116e46f853a92ad949d3e39c4fe2e4a612",
};
// forgedSig: a genuine board event whose signature was then corrupted, same
// pattern as boarddiscovery_test.go's TestDiscoverOwnerBoards_DropsForgedBoardEvent
// ("00" + sig[2:]) — the exact security property done condition 4 requires.
export const forgedSig: NostrEvent = {
  id: "e89f8a93959720e845d007e3c1780815aac2bab686872964cdda9f4cecbc51ff",
  pubkey: OWNER,
  created_at: 1700000006,
  kind: 30301,
  tags: [["d", "evil"], ["title", "Evil Board"]],
  content: "",
  sig: "0022dcf8ddb9b37d11e6b7cfb96462742228662b18c9afb247d3d0f94644444e4ebf27fb3a10715b1b1baa487c32209c67bfdd293754f0f8a1028baf8d2c1e66",
};
// impersonator: genuinely signed by OTHER, then its pubkey FIELD was
// overwritten to OWNER without re-signing — a hostile relay's attempt to
// relabel someone else's event as the follow target's. The stored id (from
// signing time) no longer matches the recomputed id under the new pubkey
// field, so this must be rejected too.
export const impersonator: NostrEvent = {
  id: "8d2f7f7b34c9586a00735e5cdc7b7086519c9e713556f072327e8a2c8c000b19",
  pubkey: OWNER, // overwritten; originally signed as OTHER
  created_at: 1700000007,
  kind: 30301,
  tags: [["d", "impersonated"], ["title", "Impersonated Board"]],
  content: "",
  sig: "768b4679199403b5d0bd0c9df8a7d1ae5eaa4be0e6c5d0eca03d6499e1ebdfce66b5f09483bc5976d5b9e240dc61aa4d696619014d31cca4343e69bc81009860",
};
