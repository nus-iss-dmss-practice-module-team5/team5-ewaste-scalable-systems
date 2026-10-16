import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { Providers } from "./providers";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "E-Waste Platform",
  description:
    "E-waste collection, recycling traceability, and environmental impact platform",
  // app/manifest.ts adds <link rel="manifest">. These cover iOS, which ignores
  // most of the manifest for "Add to Home Screen".
  applicationName: "E-Waste Platform",
  appleWebApp: {
    capable: true,
    title: "E-Waste",
    statusBarStyle: "default",
  },
  icons: {
    apple: [
      { url: "/icons/apple-touch-icon.png", sizes: "180x180", type: "image/png" },
    ],
  },
};

export const viewport: Viewport = {
  // Keep in sync with theme_color in app/manifest.ts.
  themeColor: "#134e4a",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="en"
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <body className="min-h-full flex flex-col bg-slate-100 text-slate-900">
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
