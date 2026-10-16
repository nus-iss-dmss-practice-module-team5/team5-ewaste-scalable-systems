// Rule 6 (EWSS-15): a failed pickup carries its capture time. Until the API
// has a time field, the app appends "Observed offline at <time>" to
// observed_details, within the server's 500-rune limit.

/** Server limit for observed_details, in Unicode code points (Go runes). */
export const OBSERVED_DETAILS_MAX = 500;

const ELLIPSIS = "…";
const SEPARATOR = "\n";

// Matches a suffix this module wrote, so re-applying replaces it.
const SUFFIX_PATTERN =
  /(?:^|\n)Observed offline at \d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC(?:, confirmed after sign-in at \d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC)?\s*$/;

/** Readable UTC time, e.g. "2026-10-16 09:30 UTC". */
export function formatObservedTime(value: Date | string): string {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) {
    throw new RangeError(`Invalid observed time: ${String(value)}`);
  }
  const iso = date.toISOString();
  return `${iso.slice(0, 10)} ${iso.slice(11, 16)} UTC`;
}

function codePoints(text: string): string[] {
  return Array.from(text);
}

/** Removes a suffix written by withObservedNote, leaving the collector's text. */
export function stripObservedNote(details: string | undefined): string {
  return (details ?? "").replace(SUFFIX_PATTERN, "").trimEnd();
}

/**
 * Returns the collector's text followed by "Observed offline at <t1>" (or
 * "Observed offline at <t1>, confirmed after sign-in at <t2>"). The result is
 * at most 500 code points: the collector's text is shortened with "…" so the
 * suffix always fits. An existing suffix is replaced, never stacked.
 */
export function withObservedNote(
  details: string | undefined,
  capturedAt: Date | string,
  confirmedAt?: Date | string,
): string {
  let suffix = `Observed offline at ${formatObservedTime(capturedAt)}`;
  if (confirmedAt !== undefined) {
    suffix += `, confirmed after sign-in at ${formatObservedTime(confirmedAt)}`;
  }
  const userText = stripObservedNote(details);
  if (userText.trim() === "") {
    return suffix;
  }
  const budget =
    OBSERVED_DETAILS_MAX -
    codePoints(suffix).length -
    codePoints(SEPARATOR).length;
  const chars = codePoints(userText);
  const kept =
    chars.length <= budget
      ? userText
      : chars.slice(0, Math.max(0, budget - 1)).join("").trimEnd() + ELLIPSIS;
  return `${kept}${SEPARATOR}${suffix}`;
}
