import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "@/lib/auth/types";
import { LoginForm } from "./login-form";

const setSession = vi.fn();
const login = vi.fn();

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({ setSession }),
}));

vi.mock("@/lib/auth/login", () => ({
  login: (...args: unknown[]) => login(...args),
}));

function donorSession(): Session {
  return {
    user: {
      id: "USR-003",
      email: "donor1@ewaste.test",
      name: "Green Office Donor",
      organisationId: "DON-001",
      organisationName: "DON-001",
      role: "DONOR",
    },
    tokens: {
      accessToken: "access",
      refreshToken: "refresh",
      tokenType: "Bearer",
      expiresIn: 900,
      refreshExpiresIn: 86400,
    },
    accessExpiresAt: Date.now() + 900_000,
    refreshExpiresAt: Date.now() + 86_400_000,
  };
}

describe("LoginForm", () => {
  beforeEach(() => {
    setSession.mockReset();
    login.mockReset();
  });

  it("shows the session-expired banner when opened after expiry", () => {
    render(<LoginForm expired />);
    expect(screen.getByTestId("login-session-expired")).toHaveTextContent(
      "Your session expired. Please sign in again.",
    );
  });

  it("shows a generic error when login fails", async () => {
    const user = userEvent.setup();
    login.mockRejectedValue({
      code: "AUTH_INVALID_CREDENTIALS",
      message: "Invalid email or password",
      correlationId: "corr-login-401",
    });

    render(<LoginForm />);
    await user.type(screen.getByTestId("login-email"), "donor1@ewaste.test");
    await user.type(screen.getByTestId("login-password"), "wrong-password");
    await user.click(screen.getByTestId("login-submit"));

    expect(await screen.findByTestId("login-error")).toHaveTextContent(
      "Invalid email or password",
    );
    expect(setSession).not.toHaveBeenCalled();
  });

  it("stores the session after a successful login", async () => {
    const user = userEvent.setup();
    const session = donorSession();
    login.mockResolvedValue(session);

    render(<LoginForm />);
    await user.type(screen.getByTestId("login-email"), "donor1@ewaste.test");
    await user.type(screen.getByTestId("login-password"), "correct-password");
    await user.click(screen.getByTestId("login-submit"));

    await waitFor(() => {
      expect(setSession).toHaveBeenCalledWith(session);
    });
    expect(screen.queryByTestId("login-error")).not.toBeInTheDocument();
  });
});
