// @license MIT — https://example.com/l
//
// Stands in for a bundled third-party dependency. Two things about it matter,
// and both are asserted by TestDist_ExternalRefScanToleratesBannersAndRelayURLs:
//
//  1. The banner above is a legal comment (esbuild preserves any comment
//     containing @license or @preserve), so it survives minification into the
//     shipped chunk — carrying an absolute https URL with it, as real banners
//     almost always do.
//  2. wss:// relay endpoints are ordinary string literals in TypeScript
//     source. They used to be impossible to write here, which is why the
//     board's relay list was moved to a runtime-fetched JSON file.
//
// Nothing in this file is imported by the real board app; it exists only as
// the root of the fixture build.

export const RELAYS: string[] = ["wss://relay.3dl.network", "wss://relay.damus.io"];
