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
          { key: "Cache-Control", value: "no-cache, no-store, must-revalidate" },
          { key: "Content-Type", value: "application/javascript; charset=utf-8" },
        ],
      },
    ];
  },
  async rewrites() {
    return [
      {
        source: "/api/v1/:path*",
        destination: `${apiTarget}/api/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
