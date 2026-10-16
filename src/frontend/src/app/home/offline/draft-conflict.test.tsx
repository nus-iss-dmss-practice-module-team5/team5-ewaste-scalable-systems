import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { DraftFailure, OfflineDraft } from "@/lib/offline/types";
import { formatWhen } from "../workflow-ui";
import { DraftConflict } from "./draft-conflict";
import { failPickupDraft, handoffDraft } from "./test-fixtures";

function failure(overrides: Partial<DraftFailure> = {}): DraftFailure {
  return {
    httpStatus: 409,
    code: "STALE_VERSION",
    correlationId: "corr-123",
    ...overrides,
  };
}

function renderConflict(
  draft: OfflineDraft = handoffDraft({ state: "CONFLICT", failure: failure() }),
) {
  const onDiscard = vi.fn();
  const onClose = vi.fn();
  render(
    <DraftConflict draft={draft} onDiscard={onDiscard} onClose={onClose} />,
  );
  return { onDiscard, onClose };
}

describe("DraftConflict", () => {
  it("has a heading and shows what the collector tried", () => {
    const draft = handoffDraft({ state: "CONFLICT", failure: failure() });
    renderConflict(draft);
    expect(
      screen.getByRole("heading", { name: "This draft was not applied" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/Pickup recorded · Batch/)).toHaveTextContent(
      `Captured ${formatWhen(draft.capturedAt)}`,
    );
    const summary = screen.getByTestId("collector-conflict-summary");
    expect(summary).toHaveTextContent("Dana Donor");
    expect(summary).toHaveTextContent("12");
  });

  it.each([
    ["STALE_VERSION", 409, "This batch changed after you saved the draft."],
    ["NOT_ASSIGNED", 403, "This assignment is no longer assigned to you."],
    ["CONFLICT", 409, "Someone else updated this assignment."],
    [
      "IDEMPOTENCY_CONFLICT",
      409,
      "This draft clashed with an earlier send of the same command",
    ],
  ])("explains %s", (code, httpStatus, text) => {
    renderConflict(
      handoffDraft({
        state: "CONFLICT",
        failure: failure({ code, httpStatus }),
      }),
    );
    expect(screen.getByTestId("collector-conflict-reason")).toHaveTextContent(
      text,
    );
  });

  it("shows the reference for an idempotency conflict", () => {
    renderConflict(
      handoffDraft({
        state: "CONFLICT",
        failure: failure({
          code: "IDEMPOTENCY_CONFLICT",
          correlationId: "corr-idem-9",
        }),
      }),
    );
    expect(
      screen.getByTestId("collector-conflict-reference"),
    ).toHaveTextContent("Reference corr-idem-9");
  });

  it("falls back to the server message for an unknown code", () => {
    renderConflict(
      handoffDraft({
        state: "CONFLICT",
        failure: failure({ code: "SOMETHING_NEW", message: "Odd answer" }),
      }),
    );
    expect(screen.getByTestId("collector-conflict-reason")).toHaveTextContent(
      "Odd answer",
    );
  });

  it("warns when the command may already have been applied", () => {
    renderConflict(
      handoffDraft({
        state: "CONFLICT",
        failure: failure({ possiblyApplied: true }),
      }),
    );
    expect(
      screen.getByTestId("collector-conflict-possibly-applied"),
    ).toHaveTextContent(
      "This may already have been recorded — check the current status before redoing it.",
    );
  });

  it("does not warn when possiblyApplied is unset", () => {
    renderConflict();
    expect(
      screen.queryByTestId("collector-conflict-possibly-applied"),
    ).toBeNull();
  });

  it("shows the current state when it was fetched", () => {
    const updatedAt = "2026-10-16T10:00:00.000Z";
    renderConflict(
      handoffDraft({
        state: "CONFLICT",
        failure: failure({
          current: { assignmentStatus: "SUPERSEDED", updatedAt },
        }),
      }),
    );
    expect(screen.getByTestId("collector-conflict-current")).toHaveTextContent(
      `Assignment is SUPERSEDED · updated ${formatWhen(updatedAt)}`,
    );
  });

  it("says when the current state is unavailable", () => {
    renderConflict();
    expect(screen.getByTestId("collector-conflict-current")).toHaveTextContent(
      "Current state unavailable",
    );
  });

  it("never offers to send anyway or overwrite", () => {
    renderConflict(
      handoffDraft({
        state: "CONFLICT",
        failure: failure({ possiblyApplied: true }),
      }),
    );
    const names = screen
      .getAllByRole("button")
      .map((button) => button.textContent ?? "");
    expect(names).toEqual(["Discard draft", "Back to drafts"]);
    for (const name of names) {
      expect(name).not.toMatch(/send|overwrite|retry|anyway/i);
    }
  });

  it("shows the server message and the fix hint for a rejected draft", () => {
    renderConflict(
      failPickupDraft({
        state: "REJECTED",
        failure: failure({
          httpStatus: 422,
          code: "VALIDATION_ERROR",
          message: "Pickup time is in the future",
        }),
      }),
    );
    expect(
      screen.getByRole("heading", { name: "The server refused this draft" }),
    ).toBeInTheDocument();
    const rejected = screen.getByTestId("collector-conflict-rejected");
    expect(rejected).toHaveTextContent("Pickup time is in the future");
    expect(rejected).toHaveTextContent("Fix and save as new draft");
    expect(screen.getByTestId("collector-conflict-summary")).toHaveTextContent(
      "Donor unavailable",
    );
  });

  it("asks before discarding and closes", async () => {
    const user = userEvent.setup();
    const { onDiscard, onClose } = renderConflict();
    await user.click(screen.getByTestId("collector-conflict-discard"));
    expect(onDiscard).not.toHaveBeenCalled();
    const ask = screen.getByTestId("collector-conflict-discard-confirm");
    await user.click(within(ask).getByRole("button", { name: /Keep/ }));
    expect(onDiscard).not.toHaveBeenCalled();

    await user.click(screen.getByTestId("collector-conflict-discard"));
    await user.click(screen.getByTestId("collector-conflict-discard-yes"));
    expect(onDiscard).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Back to drafts" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("shows no Discard when none is offered", () => {
    render(
      <DraftConflict
        draft={handoffDraft({ state: "CONFLICT", failure: failure() })}
        onClose={vi.fn()}
      />,
    );
    expect(screen.queryByTestId("collector-conflict-discard")).toBeNull();
    expect(screen.getByTestId("collector-conflict-close")).toBeInTheDocument();
  });
});
