/**
 * Service worker registration (EWSS-22). The worker lives in public/sw.js and
 * reads the build id from its own URL to name its caches.
 */

export function serviceWorkerUrl(
  buildId: string = process.env.NEXT_PUBLIC_BUILD_ID || "dev",
): string {
  return `/sw.js?v=${encodeURIComponent(buildId)}`;
}

/** True when this environment should run the service worker at all. */
export function serviceWorkerEnabled(): boolean {
  return (
    process.env.NODE_ENV === "production" &&
    typeof window !== "undefined" &&
    typeof navigator !== "undefined" &&
    "serviceWorker" in navigator
  );
}

/**
 * Registers /sw.js?v=<build> with scope "/". Production only: in `next dev`
 * a worker would cache stale dev chunks. Never throws.
 */
export async function registerServiceWorker(): Promise<ServiceWorkerRegistration | null> {
  if (!serviceWorkerEnabled()) {
    return null;
  }
  try {
    return await navigator.serviceWorker.register(serviceWorkerUrl(), {
      scope: "/",
      // Always fetch sw.js from the network so a new build is noticed.
      updateViaCache: "none",
    });
  } catch (error) {
    console.warn("Service worker registration failed", error);
    return null;
  }
}
