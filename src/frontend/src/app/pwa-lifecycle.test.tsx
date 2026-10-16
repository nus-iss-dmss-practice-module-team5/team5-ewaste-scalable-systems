import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PwaLifecycle } from "./pwa-lifecycle";

const mocks = vi.hoisted(() => ({
  session: null as object | null,
  register: vi.fn(),
  precache: vi.fn(),
}));

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({ session: mocks.session }),
}));
vi.mock("@/lib/offline/register-sw", () => ({
  registerServiceWorker: mocks.register,
}));
vi.mock("@/lib/offline/precache", () => ({
  precacheLoadedStatic: mocks.precache,
}));

describe("PwaLifecycle", () => {
  beforeEach(() => {
    mocks.session = null;
    mocks.register.mockReset().mockResolvedValue(null);
    mocks.precache.mockReset().mockResolvedValue(0);
  });

  it("registers the service worker on mount but waits for sign-in to precache", () => {
    render(<PwaLifecycle />);

    expect(mocks.register).toHaveBeenCalledTimes(1);
    expect(mocks.precache).not.toHaveBeenCalled();
  });

  it("precaches loaded assets once after sign-in", () => {
    const view = render(<PwaLifecycle />);

    mocks.session = { user: { id: "USR-1" } };
    view.rerender(<PwaLifecycle />);
    mocks.session = { user: { id: "USR-1" }, renewed: true };
    view.rerender(<PwaLifecycle />);
    mocks.session = null;
    view.rerender(<PwaLifecycle />);
    mocks.session = { user: { id: "USR-2" } };
    view.rerender(<PwaLifecycle />);

    expect(mocks.precache).toHaveBeenCalledTimes(1);
    expect(mocks.register).toHaveBeenCalledTimes(1);
  });
});
