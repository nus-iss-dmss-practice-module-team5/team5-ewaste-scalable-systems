import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { formatWhen } from "../workflow-ui";
import { SyncStatus, type SyncStatusProps } from "./sync-status";

function renderStatus(overrides: Partial<SyncStatusProps> = {}) {
  const onSyncNow = vi.fn();
  render(
    <SyncStatus
      online
      pending={0}
      syncing={false}
      onSyncNow={onSyncNow}
      {...overrides}
    />,
  );
  return { onSyncNow, status: screen.getByRole("status") };
}

describe("SyncStatus", () => {
  it("shows offline and disables Sync now", () => {
    const { status } = renderStatus({ online: false, pending: 2 });
    expect(status).toHaveTextContent(
      "Offline — drafts are saved on this phone",
    );
    expect(status).toHaveAttribute("aria-live", "polite");
    expect(screen.getByRole("button", { name: "Sync now" })).toBeDisabled();
  });

  it("shows the pending count while waiting", () => {
    const { status } = renderStatus({ pending: 3 });
    expect(status).toHaveTextContent("Waiting to sync (3)");
    expect(screen.getByRole("button", { name: "Sync now" })).toBeEnabled();
  });

  it("shows syncing and disables Sync now", () => {
    const { status } = renderStatus({ pending: 1, syncing: true });
    expect(status).toHaveTextContent("Syncing…");
    expect(screen.getByRole("button", { name: "Sync now" })).toBeDisabled();
  });

  it("treats a busy stop as another tab syncing", () => {
    const { status } = renderStatus({ pending: 1, stoppedReason: "busy" });
    expect(status).toHaveTextContent("Syncing…");
  });

  it("shows all synced after a busy stop once nothing is pending", () => {
    const { status } = renderStatus({ pending: 0, stoppedReason: "busy" });
    expect(status).toHaveTextContent("All synced");
  });

  it("asks to sign in when the run stopped for the session", () => {
    const { status } = renderStatus({
      pending: 2,
      stoppedReason: "needs-sign-in",
    });
    expect(status).toHaveTextContent("Sign in to sync");
  });

  it("says it will retry when the server could not be reached", () => {
    const { status } = renderStatus({
      pending: 2,
      stoppedReason: "retry-later",
    });
    expect(status).toHaveTextContent("Couldn't reach the server — will retry");
  });

  it("shows all synced with the last sync time", () => {
    const lastSyncAt = "2026-10-16T09:00:00.000Z";
    const { status } = renderStatus({ stoppedReason: "done", lastSyncAt });
    expect(status).toHaveTextContent(
      `All synced · last sync ${formatWhen(lastSyncAt)}`,
    );
    expect(status).toHaveAttribute("data-state", "synced");
  });

  it("ignores a stale stop reason when nothing is pending", () => {
    const { status } = renderStatus({ stoppedReason: "needs-sign-in" });
    expect(status).toHaveTextContent("All synced");
  });

  it("calls onSyncNow", async () => {
    const { onSyncNow } = renderStatus({ pending: 1 });
    await userEvent.click(screen.getByRole("button", { name: "Sync now" }));
    expect(onSyncNow).toHaveBeenCalledTimes(1);
  });

  it("hides Sync now without a handler", () => {
    render(<SyncStatus online pending={0} syncing={false} />);
    expect(screen.queryByRole("button", { name: "Sync now" })).toBeNull();
  });
});
