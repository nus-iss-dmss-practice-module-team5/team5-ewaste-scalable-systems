const KG_PATTERN = /^\d{1,6}(\.\d{1,2})?$/;

// Kilograms as whole hundredths, so sums and comparisons are exact. Returns
// null for anything the API would reject: a sign, or more than two decimals.
export function parseKg(raw: string): number | null {
  const text = raw.trim();
  if (!KG_PATTERN.test(text)) {
    return null;
  }
  const [whole, fraction = ""] = text.split(".");
  return Number(whole) * 100 + Number(fraction.padEnd(2, "0"));
}

export function formatKg(hundredths: number): string {
  return `${Math.floor(hundredths / 100)}.${String(hundredths % 100).padStart(2, "0")}`;
}
