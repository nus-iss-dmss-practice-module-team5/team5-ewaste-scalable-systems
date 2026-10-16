import { serviceWorkerEnabled } from "./register-sw";

const STATIC_PREFIX = "/_next/static/";

/**
 * Same-origin /_next/static/ URLs this page has already loaded: resource
 * timing entries plus <script src> and <link href> in the document.
 */
export function collectLoadedStaticUrls(
  doc: Document = document,
  perf: Pick<Performance, "getEntriesByType"> | undefined = globalThis.performance,
): string[] {
  const origin = doc.location?.origin ?? window.location.origin;
  const candidates: string[] = [];

  for (const entry of perf?.getEntriesByType("resource") ?? []) {
    candidates.push(entry.name);
  }
  doc.querySelectorAll<HTMLScriptElement>("script[src]").forEach((script) => {
    candidates.push(script.getAttribute("src") ?? "");
  });
  doc.querySelectorAll<HTMLLinkElement>("link[href]").forEach((link) => {
    candidates.push(link.getAttribute("href") ?? "");
  });

  const urls = new Set<string>();
  for (const candidate of candidates) {
    if (!candidate) {
      continue;
    }
    let url: URL;
    try {
      url = new URL(candidate, origin);
    } catch {
      continue;
    }
    if (url.origin !== origin || !url.pathname.startsWith(STATIC_PREFIX)) {
      continue;
    }
    urls.add(`${url.pathname}${url.search}`);
  }
  return [...urls];
}

/**
 * Asks the service worker to cache the static chunks this tab already loaded
 * (they were fetched before the worker controlled the page). Posts to the
 * active worker and to a waiting one, so a pending update has them too.
 * Resolves to the number of URLs sent; never throws.
 */
export async function precacheLoadedStatic(): Promise<number> {
  if (!serviceWorkerEnabled()) {
    return 0;
  }
  try {
    const registration = await navigator.serviceWorker.ready;
    // Collect after `ready`: on a first install that can take a while, and
    // chunks loaded meanwhile (e.g. /home after sign-in) should be included.
    const urls = collectLoadedStaticUrls();
    if (urls.length === 0) {
      return 0;
    }
    const workers = new Set(
      [registration.active, registration.waiting].filter(
        (worker): worker is ServiceWorker => worker != null,
      ),
    );
    for (const worker of workers) {
      worker.postMessage({ type: "PRECACHE_STATIC", urls });
    }
    return workers.size > 0 ? urls.length : 0;
  } catch (error) {
    console.warn("Could not precache loaded assets", error);
    return 0;
  }
}
