import type { Role, SessionUser } from "./types";

const ROLES: Role[] = ["DONOR", "RECYCLER", "COLLECTOR", "AUDITOR", "ADMIN"];

const ROLE_ALIASES: Record<string, Role> = {
  SYSTEM_ADMIN: "ADMIN",
  ADMINISTRATOR: "ADMIN",
};

type SeedProfile = Pick<SessionUser, "name" | "organisationName">;

const SEED_PROFILES: Record<string, SeedProfile> = {
  "admin@ewaste.test": {
    name: "Platform Administrator",
    organisationName: "E-Waste Platform",
  },
  "auditor@ewaste.test": {
    name: "Platform Auditor",
    organisationName: "E-Waste Platform",
  },
  "donor1@ewaste.test": {
    name: "Green Office Donor",
    organisationName: "DON-001",
  },
  "donor2@ewaste.test": {
    name: "Community Hub Donor",
    organisationName: "DON-002",
  },
  "collector1@ewaste.test": {
    name: "Green Collect Operator",
    organisationName: "COL-001",
  },
  "collector2@ewaste.test": {
    name: "EcoPickup Operator",
    organisationName: "COL-002",
  },
  "recycler1@ewaste.test": {
    name: "EcoCycle Processing Facility",
    organisationName: "PROC-001",
  },
  "recycler2@ewaste.test": {
    name: "RenewTech Processing Facility",
    organisationName: "PROC-002",
  },
};

export type UserClaimFallbacks = Partial<
  Pick<SessionUser, "email" | "name" | "organisationName">
>;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function readString(
  record: Record<string, unknown>,
  key: string,
): string | undefined {
  const value = record[key];
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function nameFromEmail(email: string): string {
  const local = email.split("@")[0] ?? email;
  return local.replace(/[._-]+/g, " ");
}

export function decodeJwtPayload(token: string): Record<string, unknown> {
  const parts = token.split(".");
  if (parts.length < 2 || !parts[1]) {
    throw new Error("invalid jwt");
  }
  const padded = parts[1]
    .replace(/-/g, "+")
    .replace(/_/g, "/")
    .padEnd(Math.ceil(parts[1].length / 4) * 4, "=");
  const json = atob(padded);
  const parsed: unknown = JSON.parse(json);
  if (!isRecord(parsed)) {
    throw new Error("invalid jwt payload");
  }
  return parsed;
}

export function parseRole(value: unknown): Role | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const normalised = value.trim().toUpperCase();
  return ROLE_ALIASES[normalised] ?? ROLES.find((role) => role === normalised);
}

export function userFromAccessToken(
  accessToken: string,
  fallbacks: UserClaimFallbacks = {},
): SessionUser {
  const payload = decodeJwtPayload(accessToken);
  const role = parseRole(payload.role);
  const id = readString(payload, "sub");
  const email = fallbacks.email?.trim();
  if (!role || !id || !email) {
    throw new Error("access token is missing role or subject, or no email");
  }

  const seed = SEED_PROFILES[email.toLowerCase()];
  const organisationId = readString(payload, "org") ?? "";
  const organisationName =
    fallbacks.organisationName ?? seed?.organisationName ?? organisationId;
  const name = fallbacks.name ?? seed?.name ?? nameFromEmail(email);

  return {
    id,
    email,
    name,
    organisationId,
    organisationName,
    role,
  };
}
