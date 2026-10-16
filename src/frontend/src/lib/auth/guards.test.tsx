import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RequireAuth } from "./guards";
import type { SessionUser } from "./types";

const replaceLocation = vi.fn();
let sessionValue: Record<string, unknown> = {};

vi.mock("./session-context", () => ({
  useSession: () => sessionValue,
}));

vi.mock("./storage", () => ({
  replaceLocation: (path: string) => replaceLocation(path),
}));

const collector: SessionUser = {
  id: "USR-005",
  email: "collector1@ewaste.test",
  name: "Collector One",
  organisationId: "COL-001",
  organisationName: "Collector Co",
  role: "COLLECTOR",
};

describe("RequireAuth", () => {
  beforeEach(() => {
    replaceLocation.mockReset();
  });

  it("redirects to sign in without a session", () => {
    sessionValue = { session: null, ready: true };
    render(
      <RequireAuth>
        <p data-testid="page">Page</p>
      </RequireAuth>,
    );
    expect(screen.queryByTestId("page")).not.toBeInTheDocument();
    expect(replaceLocation).toHaveBeenCalledWith("/login");
  });

  it("renders the page with a session", () => {
    sessionValue = { session: { user: collector }, ready: true };
    render(
      <RequireAuth>
        <p data-testid="page">Page</p>
      </RequireAuth>,
    );
    expect(screen.getByTestId("page")).toBeInTheDocument();
    expect(replaceLocation).not.toHaveBeenCalled();
  });

  it("renders the page offline for the last user on this device", () => {
    sessionValue = {
      session: null,
      ready: true,
      isOffline: true,
      offlineUser: collector,
    };
    render(
      <RequireAuth>
        <p data-testid="page">Page</p>
      </RequireAuth>,
    );
    expect(screen.getByTestId("page")).toBeInTheDocument();
    expect(replaceLocation).not.toHaveBeenCalled();
  });

  it("waits until the session is read", () => {
    sessionValue = { session: null, ready: false };
    render(
      <RequireAuth>
        <p data-testid="page">Page</p>
      </RequireAuth>,
    );
    expect(screen.queryByTestId("page")).not.toBeInTheDocument();
    expect(replaceLocation).not.toHaveBeenCalled();
  });
});
