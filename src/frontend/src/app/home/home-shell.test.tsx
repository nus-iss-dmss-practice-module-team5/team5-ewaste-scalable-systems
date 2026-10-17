import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Role, SessionUser } from "@/lib/auth/types";
import {
  OFFLINE_SYNC_DEFAULT,
  OfflineSyncContext,
  type OfflineSyncValue,
} from "@/lib/offline/offline-sync-context";
import type { OfflineDraft } from "@/lib/offline/types";
import { handoffDraft } from "./offline/test-fixtures";
import { HomeShell } from "./home-shell";

let sessionValue: Record<string, unknown> = {};

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => sessionValue,
}));

vi.mock("./collector-work", () => ({
  CollectorWork: ({ fix }: { fix?: OfflineDraft }) => (
    <p data-testid="collector-view">{fix?.draftId ?? ""}</p>
  ),
}));

const rejected = handoffDraft({ draftId: "draft-rejected", state: "REJECTED" });

vi.mock("./offline/drafts-view", () => ({
  DraftsView: ({ onFix }: { onFix?: (draft: OfflineDraft) => void }) => (
    <button
      type="button"
      data-testid="drafts-view"
      onClick={() => onFix?.(rejected)}
    />
  ),
}));

vi.mock("./opportunities", () => ({
  OpportunityView: () => <p data-testid="recycler-view" />,
}));

vi.mock("./donor-batches", () => ({
  DonorBatchList: () => <p data-testid="donor-view" />,
  DonorBatchForm: () => null,
}));

function userFor(role: Role): SessionUser {
  return {
    id: `USR-${role}`,
    email: "user@ewaste.test",
    name: "Test User",
    organisationId: "ORG-1",
    organisationName: "Org",
    role,
  };
}

describe("HomeShell offline", () => {
  beforeEach(() => {
    sessionValue = {};
  });

  it("opens the collector's saved assignments for the offline user", () => {
    sessionValue = {
      session: null,
      isOffline: true,
      offlineUser: userFor("COLLECTOR"),
      justRenewed: false,
      logout: vi.fn(),
    };
    render(<HomeShell />);
    expect(screen.getByTestId("collector-view")).toBeInTheDocument();
    expect(screen.getByTestId("offline-banner")).toHaveTextContent(
      "You're offline. Showing assignments saved on this device. Recorded pickups and failed pickups are saved on this phone and sync later. Sign in again when you're back online to send them.",
    );
    expect(screen.getByText("Test User")).toBeInTheDocument();
  });

  it("shows other roles a needs-connection message instead of their view", () => {
    sessionValue = {
      session: { user: userFor("DONOR") },
      isOffline: true,
      justRenewed: false,
      logout: vi.fn(),
    };
    render(<HomeShell />);
    expect(screen.queryByTestId("donor-view")).not.toBeInTheDocument();
    expect(screen.getByTestId("offline-needs-connection")).toHaveTextContent(
      "needs a connection",
    );
    expect(screen.getByTestId("offline-banner")).toBeInTheDocument();
  });

  it("shows no offline banner online", () => {
    sessionValue = {
      session: { user: userFor("DONOR") },
      isOffline: false,
      justRenewed: false,
      logout: vi.fn(),
    };
    render(<HomeShell />);
    expect(screen.getByTestId("donor-view")).toBeInTheDocument();
    expect(screen.queryByTestId("offline-banner")).not.toBeInTheDocument();
  });
});

function renderWithSync(sync: Partial<OfflineSyncValue> = {}) {
  const syncNow = vi.fn();
  render(
    <OfflineSyncContext.Provider
      value={{ ...OFFLINE_SYNC_DEFAULT, syncNow, ...sync }}
    >
      <HomeShell />
    </OfflineSyncContext.Provider>,
  );
  return { syncNow };
}

function signedIn(role: Role, isOffline = false) {
  return {
    session: { user: userFor(role) },
    isOffline,
    justRenewed: false,
    logout: vi.fn(),
  };
}

describe("HomeShell offline drafts", () => {
  it("tells an offline collector with a session that pickups are saved", () => {
    sessionValue = signedIn("COLLECTOR", true);
    renderWithSync({ online: false });
    expect(screen.getByTestId("offline-banner")).toHaveTextContent(
      "Recorded pickups and failed pickups are saved on this phone and sync later. Other actions need a connection.",
    );
  });

  it("shows the sync status with Sync now in the collector header", async () => {
    sessionValue = signedIn("COLLECTOR");
    const { syncNow } = renderWithSync({ pending: 2 });
    const header = screen.getByRole("banner");
    expect(header).toContainElement(
      screen.getByTestId("collector-sync-status"),
    );
    expect(screen.getByTestId("collector-sync-status")).toHaveTextContent(
      "Waiting to sync (2)",
    );
    await userEvent.click(screen.getByTestId("collector-sync-now"));
    expect(syncNow).toHaveBeenCalledTimes(1);
  });

  it("offers no Sync now in offline mode without a session", () => {
    sessionValue = {
      session: null,
      isOffline: true,
      offlineUser: userFor("COLLECTOR"),
      justRenewed: false,
      logout: vi.fn(),
    };
    renderWithSync({ online: false });
    expect(screen.getByTestId("collector-sync-status")).toBeInTheDocument();
    expect(screen.queryByTestId("collector-sync-now")).toBeNull();
  });

  it.each(["DONOR", "RECYCLER", "AUDITOR", "ADMIN"] as const)(
    "shows no sync status or drafts nav for %s",
    (role) => {
      sessionValue = signedIn(role);
      renderWithSync({ pending: 1 });
      expect(screen.queryByTestId("collector-sync-status")).toBeNull();
      expect(
        screen.queryByRole("button", { name: /Offline drafts/ }),
      ).toBeNull();
    },
  );

  it("opens the drafts section from the collector nav, with a count", async () => {
    sessionValue = signedIn("COLLECTOR");
    renderWithSync({
      drafts: [
        handoffDraft({ state: "QUEUED" }),
        handoffDraft({ state: "CONFLICT" }),
        handoffDraft({ state: "SYNCED" }),
      ],
    });
    expect(screen.getByTestId("nav-drafts-count")).toHaveTextContent("2");
    await userEvent.click(
      screen.getByRole("button", { name: /Offline drafts/ }),
    );
    expect(screen.getByTestId("drafts-view")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 1, name: "Offline drafts" }),
    ).toBeInTheDocument();
  });

  it("shows no count when every draft is synced", () => {
    sessionValue = signedIn("COLLECTOR");
    renderWithSync({ drafts: [handoffDraft({ state: "SYNCED" })] });
    expect(screen.queryByTestId("nav-drafts-count")).toBeNull();
  });

  it("opens the rejected draft's assignment form from Fix in Drafts", async () => {
    sessionValue = signedIn("COLLECTOR");
    renderWithSync({ drafts: [rejected] });
    await userEvent.click(
      screen.getByRole("button", { name: /Offline drafts/ }),
    );
    await userEvent.click(screen.getByTestId("drafts-view"));
    expect(screen.getByTestId("collector-view")).toHaveTextContent(
      "draft-rejected",
    );

    // Leaving the section drops the fix.
    await userEvent.click(screen.getByRole("button", { name: /History/ }));
    await userEvent.click(screen.getByRole("button", { name: /Assignments/ }));
    expect(screen.getByTestId("collector-view")).toHaveTextContent("");
  });
});
