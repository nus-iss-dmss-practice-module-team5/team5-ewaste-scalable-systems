import { describe, expect, it } from "vitest";
import { parseAssignment } from "./parse";

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
