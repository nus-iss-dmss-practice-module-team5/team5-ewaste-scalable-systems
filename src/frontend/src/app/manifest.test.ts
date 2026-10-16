import { existsSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import manifest from "./manifest";

const publicDir = path.resolve(import.meta.dirname, "../../public");

describe("web app manifest", () => {
  it("opens the installed app on /home as a standalone window", () => {
    const result = manifest();

    expect(result.name).toBe("E-Waste Platform");
    expect(result.short_name).toBe("E-Waste");
    expect(result.start_url).toBe("/home");
    expect(result.display).toBe("standalone");
    expect(result.theme_color).toBe("#134e4a");
    expect(result.background_color).toBe("#f1f5f9");
  });

  it("lists 192, 512 and maskable 512 PNG icons under /icons", () => {
    const icons = manifest().icons ?? [];

    expect(icons).toEqual([
      expect.objectContaining({
        src: "/icons/icon-192.png",
        sizes: "192x192",
        type: "image/png",
      }),
      expect.objectContaining({
        src: "/icons/icon-512.png",
        sizes: "512x512",
        type: "image/png",
      }),
      expect.objectContaining({
        src: "/icons/icon-maskable-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "maskable",
      }),
    ]);
  });

  it("points every icon at a file that exists in public/", () => {
    for (const icon of manifest().icons ?? []) {
      expect(existsSync(path.join(publicDir, icon.src))).toBe(true);
    }
  });
});
