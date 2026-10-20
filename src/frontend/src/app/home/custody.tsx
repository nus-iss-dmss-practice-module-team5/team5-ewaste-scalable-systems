"use client";

import { useSession } from "@/lib/auth/session-context";
import {
  downloadEvidence,
  getBatchAnomalies,
  getBatchTimeline,
} from "@/lib/workflow/api";
import type { Anomaly, TimelineEntry } from "@/lib/workflow/types";
import { FormEvent, useEffect, useState } from "react";
import {
  Banner,
  LoadingLine,
  anomalyLabel,
  bannerForError,
  formatWhen,
  saveFile,
} from "./workflow-ui";

type Custody = {
  batchId: string;
  timeline: TimelineEntry[];
  anomalies: Anomaly[];
};

// Evidence has no list route for an Auditor. Each upload is a timeline event
// that carries the evidence id, so the timeline is the source for this list.
type EvidenceItem = {
  evidenceId: string;
  stage?: string;
  mimeType?: string;
  sizeBytes?: string;
  sha256Hash?: string;
  uploadedAt: string;
  source: string;
};

const EVIDENCE_EXTENSIONS: Record<string, string> = {
  "application/pdf": ".pdf",
  "image/jpeg": ".jpg",
  "image/png": ".png",
};

function detailText(
  details: Record<string, unknown>,
  key: string,
): string | undefined {
  const value = details[key];
  if (typeof value === "string") {
    return value.trim() ? value : undefined;
  }
  return typeof value === "number" || typeof value === "boolean"
    ? String(value)
    : undefined;
}

function detailValue(value: unknown): string {
  if (value === null || value === undefined || value === "") {
    return "—";
  }
  return typeof value === "object" ? JSON.stringify(value) : String(value);
}

// "RequestSubmitted" reads as "Request submitted".
function eventLabel(eventType: string): string {
  const words = eventType.replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function sourceOf(entry: TimelineEntry): string {
  if (entry.servicePrincipal) {
    return `Service ${entry.servicePrincipal}`;
  }
  if (!entry.actorUserId) {
    return "—";
  }
  return entry.actorOrganisationId
    ? `User ${entry.actorUserId} · ${entry.actorOrganisationId}`
    : `User ${entry.actorUserId}`;
}

// Analytics records a rule version. Matching records a rule set version.
const POLICY_KEYS = ["rule_version", "rule_set_version"];

function policyKey(entry: TimelineEntry): string | undefined {
  return POLICY_KEYS.find((key) => detailText(entry.details, key));
}

// The policy version gets its own row, so it is left out of the other details.
function eventRows(entry: TimelineEntry): string[][] {
  const policy = policyKey(entry);
  return [
    ["Source", sourceOf(entry)],
    ...(policy
      ? [["Policy version", detailText(entry.details, policy) ?? "—"]]
      : []),
    ["Correlation ID", entry.correlationId],
    ...Object.entries(entry.details)
      .filter(([key]) => key !== policy)
      .map(([key, value]) => [key.replaceAll("_", " "), detailValue(value)]),
  ];
}

function evidenceFrom(entry: TimelineEntry): EvidenceItem[] {
  const evidenceId = detailText(entry.details, "evidence_id");
  if (entry.eventType !== "EvidenceUploaded" || !evidenceId) {
    return [];
  }
  return [
    {
      evidenceId,
      stage: detailText(entry.details, "lifecycle_stage"),
      mimeType: detailText(entry.details, "mime_type"),
      sizeBytes: detailText(entry.details, "file_size_bytes"),
      sha256Hash: detailText(entry.details, "sha256_hash"),
      uploadedAt: entry.occurredAt,
      source: sourceOf(entry),
    },
  ];
}

function formatSize(sizeBytes?: string): string {
  const bytes = Number(sizeBytes);
  if (!sizeBytes || !Number.isFinite(bytes)) {
    return "—";
  }
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  return bytes < 1048576
    ? `${(bytes / 1024).toFixed(1)} KB`
    : `${(bytes / 1048576).toFixed(1)} MB`;
}

export function CustodyView({ initialBatchId }: { initialBatchId?: string }) {
  const { session } = useSession();
  const [batchInput, setBatchInput] = useState(initialBatchId ?? "");
  const [custody, setCustody] = useState<Custody | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [missingId, setMissingId] = useState(false);
  const [downloading, setDownloading] = useState<string | null>(null);
  const [downloadError, setDownloadError] = useState<unknown>(null);

  async function load(batchId: string) {
    if (!session) {
      return;
    }
    setLoading(true);
    setLoadError(null);
    setDownloadError(null);
    setCustody(null);
    try {
      const [timeline, anomalies] = await Promise.all([
        getBatchTimeline(session.tokens.accessToken, batchId),
        getBatchAnomalies(session.tokens.accessToken, batchId),
      ]);
      setCustody({ batchId, timeline, anomalies });
    } catch (error) {
      setLoadError(error);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    if (!initialBatchId) {
      return;
    }
    const timer = window.setTimeout(() => {
      void load(initialBatchId);
    }, 0);
    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialBatchId]);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const batchId = batchInput.trim();
    setMissingId(!batchId);
    if (batchId) {
      await load(batchId);
    }
  }

  async function onDownload(item: EvidenceItem) {
    if (!session || !custody) {
      return;
    }
    setDownloading(item.evidenceId);
    setDownloadError(null);
    try {
      saveFile(
        await downloadEvidence(
          session.tokens.accessToken,
          custody.batchId,
          item.evidenceId,
        ),
        `evidence-${item.evidenceId}${EVIDENCE_EXTENSIONS[item.mimeType ?? ""] ?? ""}`,
      );
    } catch (error) {
      setDownloadError(error);
    } finally {
      setDownloading(null);
    }
  }

  const loadBanner = loadError ? bannerForError(loadError, "custody") : null;
  const downloadBanner = downloadError
    ? bannerForError(downloadError, "custody-download")
    : null;
  const timeline = custody?.timeline ?? [];
  const anomalies = custody?.anomalies ?? [];
  const evidence = timeline.flatMap(evidenceFrom);
  const latest = timeline.at(-1);
  const analysed = timeline.some(
    (entry) => entry.eventType === "AnalyticsCompleted",
  );
  // An anomaly names the analytics result that raised it. The rule version
  // of that result is on the matching timeline event.
  const ruleVersionByResult = new Map(
    timeline.flatMap((entry) => {
      const resultId = detailText(entry.details, "analytics_result_id");
      const ruleVersion = detailText(entry.details, "rule_version");
      return resultId && ruleVersion ? [[resultId, ruleVersion] as const] : [];
    }),
  );

  return (
    <section data-testid="custody-view" className="max-w-4xl">
      <p className="mb-4 max-w-xl text-sm text-slate-600">
        Enter a batch ID to read its full history, from the donor request to
        completion. Completed batches can also be opened from the Impact tab.
        This screen is read-only.
      </p>
      <form
        onSubmit={(event) => void onSubmit(event)}
        className="mb-4 flex max-w-xl flex-wrap items-end gap-3"
      >
        <label className="grid min-w-0 flex-1 gap-1 text-sm text-slate-700">
          Batch ID
          <input
            data-testid="custody-batch-id"
            value={batchInput}
            onChange={(event) => {
              setBatchInput(event.target.value);
              setMissingId(false);
            }}
            className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
          />
        </label>
        <button
          type="submit"
          data-testid="custody-load"
          disabled={loading}
          className="rounded-md bg-teal-800 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
        >
          {loading ? "Loading…" : "Show custody"}
        </button>
      </form>
      {missingId ? (
        <Banner testId="custody-validation" tone="warning">
          Enter a batch ID.
        </Banner>
      ) : null}
      {loading ? (
        <LoadingLine testId="custody-loading">
          Loading custody history…
        </LoadingLine>
      ) : null}
      {loadBanner ? (
        <Banner testId={loadBanner.testId} tone={loadBanner.tone}>
          {loadBanner.text}
        </Banner>
      ) : null}

      {custody ? (
        <div data-testid="custody-detail" className="grid gap-6">
          <p className="text-sm text-slate-700">
            Batch <span className="break-all">{custody.batchId}</span>
            {latest ? ` is ${latest.toStatus}. ` : ". "}
            {timeline.length} {timeline.length === 1 ? "event" : "events"}{" "}
            recorded.
          </p>

          <div className="grid gap-2">
            <h2 className="text-lg font-semibold text-slate-900">Anomalies</h2>
            {anomalies.length === 0 ? (
              <Banner testId="custody-anomalies-none" tone="info">
                {analysed
                  ? "Analytics found no anomalies for this batch."
                  : "Analytics has not run for this batch yet, so no anomalies are stored."}
              </Banner>
            ) : (
              <div className="overflow-x-auto">
                <table
                  data-testid="custody-anomalies"
                  className="w-full text-left text-sm"
                >
                  <thead className="text-slate-500">
                    <tr>
                      <th className="py-2 pr-3">Anomaly</th>
                      <th className="py-2 pr-3">Declared</th>
                      <th className="py-2 pr-3">Actual</th>
                      <th className="py-2 pr-3">Difference (kg)</th>
                      <th className="py-2 pr-3">Detected</th>
                      <th className="py-2 pr-3">Policy version</th>
                      <th className="py-2">Source</th>
                    </tr>
                  </thead>
                  <tbody>
                    {anomalies.map((anomaly) => (
                      <tr
                        key={anomaly.anomalyId}
                        className="border-t border-slate-200 align-top"
                      >
                        <td className="py-2 pr-3 font-medium text-amber-950">
                          {anomalyLabel(anomaly.code)}
                        </td>
                        <td className="py-2 pr-3">
                          {anomaly.declaredValue ?? "—"}
                        </td>
                        <td className="py-2 pr-3">
                          {anomaly.actualValue ?? "—"}
                        </td>
                        <td className="py-2 pr-3">{anomaly.deltaKg ?? "—"}</td>
                        <td className="py-2 pr-3">
                          {formatWhen(anomaly.detectedAt)}
                        </td>
                        <td className="py-2 pr-3">
                          {ruleVersionByResult.get(anomaly.resultId) ?? "—"}
                        </td>
                        <td className="break-all py-2">
                          Analytics result {anomaly.resultId}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="grid gap-2">
            <h2 className="text-lg font-semibold text-slate-900">Evidence</h2>
            {downloadBanner ? (
              <Banner testId={downloadBanner.testId} tone={downloadBanner.tone}>
                {downloadBanner.text}
              </Banner>
            ) : null}
            {evidence.length === 0 ? (
              <Banner testId="custody-evidence-none" tone="info">
                No evidence was uploaded for this batch.
              </Banner>
            ) : (
              <div className="overflow-x-auto">
                <table
                  data-testid="custody-evidence"
                  className="w-full text-left text-sm"
                >
                  <thead className="text-slate-500">
                    <tr>
                      <th className="py-2 pr-3">Stage</th>
                      <th className="py-2 pr-3">Type</th>
                      <th className="py-2 pr-3">Size</th>
                      <th className="py-2 pr-3">SHA-256</th>
                      <th className="py-2 pr-3">Uploaded</th>
                      <th className="py-2 pr-3">Source</th>
                      <th className="py-2">Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {evidence.map((item) => (
                      <tr
                        key={item.evidenceId}
                        className="border-t border-slate-200 align-top"
                      >
                        <td className="py-2 pr-3">{item.stage ?? "—"}</td>
                        <td className="py-2 pr-3">{item.mimeType ?? "—"}</td>
                        <td className="py-2 pr-3 whitespace-nowrap">
                          {formatSize(item.sizeBytes)}
                        </td>
                        <td className="break-all py-2 pr-3">
                          {item.sha256Hash ?? "—"}
                        </td>
                        <td className="py-2 pr-3">
                          {formatWhen(item.uploadedAt)}
                        </td>
                        <td className="py-2 pr-3">{item.source}</td>
                        <td className="py-2">
                          <button
                            type="button"
                            data-testid={`custody-evidence-download-${item.evidenceId}`}
                            disabled={downloading !== null}
                            className="font-medium text-teal-800 disabled:opacity-50"
                            onClick={() => void onDownload(item)}
                          >
                            {downloading === item.evidenceId
                              ? "Downloading…"
                              : "Download"}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="grid gap-2">
            <h2 className="text-lg font-semibold text-slate-900">Timeline</h2>
            {timeline.length === 0 ? (
              <Banner testId="custody-timeline-none" tone="info">
                No events are recorded for this batch.
              </Banner>
            ) : (
              <ol data-testid="custody-timeline" className="grid gap-3">
                {timeline.map((entry) => (
                  <li
                    key={entry.auditId}
                    data-testid={`custody-event-${entry.auditId}`}
                    className="rounded-md border border-slate-200 bg-white p-3 text-sm"
                  >
                    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                      <h3 className="font-semibold text-slate-900">
                        {eventLabel(entry.eventType)}
                      </h3>
                      <time
                        dateTime={entry.occurredAt}
                        title={entry.occurredAt}
                        className="text-slate-600"
                      >
                        {formatWhen(entry.occurredAt)}
                      </time>
                    </div>
                    <p className="text-slate-600">
                      {entry.fromStatus === entry.toStatus
                        ? `Status ${entry.toStatus}`
                        : `${entry.fromStatus} → ${entry.toStatus}`}{" "}
                      · batch version {entry.batchVersion}
                    </p>
                    <dl className="mt-2 grid gap-x-6 gap-y-1 sm:grid-cols-2">
                      {eventRows(entry).map(([label, value]) => (
                        <div key={label} className="min-w-0">
                          <dt className="text-slate-500">{label}</dt>
                          <dd className="break-all text-slate-900">{value}</dd>
                        </div>
                      ))}
                    </dl>
                  </li>
                ))}
              </ol>
            )}
          </div>
        </div>
      ) : null}
    </section>
  );
}
