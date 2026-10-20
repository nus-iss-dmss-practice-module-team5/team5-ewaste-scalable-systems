import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  OFFLINE_SYNC_DEFAULT,
  OfflineSyncContext,
  type OfflineSyncValue,
} from "@/lib/offline/offline-sync-context";
import type { OfflineDraft } from "@/lib/offline/types";
import { formatWhen } from "../workflow-ui";
import { DraftsView, type DraftsViewProps } from "./drafts-view";
import { failPickupDraft, handoffDraft } from "./test-fixtures";

const drafts = vi.hoisted(() => ({
  confirmDraft: vi.fn(),
  discardDraft: vi.fn(),
}));

vi.mock("@/lib/offline/drafts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/offline/drafts")>()),
  confirmDraft: drafts.confirmDraft,
  discardDraft: drafts.discardDraft,
}));

let sessionValue: Record<string, unknown> = {};

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => sessionValue,
}));

const signedIn = { session: { user: { id: "USR-005" } } };
const offlineMode = { session: null, offlineUser: { id: "USR-005" } };

function renderView(
  list: OfflineDraft[],
  sync: Partial<OfflineSyncValue> = {},
  props: DraftsViewProps = {},
) {
  const syncNow = vi.fn();
  render(
    <OfflineSyncContext.Provider
      value={{ ...OFFLINE_SYNC_DEFAULT, drafts: list, syncNow, ...sync }}
    >
      <DraftsView {...props} />
    </OfflineSyncContext.Provider>,
  );
  return { syncNow };
}

function signedOutDraft(seq?: number) {
  return handoffDraft({ state: "DRAFT", capturedSignedOut: true, seq });
}

describe("DraftsView", () => {
  beforeEach(() => {
    sessionValue = signedIn;
    drafts.confirmDraft.mockReset().mockResolvedValue(undefined);
    drafts.discardDraft.mockReset().mockResolvedValue(true);
  });

  it("confirms a signed-out draft, then starts a sync", async () => {
    const draft = signedOutDraft();
    const { syncNow } = renderView([draft]);
    await userEvent.click(
      screen.getByTestId(`collector-draft-confirm-${draft.draftId}`),
    );
    await waitFor(() => expect(syncNow).toHaveBeenCalledTimes(1));
    expect(drafts.confirmDraft).toHaveBeenCalledWith(draft.draftId, "USR-005");
  });

  it("confirms all in seq order, then syncs once", async () => {
    const later = signedOutDraft(200);
    const earlier = signedOutDraft(100);
    const { syncNow } = renderView([later, earlier]);
    await userEvent.click(screen.getByTestId("collector-drafts-confirm-all"));
    await waitFor(() => expect(syncNow).toHaveBeenCalledTimes(1));
    expect(drafts.confirmDraft.mock.calls.map((call) => call[0])).toEqual([
      earlier.draftId,
      later.draftId,
    ]);
  });

  it("shows an error and does not sync when confirming fails", async () => {
    drafts.confirmDraft.mockRejectedValue(new Error("db closed"));
    const draft = signedOutDraft();
    const { syncNow } = renderView([draft]);
    await userEvent.click(
      screen.getByTestId(`collector-draft-confirm-${draft.draftId}`),
    );
    expect(
      await screen.findByTestId("collector-drafts-error"),
    ).toHaveTextContent("Could not confirm the draft");
    expect(syncNow).not.toHaveBeenCalled();
  });

  it("discards a draft after asking", async () => {
    const draft = handoffDraft({ state: "QUEUED" });
    renderView([draft]);
    await userEvent.click(
      screen.getByTestId(`collector-draft-discard-${draft.draftId}`),
    );
    await userEvent.click(
      screen.getByTestId(`collector-draft-discard-${draft.draftId}-yes`),
    );
    await waitFor(() =>
      expect(drafts.discardDraft).toHaveBeenCalledWith(
        draft.draftId,
        "USR-005",
      ),
    );
  });

  it("opens a conflict with the current state and only Discard or Close", async () => {
    const draft = handoffDraft({
      state: "CONFLICT",
      failure: {
        httpStatus: 409,
        code: "STALE_VERSION",
        correlationId: "corr-1",
        current: {
          assignmentStatus: "COLLECTED",
          updatedAt: "2026-10-16T09:30:00.000Z",
        },
      },
    });
    renderView([draft]);
    await userEvent.click(
      screen.getByTestId(`collector-draft-conflict-${draft.draftId}`),
    );
    expect(screen.getByTestId("collector-conflict-current")).toHaveTextContent(
      `Assignment is COLLECTED · updated ${formatWhen("2026-10-16T09:30:00.000Z")}`,
    );
    expect(screen.queryByText(/send again|overwrite|retry/i)).toBeNull();

    await userEvent.click(screen.getByTestId("collector-conflict-discard"));
    await userEvent.click(screen.getByTestId("collector-conflict-discard-yes"));
    await waitFor(() =>
      expect(drafts.discardDraft).toHaveBeenCalledWith(
        draft.draftId,
        "USR-005",
      ),
    );
    expect(screen.getByTestId("collector-drafts")).toBeInTheDocument();
  });

  it("opens a rejected draft from Fix with the server message", async () => {
    const draft = failPickupDraft({
      state: "REJECTED",
      failure: {
        httpStatus: 422,
        code: "VALIDATION_ERROR",
        correlationId: "corr-2",
        message: "Pickup time is in the future.",
      },
    });
    renderView([draft]);
    await userEvent.click(
      screen.getByTestId(`collector-draft-fix-${draft.draftId}`),
    );
    expect(screen.getByTestId("collector-conflict-rejected")).toHaveTextContent(
      "Pickup time is in the future.",
    );
    await userEvent.click(screen.getByTestId("collector-conflict-close"));
    expect(screen.getByTestId("collector-drafts")).toBeInTheDocument();
  });

  it("hands a rejected draft to onFix to open its form", async () => {
    const draft = failPickupDraft({
      state: "REJECTED",
      failure: {
        httpStatus: 422,
        code: "VALIDATION_ERROR",
        correlationId: "corr-2",
        message: "Pickup time is in the future.",
      },
    });
    const onFix = vi.fn();
    renderView([draft], {}, { onFix });
    await userEvent.click(
      screen.getByTestId(`collector-draft-fix-${draft.draftId}`),
    );
    expect(onFix).toHaveBeenCalledWith(draft);
    expect(screen.queryByTestId("collector-conflict")).toBeNull();
  });

  describe("offline mode without a session", () => {
    beforeEach(() => {
      sessionValue = offlineMode;
    });

    it("offers no confirm and explains that sign-in is needed", () => {
      renderView([signedOutDraft(), signedOutDraft()]);
      expect(screen.getByTestId("collector-drafts-sign-in")).toHaveTextContent(
        "Sign in to confirm and send",
      );
      expect(screen.queryByText("Confirm and send")).toBeNull();
      expect(screen.queryByTestId("collector-drafts-confirm-all")).toBeNull();
    });

    it("lets the holder discard only drafts captured signed out", async () => {
      const captured = signedOutDraft();
      const queued = handoffDraft({ state: "QUEUED" });
      renderView([captured, queued]);
      expect(
        screen.queryByTestId(`collector-draft-discard-${queued.draftId}`),
      ).toBeNull();
      await userEvent.click(
        screen.getByTestId(`collector-draft-discard-${captured.draftId}`),
      );
      await userEvent.click(
        screen.getByTestId(`collector-draft-discard-${captured.draftId}-yes`),
      );
      await waitFor(() =>
        expect(drafts.discardDraft).toHaveBeenCalledWith(
          captured.draftId,
          captured.userId,
        ),
      );
    });

    it("shows a conflict without Discard", async () => {
      const draft = handoffDraft({
        state: "CONFLICT",
        failure: { httpStatus: 403, code: "NOT_ASSIGNED", correlationId: "c" },
      });
      renderView([draft]);
      await userEvent.click(
        screen.getByTestId(`collector-draft-conflict-${draft.draftId}`),
      );
      expect(screen.getByTestId("collector-conflict")).toBeInTheDocument();
      expect(screen.queryByTestId("collector-conflict-discard")).toBeNull();
    });
  });
});
