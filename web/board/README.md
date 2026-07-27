# web/board

Static placeholder page for `ready.3dl.dev/board` (ready-2f1). This is
**not** the board UI — it exists to prove the TypeScript build pipeline
reaches production. The eventual board UI will read `docs/design/board-fold-spec.md`.

## Toolchain

- **Bundler**: Vite. Chosen over esbuild-direct because Vite's default
  config already produces hashed, content-addressed asset filenames and a
  self-contained `dist/` with zero runtime CDN references, which is exactly
  what this item needs, without hand-rolling that in a raw esbuild script.
- **Language**: TypeScript, checked with `tsc -b` before bundling (`npm run
  build` runs `tsc -b && vite build` — a type error fails the build).
- **Node version**: pinned in `web/.nvmrc` (20.17.0). CI reads it via
  `actions/setup-node`'s `node-version-file`.
- **Lockfile**: `package-lock.json` is committed. CI runs `npm ci`, never
  `npm install`, so the build is reproducible from a clean clone.

## Build

```sh
cd web/board
npm ci
npm run build      # outputs to web/board/dist/
```

`dist/` is git-ignored — CI builds it fresh on every deploy and copies it
into the Pages artifact at `/board/` (see `.github/workflows/pages.yml`).
The root `ready.3dl.dev` site (`site/`) is copied in unmodified alongside
it, so this does not touch the existing root site.

The page embeds a build-stamp (the deploying commit SHA, via the
`VITE_BUILD_STAMP` env var set in CI) rendered as `<code id="build-stamp">`
in the page, e.g. `build:abcdef0…` — useful for confirming a specific
commit actually deployed.

## Known accepted risk

`npm audit` reports a moderate esbuild advisory (GHSA-67mh-4wv8-2f99) that
only affects Vite's **dev server** accepting arbitrary requests; it does
not affect the production `vite build` output used here, and this
toolchain never runs `vite dev` in CI or production. Revisit when Vite
ships a release with a patched esbuild in the 5.x line, or on the next
routine dependency bump.
