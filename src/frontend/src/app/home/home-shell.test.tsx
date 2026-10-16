import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Role, SessionUser } from "@/lib/auth/types";
import { HomeShell } from "./home-shell";

let sessionValue: Record<string, unknown> = {};

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => sessionValue,
}));

vi.mock("./collector-work", () => ({
  CollectorWork: () => <p data-testid="collector-view" />,
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
      "You're offline. Showing assignments saved on this device. Sign in again when you're back online.",
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
