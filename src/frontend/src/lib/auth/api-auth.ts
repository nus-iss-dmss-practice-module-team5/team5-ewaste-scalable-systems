import axios from "axios";
import { api } from "./api-client";
import { userFromAccessToken } from "./jwt";
import {
  isAuthError,
  sessionFromTokens,
  type AuthErrorBody,
  type Session,
  type TokenResponse,
} from "./types";

const NETWORK_ERROR: AuthErrorBody = {
  code: "AUTH_SERVICE_UNAVAILABLE",
  message: "Could not reach the sign-in service. Try again.",
  correlationId: "corr-network",
};

function credentialsMessage(body: AuthErrorBody): AuthErrorBody {
  if (body.code === "AUTH_INVALID_CREDENTIALS") {
    return { ...body, message: "Invalid email or password" };
  }
  return body;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function parseErrorBody(data: unknown): AuthErrorBody | null {
  if (
    !isRecord(data) ||
    typeof data.code !== "string" ||
    typeof data.message !== "string" ||
    typeof data.correlation_id !== "string"
  ) {
    return null;
  }
  return {
    code: data.code,
    message: data.message,
    correlationId: data.correlation_id,
  };
}

function toAuthError(error: unknown): AuthErrorBody {
  if (axios.isAxiosError(error)) {
    const body = parseErrorBody(error.response?.data);
    return body ? credentialsMessage(body) : NETWORK_ERROR;
  }
  if (isAuthError(error)) {
    return credentialsMessage(error);
  }
  return NETWORK_ERROR;
}

function parseTokenResponse(value: unknown): TokenResponse | null {
  if (
    !isRecord(value) ||
    typeof value.access_token !== "string" ||
    typeof value.refresh_token !== "string" ||
    value.token_type !== "Bearer" ||
    typeof value.expires_in !== "number" ||
    typeof value.refresh_expires_in !== "number"
  ) {
    return null;
  }
  return {
    accessToken: value.access_token,
    refreshToken: value.refresh_token,
    tokenType: "Bearer",
    expiresIn: value.expires_in,
    refreshExpiresIn: value.refresh_expires_in,
  };
}

function sessionFromResponse(
  data: unknown,
  fallbacks?: { email?: string; name?: string; organisationName?: string },
): Session {
  const tokens = parseTokenResponse(data);
  if (!tokens) {
    throw NETWORK_ERROR;
  }
  try {
    return sessionFromTokens(
      userFromAccessToken(tokens.accessToken, fallbacks),
      tokens,
    );
  } catch {
    const error: AuthErrorBody = {
      code: "AUTH_INVALID_SESSION",
      message: "Session expired. Please sign in again.",
      correlationId: "corr-token-claims",
    };
    throw error;
  }
}

export async function apiLogin(
  email: string,
  password: string,
): Promise<Session> {
  const trimmedEmail = email.trim();
  try {
    const response = await api.post<unknown>("/api/v1/auth/login", {
      email: trimmedEmail,
      password,
    });
    return sessionFromResponse(response.data, { email: trimmedEmail });
  } catch (error) {
    throw toAuthError(error);
  }
}

export async function apiRefresh(
  refreshToken: string,
  previous?: { email: string; name: string; organisationName: string },
): Promise<Session> {
  try {
    const response = await api.post<unknown>("/api/v1/auth/refresh", {
      refresh_token: refreshToken,
    });
    return sessionFromResponse(response.data, previous);
  } catch (error) {
    throw toAuthError(error);
  }
}

export async function apiLogout(accessToken: string): Promise<void> {
  try {
    await api.post("/api/v1/auth/logout", undefined, {
      headers: { Authorization: `Bearer ${accessToken}` },
    });
  } catch (error) {
    throw toAuthError(error);
  }
}
