import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { collectLoadedStaticUrls, precacheLoadedStatic } from "./precache";

function resourceEntries(names: string[]) {
  return {
    getEntriesByType: vi.fn((type: string) =>
      type === "resource"
        ? (names.map((name) => ({ name })) as PerformanceEntry[])
        : [],
    ),
  };
}

function addElement(tag: "script" | "link", attr: "src" | "href", value: string) {
  const element = document.createElement(tag);
  element.setAttribute(attr, value);
  document.head.appendChild(element);
}

describe("collectLoadedStaticUrls", () => {
  afterEach(() => {
    document.head.innerHTML = "";
  });

  it("keeps only same-origin /_next/static/ URLs from resources and tags", () => {
    const origin = window.location.origin;
    addElement("script", "src", "/_next/static/chunks/app/home/page-1.js");
    addElement("link", "href", "/_next/static/css/app.css");
    addElement("link", "href", "/manifest.webmanifest");
    addElement("script", "src", "https://cdn.example.com/_next/static/x.js");

    const urls = collectLoadedStaticUrls(
      document,
      resourceEntries([
        `${origin}/_next/static/chunks/main-abc.js`,
        `${origin}/_next/static/chunks/main-abc.js#dup`,
        `${origin}/api/v1/batches`,
        `${origin}/home?_rsc=1`,
        "https://fonts.example.com/_next/static/media/font.woff2",
      ]),
    );

    expect(urls.sort()).toEqual(
      [
        "/_next/static/chunks/main-abc.js",
        "/_next/static/chunks/app/home/page-1.js",
        "/_next/static/css/app.css",
      ].sort(),
    );
  });
});

describe("precacheLoadedStatic", () => {
  const activePost = vi.fn();
  const waitingPost = vi.fn();

  beforeEach(() => {
    activePost.mockReset();
    waitingPost.mockReset();
    addElement("script", "src", "/_next/static/chunks/main-abc.js");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    Reflect.deleteProperty(navigator, "serviceWorker");
    document.head.innerHTML = "";
  });

  function installReadyRegistration(registration: object) {
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: { ready: Promise.resolve(registration) },
    });
  }

  it("posts PRECACHE_STATIC to the active and waiting workers", async () => {
    vi.stubEnv("NODE_ENV", "production");
    installReadyRegistration({
      active: { postMessage: activePost },
      waiting: { postMessage: waitingPost },
    });

    const sent = await precacheLoadedStatic();

    expect(sent).toBeGreaterThan(0);
    const message = activePost.mock.calls[0][0];
    expect(message.type).toBe("PRECACHE_STATIC");
    expect(message.urls).toContain("/_next/static/chunks/main-abc.js");
    expect(
      message.urls.every((url: string) => url.startsWith("/_next/static/")),
    ).toBe(true);
    expect(waitingPost).toHaveBeenCalledWith(message);
  });

  it("does nothing outside production", async () => {
    vi.stubEnv("NODE_ENV", "development");
    installReadyRegistration({ active: { postMessage: activePost } });

    await expect(precacheLoadedStatic()).resolves.toBe(0);
    expect(activePost).not.toHaveBeenCalled();
  });

  it("does nothing without service worker support", async () => {
    vi.stubEnv("NODE_ENV", "production");

    await expect(precacheLoadedStatic()).resolves.toBe(0);
  });

  it("does nothing when no worker is active yet", async () => {
    vi.stubEnv("NODE_ENV", "production");
    installReadyRegistration({ active: null, waiting: null });

    await expect(precacheLoadedStatic()).resolves.toBe(0);
  });
});
