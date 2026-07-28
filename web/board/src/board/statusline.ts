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

/**
 * ageLabel is the card's top-right age readout, in the prototype's units:
 * minutes under an hour, hours under a day, days above that ("11m", "3h",
 * "23d").
 *
 * ready-56b: the board used to print `${daysSince(...)}d` unconditionally, so
 * on a live board where most work moved within the last day EVERY card read
 * "0d" — an age column carrying no information at all, which is worse than no
 * column. The three-unit form is what makes "11m" and "23d" distinguishable at
 * a glance, and that distinction is the whole point of showing age.
 */
export function ageLabel(unixNanos: number, now: number = Date.now()): string {
  const minutes = Math.max(0, Math.floor((now - unixNanos / 1e6) / 60000));
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
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
