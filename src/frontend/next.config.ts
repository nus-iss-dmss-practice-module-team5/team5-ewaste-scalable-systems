import { execSync } from "node:child_process";
import type { NextConfig } from "next";

const apiTarget = (
  process.env.API_PROXY_TARGET ||
  process.env.NEXT_PUBLIC_API_BASE_URL ||
  process.env.NEXT_PUBLIC_API_URL ||
  "http://localhost:8080"
).replace(/\/$/, "");

/**
 * One id per build. The service worker is registered as /sw.js?v=<id> and
 * names its caches shell-<id> / static-<id>, so a new build installs a new
 * worker and drops the old caches. Order: BUILD_ID env (CI/Docker), the git
 * commit, then a timestamp.
 */
function resolveBuildId(): string {
  const fromEnv = process.env.BUILD_ID?.trim();
  if (fromEnv) {
    return sanitize(fromEnv);
  }
  try {
    const sha = execSync("git rev-parse --short=12 HEAD", {
      cwd: import.meta.dirname,
      stdio: ["ignore", "pipe", "ignore"],
    })
      .toString()
      .trim();
    if (sha) {
      return sanitize(sha);
    }
  } catch {
    // No git in the Docker build context; fall through to a timestamp.
  }
  return `t${Date.now().toString(36)}`;
}

function sanitize(id: string): string {
  return id.replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 64);
}

const buildId = resolveBuildId();
// Build workers re-evaluate this file; pin the id so they all agree.
process.env.BUILD_ID = buildId;

const serviceTargets = [
  process.env.IDENTITY_ACCESS_URL,
  process.env.BATCH_WORKFLOW_URL,
  process.env.CLAIM_LOGISTICS_URL,
  process.env.CUSTODY_VERIFICATION_URL,
];
if (serviceTargets.some(Boolean) && !serviceTargets.every(Boolean)) {
  throw new Error("Set all four service URLs to enable split API routing.");
}

const nextConfig: NextConfig = {
  agentRules: false,
  turbopack: {
    root: import.meta.dirname,
  },
  generateBuildId: async () => buildId,
  env: {
    NEXT_PUBLIC_BUILD_ID: buildId,
  },
  async headers() {
    return [
      {
        // Always revalidate the worker script so updates are picked up.
        source: "/sw.js",
        headers: [
          {
            key: "Cache-Control",
            value: "no-cache, no-store, must-revalidate",
          },
          {
            key: "Content-Type",
            value: "application/javascript; charset=utf-8",
          },
        ],
      },
    ];
  },
  async rewrites() {
    if (serviceTargets.every(Boolean)) {
      const [identity, workflow, logistics, custody] = serviceTargets.map(
        (target) => target!.replace(/\/$/, ""),
      );
      // Specific batch/assignment actions precede their catch-all reads.
      return [
        {
          source: "/api/v1/auth/:path*",
          destination: `${identity}/v1/auth/:path*`,
        },
        {
          source: "/api/v1/batches/:batch_id/claim",
          destination: `${logistics}/v1/claims/batches/:batch_id`,
        },
        {
          source: "/api/v1/batches/:batch_id/assignments",
          destination: `${logistics}/v1/logistics/assignments/batches/:batch_id`,
        },
        {
          source: "/api/v1/assignments/:assignment_id/handoff",
          destination: `${custody}/v1/custody/assignments/:assignment_id/handoff`,
        },
        {
          source: "/api/v1/assignments/:path*",
          destination: `${logistics}/v1/logistics/assignments/:path*`,
        },
        {
          source: "/api/v1/batches/:batch_id/receipt",
          destination: `${custody}/v1/processing/batches/:batch_id/receipt`,
        },
        {
          source: "/api/v1/batches/:batch_id/treatment",
          destination: `${custody}/v1/processing/batches/:batch_id/treatment`,
        },
        {
          source: "/api/v1/batches/:batch_id/evidence/:path*",
          destination: `${custody}/v1/evidence/batches/:batch_id/:path*`,
        },
        {
          source: "/api/v1/processing/:path*",
          destination: `${custody}/v1/processing/:path*`,
        },
        {
          source: "/api/v1/audit/:path*",
          destination: `${custody}/v1/audit/:path*`,
        },
        {
          source: "/api/v1/batches/:batch_id/analytics-input",
          destination: `${workflow}/internal/v1/analytics/batches/:batch_id/input`,
        },
        {
          source: "/api/v1/batches/:batch_id/analytics-results",
          destination: `${workflow}/internal/v1/analytics/batches/:batch_id/results`,
        },
        {
          source: "/api/v1/opportunities/:path*",
          destination: `${workflow}/v1/opportunities/:path*`,
        },
        {
          source: "/api/v1/batches/:path*",
          destination: `${workflow}/v1/batches/:path*`,
        },
      ];
    }
    return [
      {
        source: "/api/v1/:path*",
        destination: `${apiTarget}/api/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
