import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { registerServiceWorker, serviceWorkerUrl } from "./register-sw";

const register = vi.fn();

function installServiceWorkerApi() {
  Object.defineProperty(navigator, "serviceWorker", {
    configurable: true,
    value: { register },
  });
}

function removeServiceWorkerApi() {
  Reflect.deleteProperty(navigator, "serviceWorker");
}

describe("registerServiceWorker", () => {
  beforeEach(() => {
    register.mockReset();
    register.mockResolvedValue({ scope: "/" });
    vi.stubEnv("NEXT_PUBLIC_BUILD_ID", "build-42");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
    removeServiceWorkerApi();
  });

  it("registers /sw.js with the build id and root scope in production", async () => {
    vi.stubEnv("NODE_ENV", "production");
    installServiceWorkerApi();

    const registration = await registerServiceWorker();

    expect(register).toHaveBeenCalledTimes(1);
    expect(register).toHaveBeenCalledWith("/sw.js?v=build-42", {
      scope: "/",
      updateViaCache: "none",
    });
    expect(registration).toEqual({ scope: "/" });
  });

  it("does nothing in development", async () => {
    vi.stubEnv("NODE_ENV", "development");
    installServiceWorkerApi();

    await expect(registerServiceWorker()).resolves.toBeNull();
    expect(register).not.toHaveBeenCalled();
  });

  it("does nothing in test", async () => {
    vi.stubEnv("NODE_ENV", "test");
    installServiceWorkerApi();

    await expect(registerServiceWorker()).resolves.toBeNull();
    expect(register).not.toHaveBeenCalled();
  });

  it("does nothing when the browser has no service worker support", async () => {
    vi.stubEnv("NODE_ENV", "production");
    removeServiceWorkerApi();

    await expect(registerServiceWorker()).resolves.toBeNull();
    expect(register).not.toHaveBeenCalled();
  });

  it("logs and swallows registration errors", async () => {
    vi.stubEnv("NODE_ENV", "production");
    installServiceWorkerApi();
    register.mockRejectedValue(new Error("SecurityError"));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    await expect(registerServiceWorker()).resolves.toBeNull();
    expect(warn).toHaveBeenCalledWith(
      "Service worker registration failed",
      expect.any(Error),
    );
  });
});

describe("serviceWorkerUrl", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("falls back to 'dev' when no build id was baked in", () => {
    vi.stubEnv("NEXT_PUBLIC_BUILD_ID", "");
    expect(serviceWorkerUrl()).toBe("/sw.js?v=dev");
  });

  it("encodes the build id", () => {
    expect(serviceWorkerUrl("a b/c")).toBe("/sw.js?v=a%20b%2Fc");
  });
});
