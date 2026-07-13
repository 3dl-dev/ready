# Getting Started with Ready

Ready is work management as a nostr convention. Items, dependencies, gates, and views are all convention-conforming operations projected onto signed nostr events. No server backend — a local append-only signed-event log (`.ready/nostr-log.jsonl`) is the source of truth, synced over nostr relays as a replaceable cache.

## Table of Contents

- [Concepts](#concepts)
- [Prerequisites](#prerequisites)
- [Part 1: Solo (5 minutes)](#part-1-solo-5-minutes)
- [Part 2: Team (invite tokens)](#part-2-team-invite-tokens)
- [Part 3: Multi-agent (per-worktree `$RD_HOME`)](#part-3-multi-agent-per-worktree-rdhome)
- [Part 4: Dependencies](#part-4-dependencies)
- [Part 5: Playbooks (reusable work trees)](#part-5-playbooks-reusable-work-trees)
- [Part 6: Gate escalation](#part-6-gate-escalation)
- [Part 7: Resuming work (for agents)](#part-7-resuming-work-for-agents)
- [Part 8: Reference](#part-8-reference)

---

## Concepts

**Project log** — a local append-only signed-event log (`.ready/nostr-log.jsonl`), one per project, synced over nostr relays. `rd init` creates it and establishes (or reuses) the local secp256k1 signing identity. The local log is authoritative; relays are a replaceable cache — a project works standalone with no reachable relay.

**Item** — a convention-conforming card, materialized from a `kind-30302` event plus its status history (one `NIP-34` status event per transition). Fields: `id`, `title`, `type`, `priority`, `status`, `for`, `by`, `eta`, `due`. All state transitions are signed events — the log is the audit trail.

**Identity** — a secp256k1 key under `$RD_HOME` (default `~/.config/rd`). There is no separate identity file or backend to configure; the key is resolved once per process from `$RD_HOME` (or `--rd-home` / `$RD_HOME`).

**Authorization** — an owner-signed `kind-39301` role-grant. The board owner grants a pubkey a role; the relay write-allowlist and the client trust set are both derived from the same signed grant log.

**Views** — named filter predicates. `rd ready` runs the `ready` view: items that are not done, not blocked, and need attention within 4 hours. `rd list` runs `my-work`. Every read pulls the latest state from the local log — no manual sync required.

---

## Prerequisites

Install `rd`:

```bash
curl -fsSL https://ready.3dl.dev/install.sh | sh
```

Verify it's on your PATH:

```bash
rd --version
```

---

## Part 1: Solo (5 minutes)

One person, one project.

### Initialize

```bash
cd ~/projects/myproject
rd init --name myproject
```

No separate identity ceremony — a secp256k1 signing key is created (or reused,
if `$RD_HOME` already has one) automatically. Output:

```
initialized myproject (nostr-native)
  board: 30301:<owner-pubkey>:myproject
  owner: <owner-pubkey>
  log:   .ready/nostr-log.jsonl

  work items are signed events in .ready/nostr-log.jsonl (the source of truth);
  relays are a replaceable cache. create your first item with:
    rd create "..." --type task --priority p1
```

### Daily workflow

```bash
# Create an item — capture the ID for scripting
ITEM=$(rd create "Ship login page" --priority p1 --type task)

# See what needs attention now
rd ready
# rdtestsoloproja-e1f

# Claim it — transitions to active
rd claim $ITEM

# Post progress as work proceeds
rd progress $ITEM --notes "Wired up auth middleware"

# Close when done (reason is required)
rd done $ITEM --reason "Login page ships with JWT auth"
```

When stdout is piped, `rd create` emits only the bare item ID — no decoration. This makes shell assignment reliable without parsing.

### Show item detail

```bash
rd show <id>
```

Output:

```
ID:       rdtestgateprojyar-dd6
Title:    Migrate auth layer to new token format
Status:   active
Type:     task
Priority: p1
ETA:      2026-04-09T06:19:08Z (3h)

History:
  [2026-04-09T02:19:08Z] inbox → inbox — created
  [2026-04-09T02:19:08Z] inbox → active
```

### Other item operations

```bash
rd list                              # all items in this project
rd list --all                        # include done/cancelled
rd list --status active              # filter by status
rd update <id> --priority p0         # change priority
rd update <id> --note "blocked on review from alice"
rd cancel <id> --reason "..."        # cancel with reason
```

---

## Part 2: Team (invite tokens — the self-mint claim model)

Teammates join via single-use `rd1_` **claim** tokens. The token carries **no
secret key** — only the board coordinate, the relay set, a TTL, and a one-use
claim-nonce. The joiner **self-mints** its own key locally and joins **read-only**;
the owner then grants it write access, binding the claim-nonce to the joiner's
pubkey. The lifecycle is four steps:

1. **Owner mints a claim** — `rd invite` → a `rd1_` token (no key, no grant).
2. **Recipient joins read-only** — `rd join <token>` self-mints a key, pins the
   board, adopts the relays, and syncs. `rd ready` works immediately. The joiner
   **writes nothing to the relays** yet. It prints its `pubkey` and the `claim`.
3. **Recipient sends the owner `pubkey` + `claim`**.
4. **Owner grants + admits** — `rd grant <pubkey> contributor --claim <claim>`
   (then `rd relay sync-allowlist --apply` on locked relays).

**Security model.** The token is a **TTL-bounded claim, not a bearer secret**: a
leaked token yields only the right to self-mint and *request* a grant the owner may
deny — there is no importable key and no live grant in it. **Single-use is real and
owner-enforced**: one claim-nonce binds to exactly one pubkey (enforced at grant
derivation), so a leaked claim admitted to a second self-minted key is refused.

### Owner: mint a claim token

```bash
cd ~/projects/myproject
rd invite
# rd1_...  (claim token — NO private key; TTL-bounded; share it with the joiner)

rd invite --ttl 30m   # shorter window (default 2h)
```

### Teammate: join read-only, then send pubkey + claim

```bash
# Self-mints a key, pins the board, adopts relays, syncs READ-ONLY.
rd join rd1_...
# Joined board <owner>... READ-ONLY (invite expires in ...).
#   run 'rd ready' to see the project's items now.
#
# To get WRITE access, send the owner this:
#   pubkey=<hex>
#   claim=<nonce>

rd ready   # already synced — items are visible read-only
```

### Owner: grant write access (consumes the claim, single-use)

```bash
rd grant <joiner-pubkey> contributor --claim <claim-nonce>
# On locked relays, push the updated allowlist:
rd relay sync-allowlist --apply
```

### Delegate work to a teammate

```bash
# Owner creates and delegates an item
rd create "Build API" --type task --priority p1
rd delegate <item-id> --to <member-pubkey>
# delegated <item-id> to <member-pubkey>

# Teammate claims it
rd update <item-id> --status active

# Teammate closes it
rd done <item-id> --reason "API complete"
```

---

## Part 3: Multi-agent (per-worktree `$RD_HOME`)

Multiple agents on the same project each get their own identity. Identity is
resolved once per process from `$RD_HOME` (default `~/.config/rd`), so
pointing an agent's `$RD_HOME` at a worktree-local directory gives it an
isolated identity — no walk-up, no per-directory identity file to manage.

### Filesystem layout

```
myproject/
  .ready/nostr-log.jsonl      ← the project's authoritative log (committed to git)
  worktree-a/
    .rd/keys/owner.json       ← agent A identity ($RD_HOME=worktree-a/.rd)
  worktree-b/
    .rd/keys/owner.json       ← agent B identity ($RD_HOME=worktree-b/.rd)
```

### Setup

```bash
# Owner initializes the project (signing key created under $RD_HOME on first use)
cd ~/projects/myproject
rd init --name myproject
git add .ready/ .gitignore
git commit -m "chore: add work project"

# Create worktrees for agents
git worktree add worktree-a
git worktree add worktree-b

# Each agent joins with its own $RD_HOME — join self-mints a fresh key into that
# $RD_HOME (read-only), so no separate bootstrap step is needed. The owner then
# grants each agent's printed pubkey: rd grant <pubkey> contributor --claim <claim>.
cd ~/projects/myproject && RD_HOME=worktree-a/.rd rd join rd1_<token-for-agent-a>
cd ~/projects/myproject && RD_HOME=worktree-b/.rd rd join rd1_<token-for-agent-b>
```

### Each agent works independently

```bash
# Agent A
cd ~/projects/myproject/worktree-a
RD_HOME=../worktree-a/.rd rd ready     # sees items assigned to agent A

# Agent B
cd ~/projects/myproject/worktree-b
RD_HOME=../worktree-b/.rd rd ready     # sees items assigned to agent B
```

`$RD_HOME` resolution order: `--rd-home` flag → `$RD_HOME` env → default
`~/.config/rd` (XDG). Set `$RD_HOME` once per agent's environment (e.g. in the
worktree's shell profile or launch script) and every `rd` invocation in that
worktree uses the right identity automatically.

---

## Part 4: Dependencies

### Within a project

```bash
cd ~/projects/myproject

rd create "Build backend API" --priority p1 --type task
# → myproject-001

rd create "Wire frontend to API" --priority p1 --type task
# → myproject-002

# frontend work blocks on backend
rd dep add myproject-002 myproject-001

# View the dep graph
rd dep tree myproject-002
# myproject-002  [inbox]  Wire frontend to API

# rd ready hides myproject-002 until myproject-001 is closed
rd ready
# myproject-001  p1  inbox  3h  Build backend API

rd done myproject-001 --reason "API endpoint deployed"
# closed myproject-001 (done)

rd ready
# myproject-002  p1  inbox  3h  Wire frontend to API
# ↑ unblocked
```

### Cross-project

Cross-project deps work when the signer has visibility into both projects'
boards (e.g. the same identity, or a granted role on each). `rd dep add`
resolves the blocker across projects automatically:

```bash
cd FRONTEND && rd dep add frontend-a91 backend-322
# blocked: frontend-a91 is now blocked by backend-322 [cross]

cd FRONTEND && rd ready
# (frontend item blocked — not shown)

cd BACKEND && rd done backend-322 --reason "API endpoint /api/v1/users deployed"
# closed backend-322 (done)

cd FRONTEND && rd ready
# frontend-a91
# ↑ frontend item is now unblocked
```

---

## Part 5: Playbooks (reusable work trees)

A **playbook** is a template — a reusable pattern of work items with dependencies and variable substitution. `rd engage` stamps a playbook into concrete items, wires the deps, and records the engagement as an audit entry.

Reach for a playbook whenever you find yourself typing the same decomposition twice: incident runbook, feature rollout, release prep, migration, onboarding flow.

### Register a playbook

Create a JSON file describing the item tree:

```json
[
  {
    "title": "Triage {{env}} incident",
    "type": "task",
    "priority": "p0",
    "context": "Identify blast radius in {{env}}. Page on-call if >10% users affected.",
    "deps": []
  },
  {
    "title": "Root cause for {{env}} incident",
    "type": "task",
    "priority": "p0",
    "context": "Find the commit or config change. Link it in progress notes.",
    "deps": [0]
  },
  {
    "title": "Remediate {{env}}",
    "type": "task",
    "priority": "p0",
    "context": "Roll back or forward-fix. Verify metrics recover.",
    "deps": [1]
  },
  {
    "title": "Post-incident review for {{env}}",
    "type": "review",
    "priority": "p1",
    "context": "Write up timeline, contributing factors, action items.",
    "deps": [2]
  }
]
```

Per-item fields: `title`, `type`, `priority` (required); `level`, `context`, `deps` (optional). `deps` are 0-based indices into the items array. `{{variable}}` placeholders can appear in `title` and `context` and are substituted at engage time.

Register it:

```bash
rd playbook create "SRE Incident Response" \
  --id sre-incident \
  --description "Standard incident runbook" \
  --items-file sre-incident.json
# playbook sre-incident registered (4 items, msg: ...)
```

### List and inspect

```bash
rd playbook list
#   sre-incident   4 items   Standard incident runbook

rd playbook show sre-incident
# ID:          sre-incident
# Title:       SRE Incident Response
# Description: Standard incident runbook
# Items:       4
#
# Item tree:
#   [0] p0  task    Triage {{env}} incident
#   [1] p0  task    Root cause for {{env}} incident   (after: [0])
#   [2] p0  task    Remediate {{env}}                 (after: [1])
#   [3] p1  review  Post-incident review for {{env}}  (after: [2])
```

### Engage — instantiate into work items

```bash
rd engage sre-incident \
  --project myapp \
  --for oncall@myteam.dev \
  --var env=prod
# engaged playbook sre-incident → 4 items
#
#   myapp-a2f   p0  Triage prod incident
#   myapp-4x1   p0  Root cause for prod incident      (blocked by: myapp-a2f)
#   myapp-7b3   p0  Remediate prod                    (blocked by: myapp-4x1)
#   myapp-9c0   p1  Post-incident review for prod     (blocked by: myapp-7b3)
```

What engage does:

1. Finds the playbook by ID.
2. Generates item IDs (`<project>-<random-3-chars>` per template item).
3. Substitutes `{{variable}}` placeholders (unknown vars are left as-is).
4. Sends `work:create` for each item.
5. Sends `work:block` for each dependency edge.
6. Records a `work:engage` message linking every created ID — audit trail from engagement back to items.

### When agents should reach for playbooks

**Before decomposing work by hand**, run `rd playbook list`. If a registered playbook fits the shape of the task, `rd engage` it and edit the resulting items as needed. Faster than creating from scratch, and it preserves accumulated team knowledge about which steps matter.

**After producing a clean item tree for non-trivial work**, consider registering it as a playbook so the next engagement reuses the decomposition instead of re-deriving it.

Playbooks and the `work:engage` message are fully specified in `docs/convention/work-management.md` §4.12–4.13.

---

## Part 6: Gate escalation

Agents use `rd gate` when they hit a decision point that requires human judgment. The item transitions to `waiting`. The human runs `rd gates`, then `rd approve` or `rd reject`. Approval transitions the item back to `active`.

### Agent gates an item

```bash
rd gate <item-id> \
  --gate-type design \
  --description "Two approaches: option A saves 2ms but breaks caching, option B is safe. Need direction."
```

Gate types: `budget`, `design`, `scope`, `review`, `human`, `stall`, `periodic`.

### Human reviews and approves

```bash
# See all pending gates
rd gates

# Output:
#   rdtestgateprojyar-dd6  p1  Two viable approaches...  Migrate auth layer

# Approve — item returns to active
rd approve <item-id> --reason "Use option B. Safety over 2ms gain."

# Or reject — item stays in waiting for further discussion
rd reject <item-id> --reason "Split into smaller items first."
```

Example:

```
$ rd gate rdtestgateprojyar-dd6 --gate-type design --description '...'
{"gate_type":"design","id":"rdtestgateprojyar-dd6","msg_id":"396874de-..."}

$ rd gates
  rdtestgateprojyar-dd6  p1  Two viable approaches...  Migrate auth layer to new token format

$ rd approve rdtestgateprojyar-dd6 --reason 'Use option B. Safety over 2ms gain.'
{"id":"rdtestgateprojyar-dd6","resolution":"approved"}

$ rd show rdtestgateprojyar-dd6
Status:   active
```

After approval, the agent checks `rd gates` — no pending entries — and continues work.

---

## Part 7: Resuming work (for agents)

Agents resuming after a context reset (compaction, restart) follow this pattern:

```bash
# What's actionable right now?
rd ready

# What am I currently working?
rd ready --view work

# Load the spec for the active item
rd show <id>

# Continue from where the spec says
```

The `work` view surfaces items in `active` status. `rd show` includes the full history and any progress notes posted during previous sessions.

### Programmatic agent loop

Agents can query JSON directly — no parsing wrapper needed:

```bash
# Get assigned work as JSON
rd ready --view my-work --json

# Claim the first item
ITEM_ID=$(rd ready --view my-work --json | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['id'])")
rd claim $ITEM_ID --reason "Starting batch job"

# Post incremental progress
rd progress $ITEM_ID --notes "Processed 47/142 records"
rd progress $ITEM_ID --notes "Processed 142/142 records — complete"

# Close
rd done $ITEM_ID --reason "Batch complete: 142 records processed, 0 errors"
```

Real transcript excerpt (`test/demo/output/05-agent-workflow.txt`):

```
$ rd ready --view my-work --json
[{"id":"rdtestagentprojs-f79","title":"Reindex search corpus","status":"inbox",...}]

$ rd claim rdtestagentprojs-f79 --reason "Starting batch reindex job"
claimed rdtestagentprojs-f79

$ rd progress rdtestagentprojs-f79 --notes "Processed 47/142 records, 0 errors"
progress noted on rdtestagentprojs-f79

$ rd done rdtestagentprojs-f79 --reason "Batch complete: 142 records processed, 0 errors"
closed rdtestagentprojs-f79 (done)
```

---

## Part 8: Reference

### Status values

| Status | Meaning |
|--------|---------|
| `inbox` | Created, not yet claimed |
| `active` | Being worked now |
| `scheduled` | Planned for later |
| `waiting` | Blocked on a gate or external party |
| `blocked` | Blocked on another item (dep) |
| `done` | Completed |
| `cancelled` | Abandoned with reason |
| `failed` | Attempted and did not succeed |

### Priority and ETA

Priority drives the default ETA offset from creation time:

| Priority | Default ETA offset |
|----------|--------------------|
| P0 | +1 hour |
| P1 | +4 hours |
| P2 | +24 hours |
| P3 | +72 hours |

The `ready` view surfaces items where `eta < now + 4h`. Override with `--eta`:

```bash
rd create "Quarterly review" --priority p2 --eta "2026-04-15T09:00"
```

### Item types

`task`, `decision`, `review`, `reminder`, `deadline`, `prep`, `message`, `directive`

### Views

| View | `rd` command | Shows |
|------|-------------|-------|
| `ready` | `rd ready` | Unblocked, not done, ETA within 4h |
| `work` | `rd ready --view work` | Items you have active |
| `my-work` | `rd ready --view my-work` | Items assigned to you |
| `delegated` | `rd ready --view delegated` | Items you delegated, still open |
| `pending` | `rd list --view pending` | Scheduled for later |
| `overdue` | `rd list --view overdue` | Past ETA, not done |

### Common flags

| Flag | Works with | Effect |
|------|-----------|--------|
| `--json` | `rd ready`, `rd list`, `rd gates`, `rd show`, `rd gate`, `rd approve` | Machine-readable output |
| `--all` | `rd list` | Include done and cancelled items |
| `--view <name>` | `rd ready`, `rd list` | Use a named view predicate |
| `--ttl <duration>` | `rd invite` | Invite token time-to-live (default 2h) |
| `--rd-home <path>` | any `rd` command | Override `$RD_HOME` for this invocation |

### Further reading

- Convention spec: `docs/convention/work-management.md` — full operation declarations, field validation, compaction policy
- Named view predicates: `pkg/views/` — S-expression predicates for each built-in view
- Identity model: [`docs/design/nostr-identity-model.md`](design/nostr-identity-model.md) — `$RD_HOME`, per-actor keys, kind-39301 grants
- Migration runbook: [`docs/nostr-migration.md`](nostr-migration.md), or the short version in the [README](../README.md#migrating-an-existing-campfire-project)
