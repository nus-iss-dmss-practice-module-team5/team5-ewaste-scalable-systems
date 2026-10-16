import type { MetadataRoute } from "next";

/**
 * Served by Next.js at /manifest.webmanifest. Keep the colours in sync with
 * `viewport.themeColor` in layout.tsx. The service worker precaches this file.
 */
export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "E-Waste Platform",
    short_name: "E-Waste",
    description:
      "E-waste collection, recycling traceability, and environmental impact platform",
    id: "/home",
    start_url: "/home",
    scope: "/",
    display: "standalone",
    theme_color: "#134e4a",
    background_color: "#f1f5f9",
    icons: [
      {
        src: "/icons/icon-192.png",
        sizes: "192x192",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icons/icon-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icons/icon-maskable-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "maskable",
      },
    ],
  };
}
