import type { ReactNode } from "react";
import { isWorkflowError, type WorkflowErrorKind } from "@/lib/workflow/errors";

export function Banner({
  testId,
  tone,
  children,
}: {
  testId: string;
  tone: "error" | "warning" | "success" | "info";
  children: ReactNode;
}) {
  const toneClass = {
    error: "border-red-300 bg-red-50 text-red-800",
    warning: "border-amber-300 bg-amber-50 text-amber-950",
    success: "border-teal-300 bg-teal-50 text-teal-900",
    info: "border-slate-200 bg-white text-slate-700",
  }[tone];
  return (
    <p
      role={tone === "error" || tone === "warning" ? "alert" : "status"}
      data-testid={testId}
      className={`rounded-md border px-3 py-2 text-sm ${toneClass}`}
    >
      {children}
    </p>
  );
}

export function LoadingLine({
  testId,
  children,
}: {
  testId: string;
  children: string;
}) {
  return (
    <p role="status" data-testid={testId} className="text-sm text-slate-600">
      {children}
    </p>
  );
}

export function messageForKind(kind: WorkflowErrorKind): string {
  switch (kind) {
    case "forbidden":
      return "You do not have access to this record for your organisation.";
    case "not_found":
      return "That record is missing or no longer visible.";
    case "conflict":
      return "This record changed. Refresh and try again.";
    case "duplicate":
      return "This command was already sent. Refresh to see the current result.";
    case "validation":
      return "One or more fields are invalid.";
    case "network":
      return "Could not reach the workflow service. Try again.";
    case "unavailable":
      return "The workflow service is temporarily unavailable. Try again.";
    case "session":
      return "Your session is no longer valid. Sign in again.";
    default:
      return "The request failed.";
  }
}

export function bannerForError(error: unknown, fallbackTestId: string) {
  if (!isWorkflowError(error)) {
    return {
      testId: fallbackTestId,
      tone: "error" as const,
      text: messageForKind("error"),
    };
  }
  const tone =
    error.kind === "conflict" ||
    error.kind === "duplicate" ||
    error.kind === "validation"
      ? ("warning" as const)
      : ("error" as const);
  let text =
    error.kind === "validation" ||
    error.kind === "conflict" ||
    error.kind === "duplicate"
      ? error.message || messageForKind(error.kind)
      : messageForKind(error.kind);
  if (error.code === "STALE_VERSION" && error.currentState) {
    text = `This draft is out of date. The server now shows assignment ${error.currentState.assignmentStatus} and batch ${error.currentState.batchStatus}. Review the latest assignment before sending again.`;
  }
  return { testId: `${fallbackTestId}-${error.kind}`, tone, text };
}

export function formatWhen(value?: string): string {
  if (!value) {
    return "—";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString();
}

export function saveFile(file: Blob, fileName: string) {
  const url = URL.createObjectURL(file);
  const link = document.createElement("a");
  link.href = url;
  link.download = fileName;
  link.click();
  URL.revokeObjectURL(url);
}

const ANOMALY_LABELS: Record<string, string> = {
  CATEGORY_MISMATCH: "Category mismatch",
  COUNT_MISMATCH: "Item count mismatch",
  WEIGHT_MISMATCH: "Weight mismatch",
  MISSING_OUTCOME: "Missing outcome",
  UNALLOCATED_WEIGHT: "Unallocated weight",
};

// A code this screen does not know yet is shown as stored.
export function anomalyLabel(code: string): string {
  return ANOMALY_LABELS[code] ?? code;
}

export function toUtcIso(localValue: string): string | null {
  const date = new Date(localValue);
  if (Number.isNaN(date.getTime())) {
    return null;
  }
  return date.toISOString();
}

export function toLocalInput(iso?: string): string {
  if (!iso) {
    return "";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const pad = (part: number) => String(part).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
