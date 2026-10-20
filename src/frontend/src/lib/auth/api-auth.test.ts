import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api-client";
import { apiLogin, apiRefresh } from "./api-auth";
import { AxiosError, AxiosHeaders } from "axios";

vi.mock("./api-client", () => ({
  api: {
    post: vi.fn(),
  },
}));

const post = vi.mocked(api.post);

function jwtWithPayload(payload: Record<string, unknown>): string {
  const header = Buffer.from(
    JSON.stringify({ alg: "none", typ: "JWT" }),
  ).toString("base64url");
  const body = Buffer.from(JSON.stringify(payload)).toString("base64url");
  return `${header}.${body}.sig`;
}

function workflowTokenResponse() {
  return {
    access_token: jwtWithPayload({
      sub: "USR-003",
      role: "DONOR",
      org: "DON-001",
      sid: "session-1",
      typ: "access",
    }),
    refresh_token: "refresh-token",
    token_type: "Bearer",
    expires_in: 900,
    refresh_expires_in: 86400,
  };
}

function axiosError(status: number, data: unknown) {
  return new AxiosError("request failed", undefined, undefined, undefined, {
    status,
    statusText: "Error",
    data,
    headers: new AxiosHeaders(),
    config: { headers: new AxiosHeaders() },
  });
}

describe("apiLogin", () => {
  beforeEach(() => {
    post.mockReset();
  });

  it("maps a snake_case token response onto a session", async () => {
    const tokens = workflowTokenResponse();
    post.mockResolvedValue({ data: tokens });

    const session = await apiLogin(" donor1@ewaste.test ", "secret");

    expect(post).toHaveBeenCalledWith("/api/v1/auth/login", {
      email: "donor1@ewaste.test",
      password: "secret",
    });
    expect(session.user.role).toBe("DONOR");
    expect(session.user.email).toBe("donor1@ewaste.test");
    expect(session.user.name).toBe("Green Office Donor");
    expect(session.user.id).toBe("USR-003");
    expect(session.tokens).toEqual({
      accessToken: tokens.access_token,
      refreshToken: "refresh-token",
      tokenType: "Bearer",
      expiresIn: 900,
      refreshExpiresIn: 86400,
    });
  });

  it("rejects a camelCase token response", async () => {
    const tokens = workflowTokenResponse();
    post.mockResolvedValue({
      data: {
        accessToken: tokens.access_token,
        refreshToken: tokens.refresh_token,
        tokenType: "Bearer",
        expiresIn: 900,
        refreshExpiresIn: 86400,
      },
    });

    await expect(
      apiLogin("donor1@ewaste.test", "secret"),
    ).rejects.toMatchObject({ code: "AUTH_SERVICE_UNAVAILABLE" });
  });

  it("normalises invalid credentials and keeps the correlation ID", async () => {
    post.mockRejectedValue(
      axiosError(401, {
        code: "AUTH_INVALID_CREDENTIALS",
        message: "invalid credentials",
        correlation_id: "corr-login-001",
      }),
    );

    await expect(apiLogin("donor1@ewaste.test", "wrong")).rejects.toMatchObject(
      {
        code: "AUTH_INVALID_CREDENTIALS",
        message: "Invalid email or password",
        correlationId: "corr-login-001",
      },
    );
  });

  it("does not treat an Axios network code as an auth error body", async () => {
    const failure = axiosError(500, { error: "boom" });
    failure.code = "ERR_BAD_RESPONSE";
    post.mockRejectedValue(failure);

    await expect(
      apiLogin("donor1@ewaste.test", "secret"),
    ).rejects.toMatchObject({
      code: "AUTH_SERVICE_UNAVAILABLE",
    });
  });
});

describe("apiRefresh", () => {
  beforeEach(() => {
    post.mockReset();
  });

  it("posts the refresh token as refresh_token", async () => {
    post.mockResolvedValue({ data: workflowTokenResponse() });
    const session = await apiRefresh("old-refresh", {
      email: "donor1@ewaste.test",
      name: "Green Office Donor",
      organisationName: "DON-001",
    });
    expect(post).toHaveBeenCalledWith("/api/v1/auth/refresh", {
      refresh_token: "old-refresh",
    });
    expect(session.user.role).toBe("DONOR");
  });
});
