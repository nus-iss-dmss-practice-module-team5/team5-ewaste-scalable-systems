// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  getRewrittenUrl,
  unstable_getResponseFromNextConfig,
} from "next/experimental/testing/server";

const canonicalTargets = {
  IDENTITY_ACCESS_URL: "https://identity.test",
  BATCH_WORKFLOW_URL: "https://workflow.test",
  CLAIM_LOGISTICS_URL: "https://logistics.test",
  CUSTODY_VERIFICATION_URL: "https://custody.test",
};

const aliasTargets = {
  AUTH_SERVICE_URL: "https://identity-alias.test",
  BATCH_SERVICE_URL: "https://workflow-alias.test",
  CLAIM_SERVICE_URL: "https://logistics-alias.test",
  CUSTODY_SERVICE_URL: "https://custody-alias.test",
};

const allEnvKeys = [
  ...Object.keys(canonicalTargets),
  ...Object.keys(aliasTargets),
  "API_PROXY_TARGET",
  "NEXT_PUBLIC_API_BASE_URL",
  "NEXT_PUBLIC_API_URL",
];

beforeEach(() => {
  vi.resetModules();
  for (const name of allEnvKeys) vi.stubEnv(name, "");
});
afterEach(() => vi.unstubAllEnvs());

async function destination(path: string) {
  const { default: nextConfig } = await import("./next.config");
  return getRewrittenUrl(
    await unstable_getResponseFromNextConfig({
      url: `http://ui:3000${path}`,
      nextConfig,
    }),
  );
}

describe("API routing", () => {
  it("preserves the monolith target when split routing is unset", async () => {
    vi.stubEnv("API_PROXY_TARGET", "https://monolith.test/");
    expect(await destination("/api/v1/batches/b1/claim")).toBe(
      "https://monolith.test/api/v1/batches/b1/claim",
    );
  });

  it("retains custom ports in configured destinations", async () => {
    for (const name of Object.keys(canonicalTargets))
      vi.stubEnv(name, "http://localhost:8084/");
    const { default: config } = await import("./next.config");
    const routes = await config.rewrites!();
    // Next's experimental response helper drops destination ports, so check
    // the configured URLs directly for this part of the contract.
    expect(
      Array.isArray(routes) &&
        routes.every((route) =>
          route.destination.startsWith("http://localhost:8084/"),
        ),
    ).toBe(true);
  });

  it("rejects partial split configuration", async () => {
    vi.stubEnv("IDENTITY_ACCESS_URL", canonicalTargets.IDENTITY_ACCESS_URL);
    await expect(import("./next.config")).rejects.toThrow(
      "Set all four service URLs",
    );
  });

  it("routes using alias service URLs when canonical variables are unset", async () => {
    for (const [name, value] of Object.entries(aliasTargets)) {
      vi.stubEnv(name, value);
    }
    expect(await destination("/api/v1/auth/login")).toBe(
      "https://identity-alias.test/v1/auth/login",
    );
    expect(await destination("/api/v1/batches/b1/claim")).toBe(
      "https://logistics-alias.test/v1/claims/batches/b1",
    );
    expect(await destination("/api/v1/assignments/a1/handoff")).toBe(
      "https://custody-alias.test/v1/custody/assignments/a1/handoff",
    );
    expect(await destination("/api/v1/batches")).toBe(
      "https://workflow-alias.test/v1/batches",
    );
  });

  it.each([
    ["/api/v1/auth/login", "identity", "/v1/auth/login"],
    ["/api/v1/batches?page=2", "workflow", "/v1/batches?page=2"],
    ["/api/v1/batches/b1/submit", "workflow", "/v1/batches/b1/submit"],
    ["/api/v1/opportunities", "workflow", "/v1/opportunities"],
    ["/api/v1/batches/b1/claim", "logistics", "/v1/claims/batches/b1"],
    [
      "/api/v1/batches/b1/assignments",
      "logistics",
      "/v1/logistics/assignments/batches/b1",
    ],
    ["/api/v1/assignments", "logistics", "/v1/logistics/assignments"],
    [
      "/api/v1/assignments/a1/accept",
      "logistics",
      "/v1/logistics/assignments/a1/accept",
    ],
    [
      "/api/v1/assignments/a1/reject",
      "logistics",
      "/v1/logistics/assignments/a1/reject",
    ],
    [
      "/api/v1/assignments/a1/fail",
      "logistics",
      "/v1/logistics/assignments/a1/fail",
    ],
    [
      "/api/v1/assignments/a1/handoff",
      "custody",
      "/v1/custody/assignments/a1/handoff",
    ],
    [
      "/api/v1/batches/b1/receipt",
      "custody",
      "/v1/processing/batches/b1/receipt",
    ],
    [
      "/api/v1/batches/b1/treatment",
      "custody",
      "/v1/processing/batches/b1/treatment",
    ],
    ["/api/v1/batches/b1/evidence", "custody", "/v1/evidence/batches/b1"],
    ["/api/v1/batches/b1/evidence/e1", "custody", "/v1/evidence/batches/b1/e1"],
    ["/api/v1/processing/batches", "custody", "/v1/processing/batches"],
    ["/api/v1/audit/impact", "custody", "/v1/audit/impact"],
    [
      "/api/v1/batches/b1/analytics-input?source_event_id=e1",
      "workflow",
      "/internal/v1/analytics/batches/b1/input?source_event_id=e1",
    ],
    [
      "/api/v1/batches/b1/analytics-results",
      "workflow",
      "/internal/v1/analytics/batches/b1/results",
    ],
  ])("routes %s to its owner", async (path, service, localPath) => {
    for (const [name, value] of Object.entries(canonicalTargets))
      vi.stubEnv(name, value);
    expect(await destination(path)).toBe(`https://${service}.test${localPath}`);
  });
});
