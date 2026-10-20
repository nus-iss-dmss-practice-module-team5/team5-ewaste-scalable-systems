import { apiLogin, apiLogout, apiRefresh } from "./api-auth";
import type { Session, SessionUser } from "./types";

export async function login(email: string, password: string): Promise<Session> {
  return apiLogin(email, password);
}

export async function refreshSession(
  refreshToken: string,
  previous?: SessionUser,
): Promise<Session> {
  return apiRefresh(refreshToken, previous);
}

export async function logoutSession(accessToken: string): Promise<void> {
  return apiLogout(accessToken);
}
