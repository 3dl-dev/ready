# CLI Agent Ergonomics — Design Brief

**Date:** 2026-04-08
**Status:** Approved (Baron, session 2026-04-07) — updated 2026-04-08 for v0.9.4
**Items:** ready-42c, ready-00f, ready-731, ready-519, ready-bd6, ready-d2b

## Problem

99.9% of rd invocations are by agents, not operators. The CLI defaults are human-biased:

1. **Output format**: `--json` is opt-in. Default is formatted tables. Agents must add `--json` to every call or parse columns.
2. **Warnings on stdout**: Durability, inbox membership, and provenance warnings go to stdout, contaminating machine-parseable output. An agent piping `rd ready` to `jq` gets garbage.
3. **No agent onboarding**: Getting-started guide is for humans. No `.well-known/agent.json` for A2A discovery. README leads with human workflow.

## v0.9.x Shipped

The following items shipped in the v0.9.x cycle:

- ✅ **Pipe-friendly output** (ready-e09, ready-72e): `rd create`, `rd ready`, `rd list` print bare IDs when output is not a TTY. `ITEM=$(rd create ...)` and `for id in $(rd ready)` now work without `--json`. See notes on ready-42c below.
- ✅ **Inbox warning suppressed for non-owners** (ready-3ce): inbox warning no longer shown to agents who aren't the board owner.
- ✅ **Durability warnings suppressed for local filesystem** (ready-b56): noise removed for the common dev case.
- ✅ **Demo script refresh** (ready-d2b): all 11 demos rebuilt and output files updated.
- ✅ **Invite tokens** (ready-c31): `rd admit` replaced with `rd invite` / `rd join`.
- ✅ **Walk-up identity** (ready-c4b): agents no longer need `CF_HOME` set before running rd.
- ✅ **Auto-sync** (ready-341): `rd sync pull` no longer required manually.

## Decisions (shipped this session)

- ReadyFilter no longer gates on ETA. ETA is for sort order. `scheduled` status is the gate for work that can't start yet. (commit bc1cb43)
- `rd ready` identity filter matches `for` OR `by`. Delegated work now visible to the performer. (commit bc1cb43)

## Remaining work

### ready-42c: JSON default output — Reconsidering

**Original change:** `root.go:68` — flip `jsonOutput` default to `true`. Add `--human` / `--pretty` flag that sets `jsonOutput = false`.

**Status:** Reconsidering. Pipe-friendly output (ready-e09, ready-72e) may make this unnecessary for most agent use cases. When output is not a TTY, commands print bare IDs — `ITEM=$(rd create ...)` just works, and `for id in $(rd ready)` iterates without `--json`. Structured JSON is still available via `--json` for agents that need full objects.

The JSON-default approach would make `--json` the default for *all* output, breaking scripts that parse table format. Pipe-friendly is non-breaking and covers the primary agent case (capturing IDs). JSON default may still be worth doing for agents that need structured fields without explicit `--json`, but it is no longer urgent.

**Scope (if pursued):**
- `cmd/rd/root.go` — default and flag
- Every command that checks `jsonOutput` — verify JSON path is the primary code path
- `printItemTable` and similar formatters become `--human`-only
- `rd create` should always return the created item JSON (currently silent without `--json`)

**Breaking:** Yes. Any script parsing table output breaks. Any script already using `--json` is unaffected.

**Test:** `go test ./cmd/rd/...` — existing tests that assert table output need updating to assert JSON or use `--human`.

### ready-00f: Warnings to stderr — Partial

**Change:** All `fmt.Printf("warning: ...")` and `log.Printf("warning: ...")` calls in `cmd/rd/` must write to `os.Stderr`, not `os.Stdout`.

**Progress:** Two high-noise warnings are suppressed:
- Inbox membership warning (ready-3ce): suppressed for non-owners
- Durability warning for local filesystem (ready-b56): suppressed for the common dev case

The broader migration — moving all remaining warnings to stderr rather than suppressing them — is still open. Agents piping to `jq` may still see non-fatal warnings on stdout from other code paths.

**Remaining scope:**
- `cmd/rd/root.go` — the `warn()` helper (if it exists) or all inline warning prints
- `cmd/rd/send.go` — relay send failure warnings
- Any command that prints "warning:" to stdout outside the two suppressed cases

**Pattern:** Create a `warnf(format, args...)` helper that writes to stderr. Replace all warning prints.

**Test:** Capture stderr separately from stdout in tests. Verify stdout is clean JSON.

### ready-731: docs/agent.md — In Progress (getting-started.md rewrite)

**Status:** The dedicated agent guide is being written as a rewrite of `docs/getting-started.md` rather than a new `docs/agent.md`. The getting-started guide is being restructured to lead with agent workflow. The content below remains the target spec; the file path may land as `docs/getting-started.md` or `docs/agent.md` depending on the rewrite outcome.

Primary getting-started guide for agents. Content:

```markdown
# Agent Quickstart

## Join a project
rd join <invite-token>     # self-mints your key, pins the board read-only

## Work loop
rd ready --view my-work    # your queue (JSON by default)
rd claim <id>              # accept work
rd progress <id> --notes "status update"
rd done <id> --reason "what was accomplished"

## Escalate
rd gate <id> --gate-type design --description "question for human"
# human runs: rd approve <id> --reason "direction"

## Programmatic parsing
rd ready --view my-work    # returns JSON array
rd show <id>               # returns JSON object
# exit code 0 = success, non-zero = error
# warnings go to stderr, data goes to stdout
```

### ready-519: .well-known/agent.json — Open

A2A discovery file. Format:

```json
{
  "name": "ready",
  "description": "Work management as a nostr convention",
  "protocol": "nostr",
  "docs": {
    "agent_quickstart": "https://ready.3dl.dev/docs/agent",
    "convention_schema": "https://ready.3dl.dev/docs/convention"
  },
  "operations": [
    "work:create", "work:claim", "work:close", "work:status",
    "work:delegate", "work:gate", "work:gate-resolve",
    "work:block", "work:unblock"
  ]
}
```

### ready-bd6: README restructure — In Progress

**Status:** Happening now alongside the getting-started.md rewrite.

Lead with agent workflow. Move operator setup to "Setting up a project" section below. Link to the agent guide (docs/getting-started.md or docs/agent.md) as the primary entry point.

### ready-d2b: Demo script refresh — ✅ Done

After the nostr cutover the demo suite is **5 nostr-native workflow scripts** — `01-solo`, `02-team`, `03-multiproject`, `05-agent-workflow`, `06-gate-escalation` — each regenerating a fresh transcript in `test/demo/output/*.txt`. The **6 org / hosted-topology scripts** (`04-org-observer`, `07-hosted-multi-machine`, `08-org-naming`, `09-cattle-workstation`, `10-bridge-hybrid`, `11-filesystem-isolation`) had no nostr-native equivalent and were removed in the cutover. The remaining transcripts reflect pipe-friendly output (bare IDs when piped) and suppressed warnings.
