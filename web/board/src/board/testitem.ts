// Shared test helper: builds a minimal Item, mirroring pkg/views/views_test.go's
// makeItem — hand-authored fixtures, not fold output, per the anti-tautology
// method (docs/design/board-fold-spec.md conformance vectors).
import type { Item } from "./types";

let counter = 0;

export function makeItem(overrides: Partial<Item> = {}): Item {
  counter += 1;
  const now = Date.now() * 1e6; // ms -> ns, matching state.Item's unix-nanos convention
  return {
    id: `t${counter}`,
    title: `Test item ${counter}`,
    type: "task",
    for: "",
    priority: "",
    status: "inbox",
    createdAt: now,
    updatedAt: now,
    ...overrides,
  };
}
