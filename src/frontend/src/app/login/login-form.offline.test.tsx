import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionUser } from "@/lib/auth/types";
import { LoginForm } from "./login-form";

const setSession = vi.fn();
const login = vi.fn();
let offlineUser: SessionUser | null = null;

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({ setSession, isOffline: true, offlineUser }),
}));

vi.mock("@/lib/auth/login", () => ({
  login: (...args: unknown[]) => login(...args),
}));

describe("LoginForm offline", () => {
  beforeEach(() => {
    setSession.mockReset();
    login.mockReset();
    offlineUser = null;
  });

  it("says a connection is needed and sends no sign-in request", async () => {
    const user = userEvent.setup();
    render(<LoginForm />);
    expect(screen.getByTestId("login-offline")).toHaveTextContent(
      "You're offline. Connect to the internet to sign in.",
    );
    await user.type(screen.getByTestId("login-email"), "donor1@ewaste.test");
    await user.type(screen.getByTestId("login-password"), "password");
    expect(screen.getByTestId("login-submit")).toBeDisabled();
    await user.keyboard("{Enter}");
    expect(login).not.toHaveBeenCalled();
    expect(screen.queryByTestId("login-error")).not.toBeInTheDocument();
  });

  it("links the last collector to their saved assignments", () => {
    offlineUser = {
      id: "USR-005",
      email: "collector1@ewaste.test",
      name: "Collector One",
      organisationId: "COL-001",
      organisationName: "Collector Co",
      role: "COLLECTOR",
    };
    render(<LoginForm />);
    const link = screen.getByTestId("login-offline-continue");
    expect(link).toHaveAttribute("href", "/home");
    expect(link).toHaveTextContent("Open assignments saved for Collector One");
  });
});
