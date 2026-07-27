import { defineConfig } from "vite";

// ready.3dl.dev/board is served as a sub-path of the existing GitHub Pages
// site (ready.3dl.dev root = site/). base must match that mount point so
// built asset URLs resolve correctly, and build output has no runtime CDN
// fetches — everything referenced here is bundled into dist/.
export default defineConfig({
  base: "/board/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
