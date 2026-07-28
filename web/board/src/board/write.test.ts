// The write path is an AFFORDANCE, not an implementation (item spec: "define
// a typed write interface and leave it unimplemented"). These tests pin
// exactly that: the interface exists and is callable, and every method
// rejects rather than silently succeeding or constructing an event.
import { describe, expect, it } from "vitest";
import { unimplementedWriter, WriteNotImplementedError } from "./write";

describe("unimplementedWriter", () => {
  it("moveStatus rejects with WriteNotImplementedError, never resolves", async () => {
    await expect(unimplementedWriter.moveStatus("ready-abc", "active")).rejects.toBeInstanceOf(
      WriteNotImplementedError,
    );
  });

  it("resolveGate rejects with WriteNotImplementedError, never resolves", async () => {
    await expect(unimplementedWriter.resolveGate("ready-abc", true)).rejects.toBeInstanceOf(
      WriteNotImplementedError,
    );
  });

  it("the error message points at the items that own event construction", () => {
    const err = new WriteNotImplementedError("test");
    expect(err.message).toContain("ready-b2b");
    expect(err.message).toContain("ready-186");
    expect(err.message).toContain("ready-4359");
  });
});
