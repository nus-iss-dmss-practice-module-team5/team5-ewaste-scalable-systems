import type { NextConfig } from "next";

const apiTarget = (
  process.env.API_PROXY_TARGET ||
  process.env.NEXT_PUBLIC_API_BASE_URL ||
  process.env.NEXT_PUBLIC_API_URL ||
  "http://localhost:8080"
).replace(/\/$/, "");

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
