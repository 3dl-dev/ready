// Card status line derivation (item spec: "one status line (agent working /
// waits on X / untouched Nd / yours / queued)"). Priority order, most
// specific first: an item assigned to the viewer always reads "yours" even
// if it also happens to be active, because "this is mine" is the more
// actionable fact for the person looking at their own board.
import type { Item } from "./types";

export function daysSince(unixNanos: number, now: number = Date.now()): number {
  const ms = unixNanos / 1e6;
  return Math.max(0, Math.floor((now - ms) / (1000 * 60 * 60 * 24)));
}

export function statusLine(item: Item, viewerId?: string, now: number = Date.now()): string {
  if (viewerId && (item.for === viewerId || item.by === viewerId)) {
    return "yours";
  }
  if ((item.status === "waiting" || item.status === "blocked") && item.waitingOn) {
    return `waits on ${item.waitingOn}`;
  }
  if (item.status === "active") {
    return "agent working";
  }
  if (item.status === "inbox") {
    return "queued";
  }
  return `untouched ${daysSince(item.updatedAt, now)}d`;
}
