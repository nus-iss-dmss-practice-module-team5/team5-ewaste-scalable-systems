"use client";

import { HOME_PATH, LOGIN_PATH } from "@/lib/auth/config";
import { Redirecting } from "@/lib/auth/guards";
import { useSession } from "@/lib/auth/session-context";
import { replaceLocation } from "@/lib/auth/storage";
import { useEffect } from "react";

export default function IndexPage() {
  const { session, ready, offlineUser } = useSession();
  const signedIn = Boolean(session || offlineUser);

  useEffect(() => {
    if (!ready) {
      return;
    }
    replaceLocation(signedIn ? HOME_PATH : LOGIN_PATH);
  }, [ready, signedIn]);

  return <Redirecting />;
}
