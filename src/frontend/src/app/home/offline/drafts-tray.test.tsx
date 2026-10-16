import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { OfflineDraft } from "@/lib/offline/types";
import { formatWhen } from "../workflow-ui";
import { DraftsTray, groupDrafts, type DraftsTrayProps } from "./drafts-tray";
import { failPickupDraft, handoffDraft } from "./test-fixtures";

function renderTray(
  drafts: OfflineDraft[],
  overrides: Partial<DraftsTrayProps> = {},
) {
  const handlers = {
    onConfirm: vi.fn(),
    onConfirmAll: vi.fn(),
    onDiscard: vi.fn(),
    onOpenConflict: vi.fn(),
    onFix: vi.fn(),
  };
  render(<DraftsTray drafts={drafts} {...handlers} {...overrides} />);
  return handlers;
}

const conflictFailure = {
  httpStatus: 409,
  code: "STALE_VERSION",
  correlationId: "corr-1",
};

describe("groupDrafts", () => {
  it("groups by confirmation and state, in seq order", () => {
    const signedOut = handoffDraft({
      state: "DRAFT",
      capturedSignedOut: true,
      seq: 9,
    });
    const confirmed = handoffDraft({
      state: "QUEUED",
      capturedSignedOut: true,
      confirmedAt: "2026-10-16T09:00:00.000Z",
      seq: 5,
    });
    const queued = failPickupDraft({ state: "QUEUED", seq: 2 });
    const sending = handoffDraft({
      state: "SENDING",
      frozenAt: "2026-10-16T09:01:00.000Z",
      seq: 1,
    });
    const conflict = handoffDraft({
      state: "CONFLICT",
      failure: conflictFailure,
    });
    const rejected = failPickupDraft({ state: "REJECTED" });
    const synced = handoffDraft({ state: "SYNCED" });

    const groups = groupDrafts([
      signedOut,
      confirmed,
      queued,
      sending,
      conflict,
      rejected,
      synced,
    ]);
    expect(groups.confirm).toEqual([signedOut]);
    expect(groups.waiting).toEqual([sending, queued, confirmed]);
    expect(groups.attention).toEqual([conflict, rejected]);
    expect(groups.synced).toEqual([synced]);
  });
});

describe("DraftsTray", () => {
  it("shows an empty state", () => {
    renderTray([]);
    expect(screen.getByTestId("collector-drafts-empty")).toHaveTextContent(
      "No drafts on this phone.",
    );
  });

  it("renders each group under its heading", () => {
    renderTray([
      handoffDraft({ state: "DRAFT", capturedSignedOut: true }),
      failPickupDraft({ state: "QUEUED" }),
      handoffDraft({ state: "CONFLICT", failure: conflictFailure }),
      handoffDraft({
        state: "SYNCED",
        result: {
          assignmentStatus: "COMPLETED",
          correlationId: "c",
          syncedAt: "2026-10-16T09:30:00.000Z",
        },
      }),
    ]);
    for (const title of [
      "Waiting for you to confirm (1)",
      "Waiting to sync (1)",
      "Needs attention (1)",
      "Synced (1)",
    ]) {
      expect(screen.getByRole("heading", { name: title })).toBeInTheDocument();
    }
    expect(
      within(screen.getByTestId("collector-drafts-synced")).getByText(
        /Assignment is COMPLETED/,
      ),
    ).toBeInTheDocument();
  });

  it("shows the row facts", () => {
    const draft = failPickupDraft({
      state: "QUEUED",
      batchId: "batch-77",
      assignmentId: "asg-77",
      attempts: 3,
    });
    renderTray([draft]);
    const row = screen.getByTestId(`collector-draft-${draft.draftId}`);
    expect(row).toHaveTextContent("Pickup failed");
    expect(row).toHaveTextContent("Batch batch-77 · Assignment asg-77");
    expect(row).toHaveTextContent(
      `Captured ${formatWhen(draft.capturedAt)} · Attempts 3`,
    );
    expect(
      screen.getByTestId(`collector-draft-state-${draft.draftId}`),
    ).toHaveTextContent("Waiting to sync");
  });

  it("shows the payload and idempotency key in the expandable detail", async () => {
    const user = userEvent.setup();
    const draft = handoffDraft({
      state: "QUEUED",
      idempotencyKey: "4b7c0d7e-key",
    });
    renderTray([draft]);
    expect(
      screen.queryByTestId(`collector-draft-key-${draft.draftId}`),
    ).toBeNull();

    const toggle = screen.getByTestId(
      `collector-draft-details-${draft.draftId}`,
    );
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(
      screen.getByTestId(`collector-draft-key-${draft.draftId}`),
    ).toHaveTextContent("4b7c0d7e-key");
    const summary = screen.getByTestId(
      `collector-draft-summary-${draft.draftId}`,
    );
    expect(summary).toHaveTextContent("Dana Donor");
    expect(summary).toHaveTextContent("Back door");

    await user.click(toggle);
    expect(
      screen.queryByTestId(`collector-draft-key-${draft.draftId}`),
    ).toBeNull();
  });

  it("lists signed-out drafts with details and confirms one or all", async () => {
    const user = userEvent.setup();
    const first = failPickupDraft({ state: "DRAFT", capturedSignedOut: true });
    const second = handoffDraft({ state: "DRAFT", capturedSignedOut: true });
    const handlers = renderTray([first, second]);

    // Rule 7: details are shown without expanding.
    expect(
      screen.getByTestId(`collector-draft-summary-${first.draftId}`),
    ).toHaveTextContent("Donor unavailable");

    await user.click(
      screen.getByTestId(`collector-draft-confirm-${second.draftId}`),
    );
    expect(handlers.onConfirm).toHaveBeenCalledWith(second.draftId);

    await user.click(screen.getByRole("button", { name: "Confirm all" }));
    expect(handlers.onConfirmAll).toHaveBeenCalledTimes(1);
  });

  it("does not offer Confirm and send outside the confirm group", () => {
    const draft = handoffDraft({ state: "QUEUED" });
    renderTray([draft]);
    expect(
      screen.queryByTestId(`collector-draft-confirm-${draft.draftId}`),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: "Confirm all" })).toBeNull();
  });

  it("asks inline before discarding", async () => {
    const user = userEvent.setup();
    const draft = handoffDraft({ state: "DRAFT", capturedSignedOut: true });
    const handlers = renderTray([draft]);
    const discardId = `collector-draft-discard-${draft.draftId}`;

    await user.click(screen.getByTestId(discardId));
    expect(handlers.onDiscard).not.toHaveBeenCalled();
    expect(screen.getByTestId(`${discardId}-confirm`)).toHaveTextContent(
      "Discard this draft? It will not be sent.",
    );
    await user.click(screen.getByTestId(`${discardId}-keep`));
    expect(handlers.onDiscard).not.toHaveBeenCalled();
    expect(screen.getByTestId(discardId)).toBeInTheDocument();

    await user.click(screen.getByTestId(discardId));
    await user.click(screen.getByTestId(`${discardId}-yes`));
    expect(handlers.onDiscard).toHaveBeenCalledWith(draft.draftId);
  });

  it("hides Discard for a frozen draft that has no final answer", () => {
    const sending = handoffDraft({
      state: "SENDING",
      frozenAt: "2026-10-16T09:01:00.000Z",
    });
    const retrying = failPickupDraft({
      state: "QUEUED",
      frozenAt: "2026-10-16T09:01:00.000Z",
      attempts: 2,
    });
    const fresh = failPickupDraft({ state: "QUEUED" });
    renderTray([sending, retrying, fresh]);
    for (const draft of [sending, retrying]) {
      expect(
        screen.queryByTestId(`collector-draft-discard-${draft.draftId}`),
      ).toBeNull();
      expect(
        screen.getByTestId(`collector-draft-frozen-${draft.draftId}`),
      ).toBeInTheDocument();
    }
    expect(
      screen.getByTestId(`collector-draft-discard-${fresh.draftId}`),
    ).toBeInTheDocument();
  });

  it("allows Discard on a frozen draft once it has a final answer", () => {
    const draft = handoffDraft({
      state: "CONFLICT",
      frozenAt: "2026-10-16T09:01:00.000Z",
      failure: conflictFailure,
    });
    renderTray([draft]);
    expect(
      screen.getByTestId(`collector-draft-discard-${draft.draftId}`),
    ).toBeInTheDocument();
  });

  it("opens the conflict screen and the fix for rejected drafts", async () => {
    const user = userEvent.setup();
    const conflict = handoffDraft({
      state: "CONFLICT",
      failure: conflictFailure,
    });
    const rejected = failPickupDraft({
      state: "REJECTED",
      failure: {
        httpStatus: 422,
        code: "VALIDATION_ERROR",
        message: "Observed details are too long",
        correlationId: "corr-2",
      },
    });
    const handlers = renderTray([conflict, rejected]);

    await user.click(
      screen.getByTestId(`collector-draft-conflict-${conflict.draftId}`),
    );
    expect(handlers.onOpenConflict).toHaveBeenCalledWith(conflict.draftId);

    expect(
      screen.getByTestId(`collector-draft-${rejected.draftId}`),
    ).toHaveTextContent("Observed details are too long");
    await user.click(
      screen.getByTestId(`collector-draft-fix-${rejected.draftId}`),
    );
    expect(handlers.onFix).toHaveBeenCalledWith(rejected.draftId);
    expect(
      screen.queryByTestId(`collector-draft-fix-${conflict.draftId}`),
    ).toBeNull();
  });

  it("composes the sync status at the top", async () => {
    const user = userEvent.setup();
    const onSyncNow = vi.fn();
    renderTray([handoffDraft({ state: "QUEUED" })], {
      sync: { online: true, pending: 1, syncing: false, onSyncNow },
    });
    expect(screen.getByRole("status")).toHaveTextContent("Waiting to sync (1)");
    await user.click(screen.getByRole("button", { name: "Sync now" }));
    expect(onSyncNow).toHaveBeenCalledTimes(1);
  });

  it("gives row buttons distinct accessible names", () => {
    const draft = handoffDraft({
      state: "DRAFT",
      capturedSignedOut: true,
      batchId: "batch-9",
    });
    renderTray([draft]);
    expect(
      screen.getByRole("button", {
        name: "Confirm and send Pickup recorded draft for batch batch-9",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: "Discard Pickup recorded draft for batch batch-9",
      }),
    ).toBeInTheDocument();
  });

  it("limits Discard to drafts allowed by canDiscard", () => {
    const allowed = handoffDraft({ state: "QUEUED" });
    const blocked = handoffDraft({ state: "QUEUED" });
    renderTray([allowed, blocked], {
      canDiscard: (draft) => draft.draftId === allowed.draftId,
    });
    expect(
      screen.getByTestId(`collector-draft-discard-${allowed.draftId}`),
    ).toBeInTheDocument();
    expect(
      screen.queryByTestId(`collector-draft-discard-${blocked.draftId}`),
    ).toBeNull();
  });
});
