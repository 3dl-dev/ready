# Building and releasing

Everything here is a **procedure you re-run**, not a snapshot you trust. No counts, no
DAG, no "current state" — those go stale and this file must not. Where a claim is
mechanically checkable against the repo, `cmd/rd/docs_release_test.go` checks it, so
this document going out of date is a red test rather than a surprise at 2am.

Two artifacts ship, on two independent triggers:

| Artifact | Trigger | Workflow |
|---|---|---|
| The browser board at `ready.3dl.dev/board/` | push to `main` touching `site/**` or `web/**` | `.github/workflows/pages.yml` |
| `rd` binaries, per OS/arch | pushing a `v*` tag | `.github/workflows/release.yml` |

You do not deploy the board by hand. Merging to `main` is the deploy.

## Build it locally

```
cd web/board && npm ci && npm run build
```

`npm ci` — not `npm install` — is what CI runs, so it is what you run. The bundle
lands in `web/board/dist/`.

## Test it before you merge

```
go build ./... && go vet ./... && go test ./...      # from the repo root
cd web/board && npx vitest run                        # the browser suite
```

Branch protection on `main` requires the `test` check. Never merge red, and never
merge by bypassing the check — if it is failing, that is the work.

## The live proof, and what it is for

The browser write path — a gate approved in the browser, resumed by an agent, landing
in `rd` on another machine — cannot be proven by a hermetic suite. It is proven by:

```
cd web/board
node scripts/live-write-roundtrip.mjs        # the seven write operations + the full gate loop
node scripts/live-roundtrip-both-ways.mjs    # convergence in both directions
```

These run against the real relay in a real Chromium and take about ten minutes each.
Board CI deliberately does **not** run them: a live-network test inside a hermetic
suite is a flakiness hazard. That is a considered policy, not an oversight.

Each passing run writes a receipt to `web/board/receipts/`, which is committed.
`web/board/receipts_test.go` then checks that receipt offline — that it records a
passing run, and that its check count still matches what the script actually asserts.
**A receipt is the only durable evidence these scripts ever passed**; before receipts
existed, the record was prose in commit messages, and a live-only regression sat
undetected behind two closed items. If you change a live script's assertion set,
re-run it and commit the new receipt — the drift guard will tell you if you forget.

Each run also leaves a throwaway board on the public relay. Do not run them
speculatively.

## Cut a release

```
git tag vX.Y.Z && git push origin vX.Y.Z
```

That is the whole trigger. `release.yml` cross-compiles `rd`, stamps the version into
the binary, and publishes a GitHub Release with the archives and `checksums.txt`.
Completion of that workflow also kicks `pages.yml` so the site picks up the new
version stamp.

**Do not "fix" the release → pages hand-off to use `release: types: [published]`.**
It looks like the obvious wiring and it never fires: the release is authored by the
default `GITHUB_TOKEN`, and GitHub does not start new workflow runs from
`GITHUB_TOKEN`-authored events. That is a platform rule, not a config knob. The
`workflow_run` trigger in `pages.yml` exists because of it, and the reasoning is
recorded there.

## Hazards a cold start actually hits

Each of these has bitten a real session. They are conditions to check, not beliefs to
carry.

- **A fresh worktree has no `web/board/node_modules`.** The browser suite cannot run
  until you `npm ci` in `web/board`. A swarm worktree is a fresh checkout.
- **The `rd` on `PATH` may not be this tree's.** It has been a symlink into another
  repository. If a step depends on `rd` behaviour, build it from this tree
  (`go build -o <tmp>/rd ./cmd/rd`) and use that.
- **Disk pressure produces failures that look like code bugs.** A full disk makes
  `go build -o` fail silently enough that the next step reports a missing binary.
  Check free space before believing a phantom `ENOENT`.
- **Relay measurement:** never count with a nostr `authors` filter — it under-returns.
  Use a kind-only or `#a` filter and page with `until`. When calling `nak` inside a
  loop, redirect stdin from `/dev/null` or it consumes the loop's input and returns
  nothing.
