import { defineConfig } from "vite";

// ready.3dl.dev/board is served as a sub-path of the existing GitHub Pages
// site (ready.3dl.dev root = site/). base must match that mount point so
// built asset URLs resolve correctly, and build output has no runtime CDN
// fetches — everything referenced here is bundled into dist/.
export default defineConfig({
  base: "/board/",
  esbuild: {
    // Bundled dependencies ship license banners (`// @license MIT <url>`,
    // `/*! ... @preserve */`) that esbuild is obliged to keep. "eof" moves
    // every preserved comment to the end of the emitted chunk, each on its
    // own line, instead of leaving it inline in the code. dist_test.go's
    // external-reference scan depends on that placement: it exempts only a
    // trailing run of comment-only lines, so a banner's URLs are tolerated
    // while a URL anywhere in the code region is not. Remove this and
    // banners land inline, where the scan rejects them —
    // TestDist_ExternalRefScanToleratesBannersAndRelayURLs is the check.
    legalComments: "eof",
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
