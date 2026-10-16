// @vitest-environment node
import { readFileSync } from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { describe, expect, it, vi } from "vitest";

/*
 * public/sw.js is plain JS served as-is, so it is loaded here into a vm
 * context with stubbed self, caches, fetch and clients.
 */

const ORIGIN = "https://app.test";
const swSource = readFileSync(
  path.resolve(import.meta.dirname, "../../../public/sw.js"),
  "utf8",
);

type FakeRequest = {
  url: string;
  method?: string;
  mode?: string;
  headers?: Headers;
};

type Listener = (event: Record<string, unknown>) => void;

type SwApi = {
  buildId: string;
  caches: { shell: string; static: string };
  shellUrls: string[];
  routeFor: (request: FakeRequest, origin: string) => string | null;
  filterStaticUrls: (urls: unknown, origin: string) => string[];
  extractStaticUrls: (html: string, origin: string) => string[];
};

function keyOf(input: string | { url: string }): string {
  return new URL(typeof input === "string" ? input : input.url, ORIGIN).href;
}

function withoutSearch(href: string): string {
  const url = new URL(href);
  return url.origin + url.pathname;
}

class FakeCache {
  entries = new Map<string, Response>();

  async match(
    input: string | { url: string },
    options?: { ignoreSearch?: boolean },
  ): Promise<Response | undefined> {
    const key = keyOf(input);
    for (const [stored, response] of this.entries) {
      const hit = options?.ignoreSearch
        ? withoutSearch(stored) === withoutSearch(key)
        : stored === key;
      if (hit) {
        return response.clone();
      }
    }
    return undefined;
  }

  async put(input: string | { url: string }, response: Response) {
    this.entries.set(keyOf(input), response.clone());
  }

  paths(): string[] {
    return [...this.entries.keys()].map((href) => {
      const url = new URL(href);
      return url.pathname + url.search;
    });
  }
}

class FakeCacheStorage {
  store = new Map<string, FakeCache>();

  async open(name: string) {
    if (!this.store.has(name)) {
      this.store.set(name, new FakeCache());
    }
    return this.store.get(name)!;
  }

  async keys() {
    return [...this.store.keys()];
  }

  async delete(name: string) {
    return this.store.delete(name);
  }
}

const HOME_HTML = `<!doctype html><html><head>
<link rel="stylesheet" href="/_next/static/css/app-1.css">
<script src="/_next/static/chunks/main-1.js" async></script>
</head><body><script>self.__next_f.push([1,"[\\"/_next/static/chunks/app/home/page-2.js\\"]"])</script>
<script src="https://cdn.example.com/_next/static/chunks/other.js"></script></body></html>`;

const LOGIN_HTML = `<!doctype html><html><head>
<script src="/_next/static/chunks/main-1.js" async></script>
<script src="/_next/static/chunks/app/login/page-3.js" async></script>
</head><body>login</body></html>`;

function defaultRoutes(): Record<string, () => Response> {
  const html = (body: string) => () =>
    new Response(body, { headers: { "Content-Type": "text/html" } });
  const bytes = (type: string) => () =>
    new Response("x", { headers: { "Content-Type": type } });
  return {
    "/home": html(HOME_HTML),
    "/login": html(LOGIN_HTML),
    "/": html("index"),
    "/manifest.webmanifest": bytes("application/manifest+json"),
    "/icons/icon-192.png": bytes("image/png"),
    "/icons/icon-512.png": bytes("image/png"),
    "/icons/icon-maskable-512.png": bytes("image/png"),
    "/icons/apple-touch-icon.png": bytes("image/png"),
    "/favicon.ico": bytes("image/x-icon"),
  };
}

function loadWorker(
  options: {
    swUrl?: string;
    existingCaches?: string[];
    routes?: Record<string, () => Response>;
  } = {},
) {
  const listeners: Record<string, Listener> = {};
  const caches = new FakeCacheStorage();
  for (const name of options.existingCaches ?? []) {
    void caches.open(name);
  }
  const routes = options.routes ?? defaultRoutes();
  const network = { online: true };
  const fetchMock = vi.fn(
    async (input: string | FakeRequest | Request): Promise<Response> => {
      if (!network.online) {
        throw new TypeError("Failed to fetch");
      }
      const url = new URL(typeof input === "string" ? input : input.url, ORIGIN);
      if (url.pathname.startsWith("/_next/static/")) {
        return new Response(`/* ${url.pathname} */`, {
          headers: { "Content-Type": "text/javascript" },
        });
      }
      const route = routes[url.pathname];
      return route ? route() : new Response("not found", { status: 404 });
    },
  );
  const clients = { claim: vi.fn(async () => undefined) };
  const skipWaiting = vi.fn(async () => undefined);

  const sandbox: Record<string, unknown> = {
    addEventListener: (type: string, listener: Listener) => {
      listeners[type] = listener;
    },
    location: new URL(options.swUrl ?? `${ORIGIN}/sw.js?v=build-1`),
    caches,
    fetch: fetchMock,
    clients,
    skipWaiting,
    URL,
    Request,
    Response,
    Headers,
    console,
  };
  sandbox.self = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(swSource, sandbox, { filename: "sw.js" });

  async function extendable(type: string, extra: Record<string, unknown> = {}) {
    const pending: Promise<unknown>[] = [];
    listeners[type]({
      ...extra,
      waitUntil: (promise: Promise<unknown>) => pending.push(promise),
    });
    await Promise.all(pending);
  }

  async function dispatchFetch(request: FakeRequest) {
    let responded: Promise<Response> | undefined;
    listeners.fetch({
      request: { method: "GET", mode: "cors", headers: new Headers(), ...request },
      respondWith: (promise: Promise<Response>) => {
        responded = promise;
      },
    });
    return responded === undefined ? undefined : await responded;
  }

  return {
    api: sandbox.__ewasteSw as SwApi,
    caches,
    clients,
    skipWaiting,
    fetchMock,
    network,
    install: () => extendable("install"),
    activate: () => extendable("activate"),
    message: (data: unknown) => extendable("message", { data }),
    dispatchFetch,
  };
}

const url = (p: string) => `${ORIGIN}${p}`;

describe("sw.js routing", () => {
  const { api } = loadWorker();
  const route = (request: FakeRequest) =>
    api.routeFor(
      { method: "GET", mode: "cors", headers: new Headers(), ...request },
      ORIGIN,
    );

  it.each([
    ["/home", "shell-page"],
    ["/home/", "shell-page"],
    ["/login", "shell-page"],
    ["/login?expired=1", "shell-page"],
    ["/", "page"],
    ["/somewhere-else", "page"],
  ])("navigation to %s is %s", (pathname, expected) => {
    expect(route({ url: url(pathname), mode: "navigate" })).toBe(expected);
  });

  it("caches Next static assets and shell assets", () => {
    expect(route({ url: url("/_next/static/chunks/main-1.js") })).toBe("static");
    expect(route({ url: url("/_next/static/css/app-1.css") })).toBe("static");
    expect(route({ url: url("/manifest.webmanifest") })).toBe("shell-asset");
    expect(route({ url: url("/icons/icon-192.png") })).toBe("shell-asset");
    expect(route({ url: url("/icons/icon-maskable-512.png") })).toBe(
      "shell-asset",
    );
    expect(route({ url: url("/favicon.ico") })).toBe("shell-asset");
  });

  it("never intercepts /api/ for any method or mode", () => {
    for (const method of ["GET", "POST", "PUT", "PATCH", "DELETE"]) {
      expect(route({ url: url("/api/v1/batches"), method })).toBeNull();
    }
    expect(route({ url: url("/api/v1/batches"), mode: "navigate" })).toBeNull();
    expect(route({ url: url("/api") })).toBeNull();
  });

  it("never intercepts RSC requests", () => {
    expect(
      route({
        url: url("/home"),
        headers: new Headers({ RSC: "1" }),
      }),
    ).toBeNull();
    expect(
      route({
        url: url("/home"),
        mode: "navigate",
        headers: new Headers({ RSC: "1" }),
      }),
    ).toBeNull();
    expect(route({ url: url("/home?_rsc=abc12") })).toBeNull();
    expect(route({ url: url("/login?_rsc=abc12"), mode: "navigate" })).toBeNull();
  });

  it("never touches non-GET requests", () => {
    expect(route({ url: url("/home"), method: "POST", mode: "navigate" })).toBeNull();
    expect(
      route({ url: url("/_next/static/chunks/main-1.js"), method: "HEAD" }),
    ).toBeNull();
  });

  it("ignores cross-origin and unknown same-origin subresources", () => {
    expect(
      route({ url: "https://cdn.example.com/_next/static/chunks/main-1.js" }),
    ).toBeNull();
    expect(
      route({ url: "https://other.test/home", mode: "navigate" }),
    ).toBeNull();
    expect(route({ url: url("/home") })).toBeNull();
    expect(route({ url: url("/next.svg") })).toBeNull();
  });
});

describe("sw.js cache naming", () => {
  it("names caches after the ?v= build id in its registration URL", () => {
    const { api } = loadWorker({ swUrl: `${ORIGIN}/sw.js?v=abc123` });

    expect(api.buildId).toBe("abc123");
    expect(api.caches).toEqual({ shell: "shell-abc123", static: "static-abc123" });
  });

  it("falls back to 'dev' without a build id", () => {
    const { api } = loadWorker({ swUrl: `${ORIGIN}/sw.js` });

    expect(api.caches).toEqual({ shell: "shell-dev", static: "static-dev" });
  });
});

describe("sw.js install", () => {
  it("precaches the shell with cache: reload and does not skip waiting", async () => {
    const worker = loadWorker();

    await worker.install();

    const shell = await worker.caches.open("shell-build-1");
    expect(shell.paths().sort()).toEqual(
      [
        "/home",
        "/login",
        "/manifest.webmanifest",
        "/icons/icon-192.png",
        "/icons/icon-512.png",
        "/icons/icon-maskable-512.png",
        "/icons/apple-touch-icon.png",
        "/favicon.ico",
      ].sort(),
    );
    const pageRequests = worker.fetchMock.mock.calls
      .map(([input]) => input)
      .filter(
        (input): input is Request =>
          typeof input !== "string" && input.url === url("/home"),
      );
    expect(pageRequests[0].cache).toBe("reload");
    expect(worker.skipWaiting).not.toHaveBeenCalled();
  });

  it("also caches the static chunks the shell documents reference", async () => {
    const worker = loadWorker();

    await worker.install();

    const staticCache = await worker.caches.open("static-build-1");
    expect(staticCache.paths().sort()).toEqual(
      [
        "/_next/static/chunks/app/home/page-2.js",
        "/_next/static/chunks/app/login/page-3.js",
        "/_next/static/chunks/main-1.js",
        "/_next/static/css/app-1.css",
      ].sort(),
    );
  });

  it("fails the install when a shell document cannot be fetched", async () => {
    const routes = defaultRoutes();
    delete routes["/login"];
    const worker = loadWorker({ routes });

    await expect(worker.install()).rejects.toThrow(/\/login/);
  });
});

describe("sw.js activate", () => {
  it("deletes caches from other builds and keeps unrelated caches", async () => {
    const worker = loadWorker({
      existingCaches: [
        "shell-old",
        "static-old",
        "shell-build-1",
        "static-build-1",
        "ewaste-offline-data",
      ],
    });

    await worker.activate();

    expect((await worker.caches.keys()).sort()).toEqual(
      ["ewaste-offline-data", "shell-build-1", "static-build-1"].sort(),
    );
    // An update: old tabs are already gone, no takeover.
    expect(worker.clients.claim).not.toHaveBeenCalled();
  });

  it("claims clients only on the very first install", async () => {
    const worker = loadWorker({ existingCaches: ["shell-build-1"] });

    await worker.activate();

    expect(worker.clients.claim).toHaveBeenCalledTimes(1);
  });
});

describe("sw.js fetch handling", () => {
  it("lets /api/ requests reach the network untouched", async () => {
    const worker = loadWorker();

    for (const method of ["GET", "POST"]) {
      const response = await worker.dispatchFetch({
        url: url("/api/v1/batches"),
        method,
      });
      expect(response).toBeUndefined();
    }
    expect(worker.fetchMock).not.toHaveBeenCalled();
    expect(await worker.caches.keys()).toEqual([]);
  });

  it("lets RSC requests through without caching", async () => {
    const worker = loadWorker();

    expect(
      await worker.dispatchFetch({
        url: url("/home?_rsc=x1"),
        headers: new Headers({ RSC: "1" }),
      }),
    ).toBeUndefined();
    expect(worker.fetchMock).not.toHaveBeenCalled();
  });

  it("serves /home and /login network-first and refreshes the shell cache", async () => {
    const worker = loadWorker();
    await worker.install();
    const shell = await worker.caches.open("shell-build-1");
    await shell.put(url("/home"), new Response("stale home"));

    const response = await worker.dispatchFetch({
      url: url("/home"),
      mode: "navigate",
    });

    expect(await response?.text()).toBe(HOME_HTML);
    await vi.waitFor(async () => {
      expect(await (await shell.match(url("/home")))?.text()).toBe(HOME_HTML);
    });
  });

  it("falls back to the cached shell document when offline, ignoring the query", async () => {
    const worker = loadWorker();
    await worker.install();
    worker.network.online = false;

    const login = await worker.dispatchFetch({
      url: url("/login?expired=1"),
      mode: "navigate",
    });
    const home = await worker.dispatchFetch({
      url: url("/home"),
      mode: "navigate",
    });

    expect(await login?.text()).toBe(LOGIN_HTML);
    expect(await home?.text()).toBe(HOME_HTML);
  });

  it("serves the cached shell document when the server answers 5xx", async () => {
    const routes = defaultRoutes();
    const worker = loadWorker({ routes });
    await worker.install();
    routes["/home"] = () => new Response("bad gateway", { status: 502 });

    const response = await worker.dispatchFetch({
      url: url("/home"),
      mode: "navigate",
    });

    expect(response?.status).toBe(200);
    expect(await response?.text()).toBe(HOME_HTML);
    const shell = await worker.caches.open("shell-build-1");
    expect(await (await shell.match(url("/home")))?.text()).toBe(HOME_HTML);
  });

  it("passes a 5xx shell response through when nothing is cached", async () => {
    const routes = defaultRoutes();
    routes["/login"] = () => new Response("unavailable", { status: 503 });
    const worker = loadWorker({ routes });

    const response = await worker.dispatchFetch({
      url: url("/login"),
      mode: "navigate",
    });

    expect(response?.status).toBe(503);
  });

  it("does not store a shell document fetched with a query string", async () => {
    const routes = defaultRoutes();
    const worker = loadWorker({ routes });
    await worker.install();
    routes["/login"] = () =>
      new Response("expired banner", {
        headers: { "Content-Type": "text/html" },
      });

    const expired = await worker.dispatchFetch({
      url: url("/login?expired=1"),
      mode: "navigate",
    });
    expect(await expired?.text()).toBe("expired banner");
    // Give any stray background cache write a chance to land.
    await new Promise((resolve) => setTimeout(resolve, 10));

    worker.network.online = false;
    const offline = await worker.dispatchFetch({
      url: url("/login"),
      mode: "navigate",
    });
    expect(await offline?.text()).toBe(LOGIN_HTML);
  });

  it("falls back to the cached /home for other navigations when offline", async () => {
    const worker = loadWorker();
    await worker.install();
    worker.network.online = false;

    const response = await worker.dispatchFetch({
      url: url("/"),
      mode: "navigate",
    });

    expect(await response?.text()).toBe(HOME_HTML);
  });

  it("fails offline navigation when nothing is cached", async () => {
    const worker = loadWorker();
    worker.network.online = false;

    await expect(
      worker.dispatchFetch({ url: url("/login"), mode: "navigate" }),
    ).rejects.toThrow("Failed to fetch");
  });

  it("serves /_next/static/ cache-first", async () => {
    const worker = loadWorker();
    const chunk = url("/_next/static/chunks/lazy-9.js");

    const first = await worker.dispatchFetch({ url: chunk });
    expect(await first?.text()).toBe("/* /_next/static/chunks/lazy-9.js */");
    await vi.waitFor(async () => {
      const staticCache = await worker.caches.open("static-build-1");
      expect(staticCache.paths()).toContain("/_next/static/chunks/lazy-9.js");
    });

    worker.network.online = false;
    const second = await worker.dispatchFetch({ url: chunk });
    expect(await second?.text()).toBe("/* /_next/static/chunks/lazy-9.js */");
  });

  it("serves the manifest and icons from the shell cache offline", async () => {
    const worker = loadWorker();
    await worker.install();
    worker.network.online = false;

    const manifest = await worker.dispatchFetch({
      url: url("/manifest.webmanifest"),
    });

    expect(manifest?.headers.get("Content-Type")).toBe(
      "application/manifest+json",
    );
  });
});

describe("sw.js PRECACHE_STATIC message", () => {
  it("adds only same-origin /_next/static/ URLs to the static cache", async () => {
    const worker = loadWorker();

    await worker.message({
      type: "PRECACHE_STATIC",
      urls: [
        "/_next/static/chunks/a.js",
        `${ORIGIN}/_next/static/css/b.css`,
        "/_next/static/chunks/a.js",
        "https://cdn.example.com/_next/static/chunks/c.js",
        "/api/v1/batches",
        "/home",
        42,
      ],
    });

    const staticCache = await worker.caches.open("static-build-1");
    expect(staticCache.paths().sort()).toEqual(
      ["/_next/static/chunks/a.js", "/_next/static/css/b.css"].sort(),
    );
    expect(await worker.caches.keys()).toEqual(["static-build-1"]);
  });

  it("ignores other messages", async () => {
    const worker = loadWorker();

    await worker.message({ type: "SOMETHING_ELSE", urls: ["/_next/static/a.js"] });
    await worker.message(null);

    expect(await worker.caches.keys()).toEqual([]);
  });
});
