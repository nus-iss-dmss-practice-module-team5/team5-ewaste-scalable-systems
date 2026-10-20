"use client";

import { useSession } from "@/lib/auth/session-context";
import { getImpact } from "@/lib/workflow/api";
import {
  EWASTE_CATEGORIES,
  type ImpactFilter,
  type ImpactReport,
} from "@/lib/workflow/types";
import { FormEvent, useEffect, useRef, useState } from "react";
import {
  Banner,
  LoadingLine,
  anomalyLabel,
  bannerForError,
  formatWhen,
} from "./workflow-ui";

const MAX_ORG_ID_LENGTH = 32;
const INPUT_CLASS =
  "w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm";

// A null weight was never recorded. It is not zero.
function weight(value: string | null): string {
  return value === null ? "Not recorded" : `${value} kg`;
}

// Built from the filter the API echoes back, so it describes the totals that
// are on screen and not edits that have not been applied.
function scopeText(filter: ImpactFilter): string {
  const parts: string[] = [];
  if (filter.completedFrom && filter.completedTo) {
    parts.push(
      `completed ${filter.completedFrom} to ${filter.completedTo} (UTC)`,
    );
  } else if (filter.completedFrom) {
    parts.push(`completed on or after ${filter.completedFrom} (UTC)`);
  } else if (filter.completedTo) {
    parts.push(`completed on or before ${filter.completedTo} (UTC)`);
  }
  if (filter.category) {
    parts.push(`category ${filter.category}`);
  }
  if (filter.processingOrgId) {
    parts.push(`facility ${filter.processingOrgId}`);
  }
  return parts.length > 0 ? parts.join(" · ") : "all completed batches";
}

export function ImpactView({
  onOpenCustody,
}: {
  onOpenCustody?: (batchId: string) => void;
}) {
  const { session } = useSession();
  const [completedFrom, setCompletedFrom] = useState("");
  const [completedTo, setCompletedTo] = useState("");
  const [category, setCategory] = useState("");
  const [processingOrgId, setProcessingOrgId] = useState("");
  const [report, setReport] = useState<ImpactReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [invalid, setInvalid] = useState<string | null>(null);
  // The filter of the last request, reused when the session is renewed.
  const applied = useRef<ImpactFilter>({});

  async function load(filter: ImpactFilter) {
    if (!session) {
      return;
    }
    applied.current = filter;
    setLoading(true);
    setLoadError(null);
    try {
      setReport(await getImpact(session.tokens.accessToken, filter));
    } catch (error) {
      setLoadError(error);
      setReport(null);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void load(applied.current);
    }, 0);
    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.tokens.accessToken]);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (completedFrom && completedTo && completedFrom > completedTo) {
      setInvalid("The start date cannot be after the end date.");
      return;
    }
    setInvalid(null);
    const facility = processingOrgId.trim();
    await load({
      ...(completedFrom ? { completedFrom } : {}),
      ...(completedTo ? { completedTo } : {}),
      ...(category ? { category } : {}),
      ...(facility ? { processingOrgId: facility } : {}),
    });
  }

  async function onClear() {
    setCompletedFrom("");
    setCompletedTo("");
    setCategory("");
    setProcessingOrgId("");
    setInvalid(null);
    await load({});
  }

  const loadBanner = loadError ? bannerForError(loadError, "impact") : null;
  const totals = report?.totals;

  return (
    <section data-testid="impact-view" className="max-w-5xl">
      <p className="mb-4 max-w-xl text-sm text-slate-600">
        Totals for COMPLETED batches. Each batch counts once. Weights are based
        on what the facility received.
      </p>
      <form
        onSubmit={(event) => void onSubmit(event)}
        className="mb-4 grid max-w-4xl gap-3"
      >
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <label className="grid gap-1 text-sm text-slate-700">
            Completed from (UTC)
            <input
              data-testid="impact-completed-from"
              type="date"
              value={completedFrom}
              onChange={(event) => {
                setCompletedFrom(event.target.value);
                setInvalid(null);
              }}
              className={INPUT_CLASS}
            />
          </label>
          <label className="grid gap-1 text-sm text-slate-700">
            Completed to (UTC)
            <input
              data-testid="impact-completed-to"
              type="date"
              value={completedTo}
              onChange={(event) => {
                setCompletedTo(event.target.value);
                setInvalid(null);
              }}
              className={INPUT_CLASS}
            />
          </label>
          <label className="grid gap-1 text-sm text-slate-700">
            Received category
            <select
              data-testid="impact-category"
              value={category}
              onChange={(event) => setCategory(event.target.value)}
              className={INPUT_CLASS}
            >
              <option value="">All categories</option>
              {EWASTE_CATEGORIES.map((item) => (
                <option key={item} value={item}>
                  {item}
                </option>
              ))}
            </select>
          </label>
          <label className="grid gap-1 text-sm text-slate-700">
            Processing facility ID
            <input
              data-testid="impact-processing-org"
              value={processingOrgId}
              maxLength={MAX_ORG_ID_LENGTH}
              onChange={(event) => setProcessingOrgId(event.target.value)}
              placeholder="All facilities"
              className={INPUT_CLASS}
            />
          </label>
        </div>
        <div className="flex flex-wrap items-center gap-4">
          <button
            type="submit"
            data-testid="impact-apply"
            disabled={loading}
            className="rounded-md bg-teal-800 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
          >
            {loading ? "Loading…" : "Apply filters"}
          </button>
          <button
            type="button"
            data-testid="impact-clear"
            disabled={loading}
            className="text-sm font-medium text-teal-800 disabled:opacity-50"
            onClick={() => void onClear()}
          >
            Clear filters
          </button>
        </div>
      </form>
      {invalid ? (
        <Banner testId="impact-validation" tone="warning">
          {invalid}
        </Banner>
      ) : null}
      {loading ? (
        <LoadingLine testId="impact-loading">Loading impact…</LoadingLine>
      ) : null}
      {loadBanner ? (
        <Banner testId={loadBanner.testId} tone={loadBanner.tone}>
          {loadBanner.text}
        </Banner>
      ) : null}

      {report && totals ? (
        <div className="grid gap-6">
          <p data-testid="impact-scope" className="text-sm text-slate-700">
            Showing {scopeText(report.filter)}.
          </p>
          {totals.completedBatchCount === 0 ? (
            <Banner testId="impact-empty" tone="info">
              No completed batches match these filters.
            </Banner>
          ) : (
            <>
              <dl
                data-testid="impact-totals"
                className="grid grid-cols-2 gap-x-6 gap-y-3 text-sm sm:grid-cols-4"
              >
                {[
                  [
                    "completed",
                    "Completed batches",
                    totals.completedBatchCount,
                  ],
                  ["received", "Received", weight(totals.receivedKg)],
                  ["reused", "Reused", weight(totals.reusedKg)],
                  ["recycled", "Recycled", weight(totals.recycledKg)],
                  ["disposed", "Disposed", weight(totals.disposedKg)],
                  ["diverted", "Diverted", weight(totals.divertedKg)],
                  ["unknown", "Unknown", weight(totals.unknownKg)],
                  [
                    "policy",
                    "Policy version",
                    totals.ruleVersions.join(", ") || "—",
                  ],
                ].map(([id, label, value]) => (
                  <div key={id} data-testid={`impact-total-${id}`}>
                    <dt className="text-slate-500">{label}</dt>
                    <dd className="break-words text-lg font-semibold text-slate-900">
                      {value}
                    </dd>
                  </div>
                ))}
              </dl>
              <p
                data-testid="impact-quality"
                className="text-sm text-slate-700"
              >
                Outcome data: {totals.completeBatchCount} complete,{" "}
                {totals.partialBatchCount} partial,{" "}
                {totals.missingOutcomeBatchCount} with no recorded outcome.
              </p>
              {totals.missingOutcomeBatchCount > 0 ? (
                <Banner testId="impact-missing-outcome" tone="warning">
                  {totals.missingOutcomeBatchCount === 1
                    ? "1 batch has"
                    : `${totals.missingOutcomeBatchCount} batches have`}{" "}
                  no recorded outcome. Their received weight is counted as
                  unknown and adds nothing to the reused, recycled, disposed or
                  diverted totals.
                </Banner>
              ) : null}
              <div className="overflow-x-auto">
                <table
                  data-testid="impact-items"
                  className="w-full text-left text-sm"
                >
                  <thead className="text-slate-500">
                    <tr>
                      <th className="py-2 pr-3">Batch</th>
                      <th className="py-2 pr-3">Completed</th>
                      <th className="py-2 pr-3">Received</th>
                      <th className="py-2 pr-3">Reused</th>
                      <th className="py-2 pr-3">Recycled</th>
                      <th className="py-2 pr-3">Disposed</th>
                      <th className="py-2 pr-3">Unknown</th>
                      <th className="py-2 pr-3">Outcome data</th>
                      <th className="py-2 pr-3">Anomalies</th>
                      <th className="py-2 pr-3">Policy version</th>
                      <th className="py-2">Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.items.map((item) => (
                      <tr
                        key={item.resultId}
                        data-testid={`impact-item-${item.batchId}`}
                        className="border-t border-slate-200 align-top"
                      >
                        <td className="break-all py-2 pr-3">{item.batchId}</td>
                        <td className="py-2 pr-3">
                          {formatWhen(item.acknowledgedAt)}
                        </td>
                        {[
                          item.receivedKg,
                          item.reusedKg,
                          item.recycledKg,
                          item.disposedKg,
                          item.unknownKg,
                        ].map((value, index) => (
                          <td
                            key={index}
                            className="py-2 pr-3 whitespace-nowrap"
                          >
                            {weight(value)}
                          </td>
                        ))}
                        <td className="py-2 pr-3">{item.dataQuality}</td>
                        <td className="py-2 pr-3">
                          {item.anomalyCodes.length > 0
                            ? item.anomalyCodes.map(anomalyLabel).join(", ")
                            : "None"}
                        </td>
                        <td className="py-2 pr-3">{item.ruleVersion}</td>
                        <td className="py-2">
                          {onOpenCustody ? (
                            <button
                              type="button"
                              data-testid={`impact-custody-${item.batchId}`}
                              className="font-medium whitespace-nowrap text-teal-800"
                              onClick={() => onOpenCustody(item.batchId)}
                            >
                              View custody
                            </button>
                          ) : null}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </div>
      ) : null}
    </section>
  );
}
