import { beforeEach, describe, expect, it } from "vitest";
import type { Assignment } from "@/lib/workflow/types";
import {
  clearAssignments,
  getSavedAssignment,
  latestSavedAt,
  loadAssignments,
  saveAssignments,
} from "./assignments";
import { resetOfflineDbForTests } from "./db";

function assignment(id: string, batchId: string): Assignment {
  return {
    assignmentId: id,
    batchId,
    assignmentStatus: "ACCEPTED",
    assignmentSequence: 1,
    version: 1,
  };
}

const SAVED_AT = new Date("2026-10-16T08:30:00.000Z");

describe("saved assignments", () => {
  beforeEach(async () => {
    await resetOfflineDbForTests();
  });

  it("saves and loads one user's assignments", async () => {
    await saveAssignments(
      "user-1",
      [assignment("asg-1", "batch-1"), assignment("asg-2", "batch-2")],
      {},
      SAVED_AT,
    );
    const saved = await loadAssignments("user-1");
    expect(saved).toEqual([
      {
        userId: "user-1",
        assignmentId: "asg-1",
        assignment: assignment("asg-1", "batch-1"),
        batchVersion: null,
        savedAt: "2026-10-16T08:30:00.000Z",
      },
      expect.objectContaining({ assignmentId: "asg-2" }),
    ]);
  });

  it("replaces the previous list instead of merging", async () => {
    await saveAssignments("user-1", [
      assignment("asg-1", "batch-1"),
      assignment("asg-2", "batch-2"),
    ]);
    await saveAssignments("user-1", [assignment("asg-3", "batch-3")]);
    const saved = await loadAssignments("user-1");
    expect(saved.map((row) => row.assignmentId)).toEqual(["asg-3"]);
    expect(await getSavedAssignment("user-1", "asg-1")).toBeNull();
  });

  it("keeps each user's assignments apart", async () => {
    await saveAssignments("user-1", [assignment("asg-1", "batch-1")]);
    await saveAssignments("user-2", [assignment("asg-2", "batch-2")]);
    await saveAssignments("user-1", []);
    expect(await loadAssignments("user-1")).toEqual([]);
    expect(
      (await loadAssignments("user-2")).map((row) => row.assignmentId),
    ).toEqual(["asg-2"]);
  });

  it("prefers the response batch version, then the session one, then null", async () => {
    await saveAssignments(
      "user-1",
      [
        { ...assignment("asg-1", "batch-1"), batchVersion: 7 },
        assignment("asg-2", "batch-2"),
        assignment("asg-3", "batch-3"),
      ],
      { "batch-1": 5, "batch-2": 6 },
    );
    const versions = Object.fromEntries(
      (await loadAssignments("user-1")).map((row) => [
        row.assignmentId,
        row.batchVersion,
      ]),
    );
    expect(versions).toEqual({ "asg-1": 7, "asg-2": 6, "asg-3": null });
  });

  it("reads one saved assignment", async () => {
    await saveAssignments("user-1", [assignment("asg-1", "batch-1")]);
    expect(await getSavedAssignment("user-1", "asg-1")).toEqual(
      expect.objectContaining({
        assignment: assignment("asg-1", "batch-1"),
      }),
    );
    expect(await getSavedAssignment("user-2", "asg-1")).toBeNull();
  });

  it("clears only the given user", async () => {
    await saveAssignments("user-1", [assignment("asg-1", "batch-1")]);
    await saveAssignments("user-2", [assignment("asg-2", "batch-2")]);
    await clearAssignments("user-1");
    expect(await loadAssignments("user-1")).toEqual([]);
    expect(await loadAssignments("user-2")).toHaveLength(1);
  });

  it("reports the newest save time", async () => {
    const records = await saveAssignments(
      "user-1",
      [assignment("asg-1", "batch-1")],
      {},
      SAVED_AT,
    );
    expect(latestSavedAt(records)).toBe("2026-10-16T08:30:00.000Z");
    expect(latestSavedAt([])).toBeNull();
  });
});
