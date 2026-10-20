"use client";

import { useSession } from "@/lib/auth/session-context";
import { precacheLoadedStatic } from "@/lib/offline/precache";
import { registerServiceWorker } from "@/lib/offline/register-sw";
import { useEffect, useRef } from "react";

/**
 * Registers the service worker on mount and, once per tab after sign-in, asks
 * it to cache the static chunks already loaded. Renders nothing.
 */
export function PwaLifecycle() {
  const { session } = useSession();
  const precached = useRef(false);
  const signedIn = session !== null;

  useEffect(() => {
    void registerServiceWorker();
  }, []);

  useEffect(() => {
    if (!signedIn || precached.current) {
      return;
    }
    precached.current = true;
    void precacheLoadedStatic();
  }, [signedIn]);

  return null;
}
