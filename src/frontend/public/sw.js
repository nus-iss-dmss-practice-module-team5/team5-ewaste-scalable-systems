/*
 * E-Waste Platform service worker: installable app with a cached shell
 * (EWSS-22, design in EWSS-15). Plain JS, served as-is from /public.
 *
 * Registered by src/lib/offline/register-sw.ts as /sw.js?v=<BUILD_ID> with
 * scope "/". The build id names the caches, so every deploy gets fresh ones.
 *
 * Caches
 *   shell-<build>   /home, /login documents, manifest, icons, favicon
 *   static-<build>  /_next/static/* (content-hashed JS, CSS, fonts)
 *
 * Lifecycle
 *   - install precaches the shell. There is deliberately NO skipWaiting(): a
 *     new version waits until every tab/app window is closed, so the UI never
 *     swaps underneath someone mid-shift. It takes over on the next app open.
 *   - activate deletes shell-* / static-* caches from other builds. It calls
 *     clients.claim() only on the very first install (no older caches), so the
 *     page that registered the worker is controlled straight away and its
 *     lazily loaded chunks land in the static cache. On an update there is
 *     nothing to claim from: activation only happens once old tabs are gone.
 *
 * Routing (see routeFor): same-origin GET only. /api/* (any method) and
 * Next.js RSC payloads (RSC: 1 header or ?_rsc=) are never intercepted, so
 * API data is never cached or queued here. Non-GET is never touched.
 */
(function (sw) {
  "use strict";

  var SHELL_PAGES = ["/home", "/login"];
  var SHELL_ASSETS = [
    "/manifest.webmanifest",
    "/icons/icon-192.png",
    "/icons/icon-512.png",
    "/icons/icon-maskable-512.png",
    "/icons/apple-touch-icon.png",
    "/favicon.ico",
  ];
  var STATIC_PREFIX = "/_next/static/";
  var OWNED_CACHE = /^(shell|static)-/;
  var OFFLINE_FALLBACK = "/home";
  var MAX_STATIC_URLS = 300;

  function readBuildId(location) {
    try {
      return new URL(String(location)).searchParams.get("v") || "dev";
    } catch {
      return "dev";
    }
  }

  function cacheNamesFor(buildId) {
    return { shell: "shell-" + buildId, static: "static-" + buildId };
  }

  function normalisePath(pathname) {
    return pathname.length > 1 && pathname.endsWith("/")
      ? pathname.slice(0, -1)
      : pathname;
  }

  function isApiPath(pathname) {
    return pathname === "/api" || pathname.startsWith("/api/");
  }

  function isRscRequest(url, headers) {
    if (url.searchParams.has("_rsc")) return true;
    return Boolean(headers && headers.get && headers.get("RSC") === "1");
  }

  /**
   * Pure routing decision. Returns null for "do not intercept" or one of
   * "shell-page" | "page" | "static" | "shell-asset".
   */
  function routeFor(request, origin) {
    if (!request || request.method !== "GET") return null;
    var url;
    try {
      url = new URL(request.url);
    } catch {
      return null;
    }
    if (url.origin !== origin) return null;
    if (isApiPath(url.pathname)) return null;
    if (isRscRequest(url, request.headers)) return null;

    if (request.mode === "navigate") {
      return SHELL_PAGES.indexOf(normalisePath(url.pathname)) !== -1
        ? "shell-page"
        : "page";
    }
    if (url.pathname.startsWith(STATIC_PREFIX)) return "static";
    if (SHELL_ASSETS.indexOf(url.pathname) !== -1) return "shell-asset";
    return null;
  }

  /** Same-origin /_next/static/ URLs only, absolute, deduplicated. */
  function filterStaticUrls(urls, origin) {
    var seen = [];
    if (!Array.isArray(urls)) return seen;
    for (var i = 0; i < urls.length && seen.length < MAX_STATIC_URLS; i++) {
      if (typeof urls[i] !== "string") continue;
      var url;
      try {
        url = new URL(urls[i], origin);
      } catch {
        continue;
      }
      if (url.origin !== origin) continue;
      if (!url.pathname.startsWith(STATIC_PREFIX)) continue;
      if (url.searchParams.has("_rsc")) continue;
      var href = url.origin + url.pathname + url.search;
      if (seen.indexOf(href) === -1) seen.push(href);
    }
    return seen;
  }

  /** /_next/static/ references inside a shell document (scripts, CSS, fonts). */
  function extractStaticUrls(html, origin) {
    // Keep any absolute origin in the match so CDN URLs are filtered out.
    var pattern = /(?:https?:\/\/[^"'\s<>\\/]+)?\/_next\/static\/[^"'\s<>\\)]+/g;
    var matches = String(html).match(pattern) || [];
    return filterStaticUrls(matches, origin);
  }

  var origin = new URL(String(sw.location)).origin;
  var buildId = readBuildId(sw.location);
  var CACHES = cacheNamesFor(buildId);

  function absolute(path) {
    return new URL(path, origin).href;
  }

  // A redirected response cannot answer a navigation; store a plain copy.
  function storable(response) {
    if (!response.redirected) return Promise.resolve(response);
    return response.blob().then(function (body) {
      return new Response(body, {
        status: response.status,
        statusText: response.statusText,
        headers: response.headers,
      });
    });
  }

  function addStaticUrls(urls) {
    var wanted = filterStaticUrls(urls, origin);
    return caches.open(CACHES.static).then(function (cache) {
      return Promise.all(
        wanted.map(function (href) {
          return cache.match(href).then(function (hit) {
            if (hit) return false;
            return fetch(href, { credentials: "same-origin" }).then(
              function (response) {
                if (!response.ok) return false;
                return cache.put(href, response).then(function () {
                  return true;
                });
              },
            );
          }).catch(function () {
            return false;
          });
        }),
      ).then(function (added) {
        return added.filter(Boolean).length;
      });
    });
  }

  function precacheShell() {
    var shellHtml = [];
    return caches
      .open(CACHES.shell)
      .then(function (cache) {
        var paths = SHELL_PAGES.concat(SHELL_ASSETS);
        return Promise.all(
          paths.map(function (path) {
            var href = absolute(path);
            return fetch(new Request(href, {
              cache: "reload",
              credentials: "same-origin",
            })).then(function (response) {
              if (!response.ok) {
                throw new Error("precache " + path + ": HTTP " + response.status);
              }
              var isPage = SHELL_PAGES.indexOf(path) !== -1;
              var text = isPage
                ? response.clone().text().then(function (body) {
                    shellHtml.push(body);
                  })
                : Promise.resolve();
              return text
                .then(function () {
                  return storable(response);
                })
                .then(function (copy) {
                  return cache.put(href, copy);
                });
            });
          }),
        );
      })
      .then(function () {
        // Best effort: the chunks the new shell documents reference, so the
        // next (possibly offline) open can render without the network.
        var urls = [];
        shellHtml.forEach(function (html) {
          urls = urls.concat(extractStaticUrls(html, origin));
        });
        return addStaticUrls(urls).catch(function () {
          return 0;
        });
      });
  }

  function activate() {
    return caches.keys().then(function (keys) {
      var stale = keys.filter(function (key) {
        return (
          OWNED_CACHE.test(key) && key !== CACHES.shell && key !== CACHES.static
        );
      });
      var firstInstall = stale.length === 0;
      return Promise.all(
        stale.map(function (key) {
          return caches.delete(key);
        }),
      ).then(function () {
        return firstInstall ? sw.clients.claim() : undefined;
      });
    });
  }

  function matchShell(path) {
    return caches.open(CACHES.shell).then(function (cache) {
      return cache.match(absolute(path), { ignoreSearch: true });
    });
  }

  function networkFirstShellPage(request) {
    var url = new URL(request.url);
    var path = normalisePath(url.pathname);
    return fetch(request).then(
      function (response) {
        // Only the plain document is stored: /login?expired=1 renders an
        // "expired" banner that must not appear on a later offline /login.
        if (response.ok && !response.redirected && url.search === "") {
          var copy = response.clone();
          caches
            .open(CACHES.shell)
            .then(function (cache) {
              return cache.put(absolute(path), copy);
            })
            .catch(function () {});
        }
        if (response.ok) return response;
        // A server error (5xx) is worse than the last good shell, which can
        // still show saved work. Other statuses (e.g. 404) pass through.
        if (response.status < 500) return response;
        return matchShell(path).then(function (cached) {
          return cached || response;
        });
      },
      function (err) {
        return matchShell(path).then(function (cached) {
          if (cached) return cached;
          throw err;
        });
      },
    );
  }

  function networkWithHomeFallback(request) {
    return fetch(request).catch(function (err) {
      return matchShell(OFFLINE_FALLBACK).then(function (cached) {
        if (cached) return cached;
        throw err;
      });
    });
  }

  function cacheFirst(cacheName, request, matchOptions) {
    return caches.open(cacheName).then(function (cache) {
      return cache.match(request, matchOptions).then(function (hit) {
        if (hit) return hit;
        return fetch(request).then(function (response) {
          if (response.ok) {
            cache.put(request, response.clone()).catch(function () {});
          }
          return response;
        });
      });
    });
  }

  function respond(route, request) {
    switch (route) {
      case "shell-page":
        return networkFirstShellPage(request);
      case "page":
        return networkWithHomeFallback(request);
      case "static":
        return cacheFirst(CACHES.static, request);
      case "shell-asset":
        return cacheFirst(CACHES.shell, request, { ignoreSearch: true });
      default:
        return fetch(request);
    }
  }

  sw.addEventListener("install", function (event) {
    event.waitUntil(precacheShell());
  });

  sw.addEventListener("activate", function (event) {
    event.waitUntil(activate());
  });

  sw.addEventListener("fetch", function (event) {
    var route = routeFor(event.request, origin);
    if (!route) return; // Let the browser handle it (API, RSC, non-GET, ...).
    event.respondWith(respond(route, event.request));
  });

  sw.addEventListener("message", function (event) {
    var data = event.data;
    if (!data || data.type !== "PRECACHE_STATIC") return;
    event.waitUntil(addStaticUrls(data.urls));
  });

  // Exposed for unit tests (loaded into a vm context) and DevTools debugging.
  sw.__ewasteSw = {
    buildId: buildId,
    caches: CACHES,
    shellUrls: SHELL_PAGES.concat(SHELL_ASSETS),
    routeFor: routeFor,
    filterStaticUrls: filterStaticUrls,
    extractStaticUrls: extractStaticUrls,
    cacheNamesFor: cacheNamesFor,
    readBuildId: readBuildId,
  };
})(self);
