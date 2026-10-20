"use client";

import { useSession } from "@/lib/auth/session-context";
import {
  claimOpportunity,
  listOpportunities,
  newIdempotencyKey,
} from "@/lib/workflow/api";
import { USE_LOCAL_CLAIM_MOCK } from "@/lib/workflow/local-claim-mock";
import type { ClaimResult, Opportunity } from "@/lib/workflow/types";
import { useEffect, useRef, useState } from "react";
import { Banner, LoadingLine, bannerForError, formatWhen } from "./workflow-ui";

function claimBlockReason(opportunity: Opportunity): string | null {
  if (opportunity.status !== "MATCHED") {
    return "Only a MATCHED opportunity can be claimed.";
  }
  if (opportunity.version === undefined || !opportunity.claimEpoch) {
    return "This opportunity has no claim version yet, so the claim cannot be sent.";
  }
  return null;
}

export function ClaimAction() {
  const { session } = useSession();
  const [rows, setRows] = useState<Opportunity[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [notes, setNotes] = useState("");
  const [pending, setPending] = useState(false);
  const [actionError, setActionError] = useState<ReturnType<
    typeof bannerForError
  > | null>(null);
  const [result, setResult] = useState<ClaimResult | null>(null);
  // Kept until the claim succeeds or its payload changes, so retrying after a
  // timeout replays the same command instead of sending a new one.
  const claimAttempt = useRef<{ fingerprint: string; key: string } | null>(
    null,
  );

  const selected = rows?.find((row) => row.batchId === selectedId) ?? null;
  const blockReason = selected ? claimBlockReason(selected) : null;

  async function reload() {
    if (!session) {
      return;
    }
    setLoadError(null);
    setRows(null);
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

  async function onClaim() {
    if (
      !session ||
      !selected ||
      selected.version === undefined ||
      !selected.claimEpoch
    ) {
      return;
    }
    const fingerprint = JSON.stringify([
      selected.batchId,
      selected.version,
      selected.claimEpoch,
      notes.trim(),
    ]);
    if (claimAttempt.current?.fingerprint !== fingerprint) {
      claimAttempt.current = { fingerprint, key: newIdempotencyKey() };
    }
    const idempotencyKey = claimAttempt.current.key;
    setPending(true);
    setResult(null);
    setActionError(null);
    try {
      const claimed = await claimOpportunity(
        session.tokens.accessToken,
        selected.batchId,
        {
          expectedVersion: selected.version,
          claimEpoch: selected.claimEpoch,
          notes,
        },
        idempotencyKey,
      );
      claimAttempt.current = null;
      setResult(claimed);
    } catch (error) {
      setActionError(bannerForError(error, "claim"));
    } finally {
      setPending(false);
    }
  }

  const loadBanner = loadError ? bannerForError(loadError, "claim-list") : null;

  return (
    <section data-testid="claim-view" className="max-w-4xl">
      <p className="mb-4 max-w-xl text-sm text-slate-600">
        Claim confirms the opportunity and assigns the next available collector.
      </p>
      {USE_LOCAL_CLAIM_MOCK ? (
        <div className="mb-4">
          <Banner testId="claim-local-mock" tone="info">
            Local mock data. Laptops can be claimed. Monitors are stale and
            conflict. Phones are already approved.
          </Banner>
        </div>
      ) : null}
      {rows === null ? (
        <LoadingLine testId="claim-loading">Loading opportunities…</LoadingLine>
      ) : null}
      {loadBanner ? (
        <Banner testId={loadBanner.testId} tone={loadBanner.tone}>
          {loadBanner.text}
        </Banner>
      ) : null}
      {rows && rows.length === 0 && !loadError ? (
        <Banner testId="claim-empty" tone="info">
          No opportunities are available to claim.
        </Banner>
      ) : null}
      {pending ? (
        <LoadingLine testId="claim-pending">Claim in progress…</LoadingLine>
      ) : null}
      {result ? (
        <Banner testId="claim-success" tone="success">
          Claim confirmed. Batch {result.batchId} is {result.status}. It leaves
          Opportunities.
          {result.status === "ASSIGNED"
            ? " Collector acceptance is pending."
            : ""}
        </Banner>
      ) : null}
      {actionError ? (
        <Banner testId={actionError.testId} tone={actionError.tone}>
          {actionError.text}
        </Banner>
      ) : null}
      {rows && rows.length > 0 ? (
        <ul className="mt-2 divide-y divide-slate-200 border-y border-slate-200 text-sm">
          {rows.map((row) => {
            const reason = claimBlockReason(row);
            return (
              <li
                key={row.batchId}
                className="flex items-center justify-between gap-4 py-3"
              >
                <span>
                  {row.category} · {row.status} · {row.zone} ·{" "}
                  {formatWhen(row.collectionDeadline)}
                </span>
                <button
                  type="button"
                  data-testid={`claim-select-${row.batchId}`}
                  className="font-medium text-teal-800"
                  onClick={() => {
                    setSelectedId(row.batchId);
                    setResult(null);
                    setActionError(null);
                  }}
                >
                  Select
                </button>
                {reason ? <span className="sr-only">{reason}</span> : null}
              </li>
            );
          })}
        </ul>
      ) : null}
      {selected ? (
        <div className="mt-4 max-w-xl" data-testid="claim-detail">
          {blockReason ? (
            <Banner testId="claim-blocked" tone="warning">
              {blockReason}
            </Banner>
          ) : (
            <>
              <label className="grid gap-1 text-sm text-slate-700">
                Notes
                <input
                  data-testid="claim-notes"
                  value={notes}
                  onChange={(event) => setNotes(event.target.value)}
                  className="rounded-md border border-slate-300 px-3 py-2"
                  maxLength={255}
                />
              </label>
              <button
                type="button"
                data-testid="claim-submit"
                disabled={pending}
                onClick={() => void onClaim()}
                className="mt-3 rounded-md bg-teal-800 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
              >
                {pending ? "Claiming…" : "Claim"}
              </button>
            </>
          )}
        </div>
      ) : null}
    </section>
  );
}
