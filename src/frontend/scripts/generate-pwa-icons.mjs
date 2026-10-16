#!/usr/bin/env node
// Generates the PWA icons in public/icons without any image library.
//
//   node scripts/generate-pwa-icons.mjs
//
// Each icon is a solid #134e4a square with a light (#f1f5f9) parcel box glyph
// and a small recycling loop. The maskable icon keeps the whole glyph inside
// the central 80% safe zone (a circle of radius 40% of the icon size), so
// launchers can crop it to any shape. PNGs are encoded by hand: RGB, 8-bit,
// filter 0, deflated with node:zlib.

import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

const here = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.resolve(here, "../public/icons");

const BG = [0x13, 0x4e, 0x4a]; // #134e4a
const FG = [0xf1, 0xf5, 0xf9]; // #f1f5f9
const SAMPLES = 4; // 4x4 supersampling for smooth edges

const ICONS = [
  // Glyph half-size is a fraction of the icon size.
  { file: "icon-192.png", size: 192, half: 0.34 },
  { file: "icon-512.png", size: 512, half: 0.34 },
  // Furthest glyph point is ~1.31 half-units from centre: 1.31 * 0.27 < 0.40.
  { file: "icon-maskable-512.png", size: 512, half: 0.27 },
  { file: "apple-touch-icon.png", size: 180, half: 0.34 },
];

function inRoundedRect(x, y, x0, y0, x1, y1, r) {
  if (x < x0 || x > x1 || y < y0 || y > y1) return false;
  const cx = Math.min(Math.max(x, x0 + r), x1 - r);
  const cy = Math.min(Math.max(y, y0 + r), y1 - r);
  return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
}

function inTriangle(x, y, [ax, ay], [bx, by], [cx, cy]) {
  const d1 = (x - bx) * (ay - by) - (ax - bx) * (y - by);
  const d2 = (x - cx) * (by - cy) - (bx - cx) * (y - cy);
  const d3 = (x - ax) * (cy - ay) - (cx - ax) * (y - ay);
  const neg = d1 < 0 || d2 < 0 || d3 < 0;
  const pos = d1 > 0 || d2 > 0 || d3 > 0;
  return !(neg && pos);
}

// Glyph in "half units": the box spans roughly [-1, 1] on both axes, y down.
function isForeground(x, y) {
  const lid = inRoundedRect(x, y, -1.0, -0.8, 1.0, -0.4, 0.08);
  const body = inRoundedRect(x, y, -0.85, -0.3, 0.85, 0.9, 0.1);
  if (!lid && !body) return false;

  // Packing tape down the middle of the lid and the top of the body.
  if (Math.abs(x) < 0.13 && y < -0.05) return false;

  // Recycling loop on the body: a ring with a gap and an arrowhead.
  const rx = x;
  const ry = y - 0.42;
  const dist = Math.hypot(rx, ry);
  const angle = Math.atan2(ry, rx); // 0 = right, -PI/2 = up
  const inGap = angle > -Math.PI / 2 && angle < -Math.PI / 8;
  if (dist > 0.2 && dist < 0.32 && !inGap) return false;
  if (inTriangle(rx, ry, [-0.04, -0.4], [-0.04, -0.12], [0.16, -0.26])) {
    return false;
  }
  return true;
}

function renderPixels(size, half) {
  const rgb = Buffer.alloc(size * size * 3);
  const scale = half * size;
  for (let py = 0; py < size; py++) {
    for (let px = 0; px < size; px++) {
      let hits = 0;
      for (let sy = 0; sy < SAMPLES; sy++) {
        for (let sx = 0; sx < SAMPLES; sx++) {
          const x = (px + (sx + 0.5) / SAMPLES - size / 2) / scale;
          const y = (py + (sy + 0.5) / SAMPLES - size / 2) / scale;
          if (isForeground(x, y)) hits++;
        }
      }
      const t = hits / (SAMPLES * SAMPLES);
      const offset = (py * size + px) * 3;
      for (let c = 0; c < 3; c++) {
        rgb[offset + c] = Math.round(BG[c] + (FG[c] - BG[c]) * t);
      }
    }
  }
  return rgb;
}

const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) {
      c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    }
    table[n] = c >>> 0;
  }
  return table;
})();

function crc32(buf) {
  let c = 0xffffffff;
  for (const byte of buf) {
    c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  }
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length);
  const typeAndData = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(typeAndData));
  return Buffer.concat([length, typeAndData, crc]);
}

function encodePng(size, rgb) {
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header[8] = 8; // bit depth
  header[9] = 2; // colour type: RGB
  header[10] = 0; // compression
  header[11] = 0; // filter
  header[12] = 0; // no interlace

  const stride = size * 3;
  const raw = Buffer.alloc((stride + 1) * size);
  for (let y = 0; y < size; y++) {
    raw[y * (stride + 1)] = 0; // filter type: none
    rgb.copy(raw, y * (stride + 1) + 1, y * stride, (y + 1) * stride);
  }

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

mkdirSync(outDir, { recursive: true });
for (const icon of ICONS) {
  const png = encodePng(icon.size, renderPixels(icon.size, icon.half));
  const target = path.join(outDir, icon.file);
  writeFileSync(target, png);
  console.log(`wrote ${path.relative(process.cwd(), target)} (${png.length} bytes)`);
}
