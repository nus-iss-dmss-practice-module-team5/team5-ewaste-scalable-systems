import { describe, expect, it } from "vitest";
import { parseAssignment, parseAssignmentPage } from "./parse";

const wire = {
  assignment_id: "asg-1",
  batch_id: "batch-1",
  assignment_status: "ACCEPTED",
  assignment_sequence: 1,
  version: 1,
};

describe("parseAssignment batch version", () => {
  it("reads batch_version when the API sends it", () => {
    expect(parseAssignment({ ...wire, batch_version: 5 }).batchVersion).toBe(5);
  });

  it("leaves batchVersion out when the API does not send it", () => {
    expect(parseAssignment(wire)).not.toHaveProperty("batchVersion");
  });
});

describe("parseAssignment pickup bounds", () => {
  it("reads assigned_at as the earliest pickup time", () => {
    expect(
      parseAssignment({ ...wire, assigned_at: "2026-09-23T10:00:30Z" })
        .assignedAt,
    ).toBe("2026-09-23T10:00:30Z");
  });

  it("reads server_time from an assignment list", () => {
    const page = parseAssignmentPage({
      data: [wire],
      page: 1,
      page_size: 20,
      total_count: 1,
      correlation_id: "c",
      server_time: "2026-09-23T11:00:00Z",
    });
    expect(page.serverTime).toBe("2026-09-23T11:00:00Z");
  });
});
