// navigator.onLine is only a hint: false reliably means no connection, true
// does not prove the API is reachable.

export function isBrowserOffline(): boolean {
  return typeof navigator !== "undefined" && navigator.onLine === false;
}

/** For useSyncExternalStore: calls back on every online/offline event. */
export function subscribeToNetwork(onChange: () => void): () => void {
  window.addEventListener("online", onChange);
  window.addEventListener("offline", onChange);
  return () => {
    window.removeEventListener("online", onChange);
    window.removeEventListener("offline", onChange);
  };
}

/** Server render: assume online so markup matches the first client render. */
export function serverNetworkSnapshot(): boolean {
  return false;
}
