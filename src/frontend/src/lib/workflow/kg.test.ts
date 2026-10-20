import { describe, expect, it } from "vitest";
import { formatKg, parseKg } from "./kg";

describe("kilogram helpers", () => {
  it("reads up to two decimals as whole hundredths", () => {
    expect(parseKg("12")).toBe(1200);
    expect(parseKg("12.5")).toBe(1250);
    expect(parseKg(" 4.20 ")).toBe(420);
    expect(parseKg("0.00")).toBe(0);
  });

  it("rejects excess precision instead of rounding", () => {
    expect(parseKg("12.005")).toBeNull();
    expect(parseKg("12.000")).toBeNull();
  });

  it("rejects signs, blanks and text", () => {
    expect(parseKg("-1.00")).toBeNull();
    expect(parseKg("")).toBeNull();
    expect(parseKg("1e2")).toBeNull();
  });

  it("pads to two decimals", () => {
    expect(formatKg(1200)).toBe("12.00");
    expect(formatKg(1250)).toBe("12.50");
    expect(formatKg(5)).toBe("0.05");
  });
});
