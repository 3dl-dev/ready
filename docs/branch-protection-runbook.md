# Branch protection runbook (ready-fe2)

## Why this exists

As of ready-fe2, `main` has **no branch protection** (`gh api
repos/3dl-dev/ready/branches/main/protection` → 404) and **no rulesets**
(`gh api repos/3dl-dev/ready/rulesets` → `[]`). Every PR and every push to
main is gated by exactly one check: CodeQL default-setup static analysis,
which runs no tests. `.github/workflows/go-test.yml` (this change) adds a
job that runs `go test ./...`, but a workflow existing is not the same as
it being enforced — a PR can still merge (or a push still land) with that
job red, or skip it entirely via direct push, until branch protection
requires it.

## Hard sequencing constraint — read before running anything below

GitHub can only require a status check **by the exact name it has already
seen report** on the target ref. If you enable protection naming a check
that has never run, every PR (including any PR meant to fix the problem)
hangs forever waiting on a check that will never arrive.

**Do not run the `gh api` call in step 3 until steps 1 and 2 are both
true.**

## Steps

### 1. Merge this workflow to main

Merge the PR built from `work/ready-fe2` (containing
`.github/workflows/go-test.yml`) into `main` via a normal PR merge — do not
push directly.

### 2. Observe the check actually report, and confirm its exact name

After the merge lands (the workflow's `push: branches: [main]` trigger
fires on the merge commit), confirm it ran and capture the literal check
name GitHub recorded:

```bash
gh run list --workflow=go-test.yml --branch=main --limit=5
gh api repos/3dl-dev/ready/commits/main/check-runs --jq '.check_runs[].name'
```

Expected check name, given `.github/workflows/go-test.yml`'s `name: Go
Test` and the `test` job's `name: test` with no matrix: **`test`** as the
job name, surfaced as context `Go Test / test` in some GitHub UI/API
surfaces and as check-run name `test` in the Checks API. Use whatever
`gh api .../check-runs` actually prints back — do not assume the string
below is correct without having run this command; that is the entire point
of this sequencing constraint.

Also open a real PR (any small one, or the next one in flight) and run:

```bash
gh pr checks <pr-number>
```

to see the name exactly as it appears in the PR checks list — that is the
string required by branch protection's `contexts` field.

### 3. ONLY THEN enable branch protection

Once you have the exact, confirmed check name from step 2 (call it
`<CHECK_NAME>` below — substitute the literal string, do not guess it),
apply:

```bash
gh api -X PUT repos/3dl-dev/ready/branches/main/protection \
  --input - <<'EOF'
{
  "required_status_checks": {
    "strict": false,
    "contexts": ["<CHECK_NAME>"]
  },
  "enforce_admins": false,
  "required_pull_request_reviews": {
    "required_approving_review_count": 0
  },
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
EOF
```

Rationale for each field, per the repo owner's ruling (ready-fe2):

- `required_status_checks.contexts`: names the go-test check as required.
  `strict: false` (do not require the branch to be up to date with main
  before merge — no stance was requested on this, `false` is the
  non-disruptive default; revisit if the owner wants it `true`).
- `enforce_admins: false` — the owner keeps an admin override for
  hotfixes.
- `required_pull_request_reviews.required_approving_review_count: 0` — a
  PR is required for every change to main (see `restrictions: null` +
  the implicit requirement below), but an approving review is not.
- A pull request is required for every change (no direct pushes) because
  `required_pull_request_reviews` being present in the protection payload
  is what GitHub uses to block direct pushes to the branch, even at
  `required_approving_review_count: 0`. Verify after applying:

  ```bash
  gh api repos/3dl-dev/ready/branches/main/protection --jq '{required_status_checks, enforce_admins, required_pull_request_reviews, allow_force_pushes}'
  ```

  Confirm `required_pull_request_reviews` is non-null and
  `allow_force_pushes` is `false`. If GitHub's API silently dropped the PR
  requirement (some GitHub plans/APIs require an explicit
  `required_pull_request_reviews` object to force PR-only merges — this is
  why it's included even with zero required approvals), test it directly:
  attempt a trivial direct push to main from a scratch branch tip and
  confirm it is rejected, then discard that test push.

- `allow_force_pushes: false`, `allow_deletions: false` — standard
  hardening, not explicitly requested but consistent with "no direct
  pushes, no bypassing the check."

### 4. If any `gh api` call 403s

That is a genuine prerequisite gap (the acting identity's token lacks
`admin:repo` scope or the repo's admin permission), not a workaround
target. Stop and escalate — do not attempt to route around it (e.g. via
repo settings UI automation, a different token, etc.) without the repo
owner's explicit sign-off.

## What NOT to do

- Do not add a `paths:` filter to `go-test.yml` — see the comment in the
  workflow file itself. A required check that's path-filtered can
  permanently block PRs that don't touch the filtered paths.
- Do not enable protection before step 2's check name is confirmed live.
- Do not broaden any existing test skip (e.g. `RD_NOSTR_LIVE_RELAY`,
  `RD_NOSTR_TEST_SECRET_HEX`/`RD_NOSTR_TEST_KEY_PATH` portfolio-key gates
  in `cmd/rd/board_test.go` and related `*_live_relay_test.go` files) to
  force this job green. Those are pre-existing hermetic-by-default gates;
  CI intentionally does not set their env vars, so they self-skip exactly
  as they do for local `go test ./...` runs. That is not a gap to close —
  it's the existing, correct behavior. Broadening them would recreate the
  vacuous-gate problem this item exists to fix, just relocated.
