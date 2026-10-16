import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { OfflineRequired } from "./offline-required";

describe("OfflineRequired", () => {
  it("shows the plain notice", () => {
    render(<OfflineRequired />);
    expect(screen.getByRole("note")).toHaveTextContent(
      /^This needs a connection\.$/,
    );
  });

  it("names the action", () => {
    render(<OfflineRequired action="Reject assignment" />);
    expect(screen.getByRole("note")).toHaveTextContent(
      "Reject assignment: This needs a connection.",
    );
  });
});
