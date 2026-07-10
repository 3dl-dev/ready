---
name: rd
description: "Nostr-native work management. Use when the user asks about work items, tasks, bugs, or what needs attention. Commands: rd ready, rd create, rd claim, rd done, rd list, rd delegate, rd invite, rd gate."
argument-hint: "<command> [args]"
---

Running rd $ARGUMENTS.

## Commands

| Command | What it does |
|---------|-------------|
| `rd ready` | Show items needing attention now (bare IDs when piped) |
| `rd ready --view work` | Show items actively being worked on |
| `rd ready --view my-work --json` | Items assigned to your identity |
| `rd list [--status ...] [--all] [--json]` | List items with filters |
| `rd show <id>` | Item details, done condition, and audit trail |
| `rd create "..." --type task --priority p1` | Create a work item (bare ID when piped) |
| `rd claim <id>` | Accept work — become the performer |
| `rd done <id> --reason "..."` | Close with reason |
| `rd delegate <id> --to <identity>` | Assign to a person or agent |
| `rd update <id> --status <status>` | Change status, priority, ETA |
| `rd invite` | Generate a one-use invite token for this project |
| `rd join <token>` | Join a project via invite token (auto-syncs items) |
| `rd dep add <id> <blocker-id>` | Wire a dependency (works across projects you own) |
| `rd gate <id> --gate-type design --description "..."` | Escalate to a human |
| `rd gates` | Show pending escalations |
| `rd approve <id> --reason "..."` | Approve a gate |
| `rd init --name <project>` | Initialize a project (local signed-event log + board) |

## Usage

If $ARGUMENTS is empty, run `rd ready` to show what needs attention.

Otherwise, run the command as given:

```bash
rd $ARGUMENTS 2>&1
```

If the command fails with "rd: command not found", tell the user to install it:

```
curl -fsSL https://ready.3dl.dev/install.sh | sh
```

## Resuming work after context loss

If you're an agent waking up in a project:

1. `rd ready --view work` — find your in-progress item
2. `rd show <id>` — read the full spec (self-contained, has the done condition)
3. Continue from where the spec says

## Item lifecycle

1. **Create**: `rd create "..." --type task --priority p1`
2. **Claim**: `rd claim <id>` — sets you as performer, status → active
3. **Work**: do the work
4. **Close**: `rd done <id> --reason "why"` — done, cancelled, or failed

## Pipe-friendly output

When stdout is piped, `rd create`, `rd ready`, and `rd list` print bare item IDs:

```bash
ITEM=$(rd create "Fix auth" --priority p0 --type task)
rd claim $ITEM

for id in $(rd ready); do
  rd show $id --json
done
```

## Multi-agent pattern

```bash
# Owner generates invite tokens for each agent
TOKEN=$(rd invite)

# Agent joins from its own worktree (walk-up config handles identity)
cd worktree-a && rd join $TOKEN
rd ready                    # sees project items
ITEM=$(rd ready | head -1)  # pick first ready item
rd claim $ITEM && rd done $ITEM --reason "..."
```

## Gate escalation

When you hit a decision you can't make:

```bash
rd gate <id> --gate-type design --description "Need ruling: PKCE vs device flow"
# Item moves to waiting. Human runs rd gates → rd approve.
```

## Types and priorities

- **Types**: task, decision, review, reminder, deadline, prep, message, directive
- **Priorities**: p0 (now), p1 (+4h), p2 (+24h), p3 (+72h)
- **Statuses**: inbox, active, scheduled, waiting, blocked, done, cancelled, failed

## Views

`rd ready` defaults to the "ready" view. Other views:

```bash
rd ready --view overdue      # past-due items
rd ready --view delegated    # work I delegated, in progress
rd ready --view my-work      # work assigned to me
rd ready --view work         # actively being worked
rd ready --view pending      # waiting, scheduled, blocked
```

## JSON output

All commands support `--json` for structured output:

```bash
rd list --json | jq '.[] | select(.status == "active")'
rd create "..." --type task --priority p1 --json
```
