import { describe, expect, it } from "vitest";
import {
  OBSERVED_DETAILS_MAX,
  formatObservedTime,
  stripObservedNote,
  withObservedNote,
} from "./observed-note";

const t1 = new Date("2026-10-16T09:30:42.123Z");
const t2 = "2026-10-16T11:05:00.000Z";
const runes = (text: string) => Array.from(text).length;

describe("observed note", () => {
  it("formats readable UTC time", () => {
    expect(formatObservedTime(t1)).toBe("2026-10-16 09:30 UTC");
    expect(formatObservedTime("2026-10-16T23:59:59+02:00")).toBe(
      "2026-10-16 21:59 UTC",
    );
    expect(() => formatObservedTime("not a date")).toThrow(RangeError);
  });

  it("uses only the suffix when there is no text", () => {
    expect(withObservedNote(undefined, t1)).toBe(
      "Observed offline at 2026-10-16 09:30 UTC",
    );
    expect(withObservedNote("   ", t1)).toBe(
      "Observed offline at 2026-10-16 09:30 UTC",
    );
  });

  it("appends the suffix on a new line after the text", () => {
    expect(withObservedNote("Gate locked", t1)).toBe(
      "Gate locked\nObserved offline at 2026-10-16 09:30 UTC",
    );
  });

  it("adds the confirmation time", () => {
    expect(withObservedNote("Gate locked", t1, t2)).toBe(
      "Gate locked\nObserved offline at 2026-10-16 09:30 UTC, confirmed after sign-in at 2026-10-16 11:05 UTC",
    );
  });

  it("replaces an existing suffix instead of stacking", () => {
    const saved = withObservedNote("Gate locked", t1);
    expect(withObservedNote(saved, t1)).toBe(saved);
    const confirmed = withObservedNote(saved, t1, t2);
    expect(confirmed).toBe(withObservedNote("Gate locked", t1, t2));
    expect(withObservedNote(confirmed, t1, t2)).toBe(confirmed);
    expect(confirmed.match(/Observed offline/g)).toHaveLength(1);
    expect(stripObservedNote(confirmed)).toBe("Gate locked");
    expect(withObservedNote(withObservedNote(undefined, t1), t1, t2)).toBe(
      withObservedNote(undefined, t1, t2),
    );
  });

  it("keeps text that fits exactly", () => {
    const suffix = withObservedNote(undefined, t1);
    const text = "a".repeat(OBSERVED_DETAILS_MAX - runes(suffix) - 1);
    const result = withObservedNote(text, t1);
    expect(runes(result)).toBe(OBSERVED_DETAILS_MAX);
    expect(result.startsWith(text)).toBe(true);
    expect(result).not.toContain("…");
  });

  it("truncates the collector's text so the suffix always fits", () => {
    const result = withObservedNote("x".repeat(600), t1, t2);
    expect(runes(result)).toBeLessThanOrEqual(OBSERVED_DETAILS_MAX);
    expect(result).toMatch(
      /x…\nObserved offline at 2026-10-16 09:30 UTC, confirmed after sign-in at 2026-10-16 11:05 UTC$/,
    );
  });

  it("counts code points, not UTF-16 units, for emoji and CJK", () => {
    for (const ch of ["😀", "回", "é"]) {
      const result = withObservedNote(ch.repeat(500), t1, t2);
      expect(runes(result)).toBe(OBSERVED_DETAILS_MAX);
      // No lone surrogate halves.
      expect(result).toBe(Array.from(result).join(""));
      expect(() => encodeURIComponent(result)).not.toThrow();
      expect(result).toContain(`${ch}…\nObserved offline at`);
    }
    // 400 emoji is 800 UTF-16 units but fits in runes with the short suffix.
    const fits = "😀".repeat(400);
    expect(withObservedNote(fits, t1).startsWith(fits)).toBe(true);
  });

  it("does not double the ellipsis when re-applied with a longer suffix", () => {
    const saved = withObservedNote("y".repeat(600), t1);
    const confirmed = withObservedNote(saved, t1, t2);
    expect(runes(confirmed)).toBeLessThanOrEqual(OBSERVED_DETAILS_MAX);
    expect(confirmed).not.toContain("……");
    expect(confirmed.match(/Observed offline/g)).toHaveLength(1);
  });
});
