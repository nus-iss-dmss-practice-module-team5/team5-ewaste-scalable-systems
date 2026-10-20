"use client";

import { useSession } from "@/lib/auth/session-context";
import { getOpportunity, listOpportunities } from "@/lib/workflow/api";
import { isWorkflowError } from "@/lib/workflow/errors";
import type { Opportunity } from "@/lib/workflow/types";
import { useEffect, useState } from "react";
import { USE_LOCAL_OPPORTUNITY_MOCK } from "@/lib/workflow/local-opportunity-mock";
import { Banner, LoadingLine, bannerForError, formatWhen } from "./workflow-ui";

export function OpportunityView() {
  const { session } = useSession();
  const [rows, setRows] = useState<Opportunity[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [selected, setSelected] = useState<Opportunity | null>(null);
  const [detailError, setDetailError] = useState<unknown>(null);
  const [stale, setStale] = useState(false);
  const [loadingDetail, setLoadingDetail] = useState(false);

  async function reload() {
    if (!session) {
      return;
    }
    setLoadError(null);
    setRows(null);
    setStale(false);
    try {
      const page = await listOpportunities(session.tokens.accessToken);
      setRows(page.data);
    } catch (error) {
      setLoadError(error);
      setRows([]);
    }
  }

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void reload();
    }, 0);
    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.tokens.accessToken]);

  async function openDetail(batchId: string) {
    if (!session) {
      return;
    }
    setLoadingDetail(true);
    setDetailError(null);
    setStale(false);
    try {
      const detail = await getOpportunity(session.tokens.accessToken, batchId);
      setSelected(detail);
    } catch (error) {
      setSelected(null);
      setDetailError(error);
      if (isWorkflowError(error) && error.kind === "not_found") {
        setStale(true);
      }
    } finally {
      setLoadingDetail(false);
    }
  }

  const loadBanner = loadError
    ? bannerForError(loadError, "opportunity-list")
    : null;
  const detailBanner = detailError
    ? bannerForError(detailError, "opportunity-detail")
    : null;

  return (
    <section data-testid="opportunity-view" className="max-w-4xl">
      <p className="mb-4 max-w-xl text-sm text-slate-600">
        Eligible opportunities for your organisation. This screen only reads
        matching results.
      </p>
      {USE_LOCAL_OPPORTUNITY_MOCK ? (
        <Banner testId="opportunity-local-mock" tone="info">
          Local mock data. View opens the detail. This screen does not claim.
        </Banner>
      ) : null}
      <button
        type="button"
        data-testid="opportunity-refresh"
        onClick={() => void reload()}
        className="mb-4 text-sm font-medium text-teal-800"
      >
        Refresh
      </button>
      {rows === null ? (
        <LoadingLine testId="opportunity-loading">
          Loading opportunities…
        </LoadingLine>
      ) : null}
      {loadBanner ? (
        <Banner testId={loadBanner.testId} tone={loadBanner.tone}>
          {loadBanner.text}
        </Banner>
      ) : null}
      {rows && rows.length === 0 && !loadError ? (
        <Banner testId="opportunity-empty" tone="info">
          No eligible opportunities. An empty list is the in-progress signal
          while matching has not produced a row in your scope.
        </Banner>
      ) : null}
      {stale ? (
        <Banner testId="opportunity-stale" tone="warning">
          That result is stale. It is missing or hidden, so it is no longer a
          current match.
        </Banner>
      ) : null}
      {detailBanner && !stale ? (
        <Banner testId={detailBanner.testId} tone={detailBanner.tone}>
          {detailBanner.text}
        </Banner>
      ) : null}
      {loadingDetail ? (
        <LoadingLine testId="opportunity-detail-loading">
          Loading opportunity…
        </LoadingLine>
      ) : null}
      {rows && rows.length > 0 ? (
        <table className="mt-2 w-full text-left text-sm">
          <thead className="text-slate-500">
            <tr>
              <th className="py-2 pr-3">Category</th>
              <th className="py-2 pr-3">Status</th>
              <th className="py-2 pr-3">Zone</th>
              <th className="py-2 pr-3">Deadline</th>
              <th className="py-2"> </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.batchId} className="border-t border-slate-200">
                <td className="py-2 pr-3">{row.category}</td>
                <td className="py-2 pr-3">{row.status}</td>
                <td className="py-2 pr-3">{row.zone}</td>
                <td className="py-2 pr-3">
                  {formatWhen(row.collectionDeadline)}
                </td>
                <td className="py-2">
                  <button
                    type="button"
                    data-testid={`opportunity-open-${row.batchId}`}
                    className="font-medium text-teal-800"
                    onClick={() => void openDetail(row.batchId)}
                  >
                    View
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {selected ? (
        <article
          data-testid="opportunity-detail"
          className="mt-6 max-w-xl rounded-md border border-slate-200 bg-white p-4 text-sm"
        >
          <h2 className="text-lg font-semibold text-slate-900">
            {selected.category}
          </h2>
          <p className="mt-2 text-slate-600">Status {selected.status}</p>
          <p className="text-slate-600">Quantity {selected.quantity}</p>
          <p className="text-slate-600">Zone {selected.zone}</p>
          <p className="text-slate-600">
            Deadline {formatWhen(selected.collectionDeadline)}
          </p>
          <p className="mt-2 text-slate-700">{selected.eligibilityReason}</p>
        </article>
      ) : null}
    </section>
  );
}
