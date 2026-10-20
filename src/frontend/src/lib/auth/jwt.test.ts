import { describe, expect, it } from "vitest";
import { userFromAccessToken } from "./jwt";

function jwtWithPayload(payload: Record<string, unknown>): string {
  const header = Buffer.from(
    JSON.stringify({ alg: "none", typ: "JWT" }),
  ).toString("base64url");
  const body = Buffer.from(JSON.stringify(payload)).toString("base64url");
  return `${header}.${body}.sig`;
}

describe("userFromAccessToken", () => {
  it("maps backend JWTs that omit email and use org / SYSTEM_ADMIN", () => {
    const donor = jwtWithPayload({
      sub: "USR-003",
      role: "DONOR",
      org: "DON-001",
      sid: "session-1",
      typ: "access",
    });
    expect(userFromAccessToken(donor, { email: "donor1@ewaste.test" })).toEqual(
      {
        id: "USR-003",
        email: "donor1@ewaste.test",
        name: "Green Office Donor",
        organisationId: "DON-001",
        organisationName: "DON-001",
        role: "DONOR",
      },
    );

    const admin = jwtWithPayload({
      sub: "USR-001",
      role: "SYSTEM_ADMIN",
      org: "PLATFORM",
    });
    expect(
      userFromAccessToken(admin, { email: "admin@ewaste.test" }).role,
    ).toBe("ADMIN");
  });

  it("ignores camelCase claims the backend does not issue", () => {
    const token = jwtWithPayload({
      userId: "usr-donor-001",
      role: "DONOR",
      organisationId: "org-donor-001",
      organisationName: "Campus Labs",
    });
    expect(() =>
      userFromAccessToken(token, { email: "donor1@ewaste.test" }),
    ).toThrow(/subject/);

    const withSub = jwtWithPayload({
      sub: "USR-003",
      role: "DONOR",
      organisationId: "org-donor-001",
      organisationName: "Campus Labs",
    });
    expect(
      userFromAccessToken(withSub, { email: "donor1@ewaste.test" }),
    ).toMatchObject({ organisationId: "", organisationName: "DON-001" });
  });

  it("rejects a token that has no role", () => {
    const token = jwtWithPayload({ sub: "USR-003", org: "DON-001" });
    expect(() =>
      userFromAccessToken(token, { email: "donor1@ewaste.test" }),
    ).toThrow(/role/);
  });
});
