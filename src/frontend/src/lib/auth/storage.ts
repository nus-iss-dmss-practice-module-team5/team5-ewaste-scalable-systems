import type { Session } from "./types";

const STORAGE_KEY = "ewaste.sprint1.session";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isSession(value: unknown): value is Session {
  if (!isRecord(value) || !isRecord(value.user) || !isRecord(value.tokens)) {
    return false;
  }
  return (
    typeof value.user.email === "string" &&
    typeof value.user.role === "string" &&
    typeof value.tokens.accessToken === "string" &&
    typeof value.tokens.refreshToken === "string" &&
    typeof value.accessExpiresAt === "number" &&
    typeof value.refreshExpiresAt === "number"
  );
}

export function readStoredSession(): Session | null {
  if (typeof window === "undefined") {
    return null;
  }
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    if (!raw) {
      return null;
    }
    const parsed: unknown = JSON.parse(raw);
    return isSession(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

export function writeStoredSession(session: Session | null): void {
  if (typeof window === "undefined") {
    return;
  }
  try {
    if (session) {
      window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(session));
    } else {
      window.sessionStorage.removeItem(STORAGE_KEY);
    }
  } catch {
    // Ignore quota / private-mode failures; in-memory session still works.
  }
}

export function replaceLocation(path: string): void {
  if (typeof window === "undefined") {
    return;
  }
  const current = `${window.location.pathname}${window.location.search}`;
  if (current !== path) {
    window.location.replace(path);
  }
}
