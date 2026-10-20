"use client";

import { useSession } from "@/lib/auth/session-context";
import {
  createBatchDraft,
  editBatchDraft,
  listBatches,
  newIdempotencyKey,
  submitBatch,
} from "@/lib/workflow/api";
import { USE_LOCAL_BATCH_MOCK } from "@/lib/workflow/local-batch-mock";
import { isWorkflowError } from "@/lib/workflow/errors";
import type { Batch, BatchDraftRequest } from "@/lib/workflow/types";
import { FormEvent, useEffect, useRef, useState } from "react";
import {
  Banner,
  LoadingLine,
  bannerForError,
  formatWhen,
  toLocalInput,
  toUtcIso,
} from "./workflow-ui";

type DraftFields = {
  category: string;
  quantity: string;
  estimatedWeightKg: string;
  conditionRating: string;
  isDataBearing: boolean;
  zone: string;
  collectionDeadline: string;
  notes: string;
};

// Kept until the command succeeds or its payload changes, so retrying after a
// timeout replays the same command instead of sending a new one.
function useRetryKey() {
  const attempt = useRef<{ fingerprint: string; key: string } | null>(null);
  return {
    keyFor(fingerprint: string): string {
      if (attempt.current?.fingerprint !== fingerprint) {
        attempt.current = { fingerprint, key: newIdempotencyKey() };
      }
      return attempt.current.key;
    },
    clear() {
      attempt.current = null;
    },
  };
}

const MAX_QUANTITY = 100000;

const EMPTY_FIELDS: DraftFields = {
  category: "",
  quantity: "",
  estimatedWeightKg: "",
  conditionRating: "",
  isDataBearing: false,
  zone: "",
  collectionDeadline: "",
  notes: "",
};

function fieldsFromBatch(batch: Batch): DraftFields {
  return {
    category: batch.category ?? "",
    quantity: batch.quantity === undefined ? "" : String(batch.quantity),
    estimatedWeightKg:
      batch.estimatedWeightKg === undefined
        ? ""
        : String(batch.estimatedWeightKg),
    conditionRating: batch.conditionRating ?? "",
    isDataBearing: batch.isDataBearing ?? false,
    zone: batch.zone ?? "",
    collectionDeadline: toLocalInput(batch.collectionDeadline),
    notes: batch.notes ?? "",
  };
}

function parseDraft(
  fields: DraftFields,
): { body: BatchDraftRequest } | { error: string } {
  const body: BatchDraftRequest = { isDataBearing: fields.isDataBearing };
  const category = fields.category.trim();
  if (category) body.category = category;
  if (fields.quantity.trim()) {
    const quantity = Number(fields.quantity);
    if (
      !Number.isInteger(quantity) ||
      quantity < 1 ||
      quantity > MAX_QUANTITY
    ) {
      return {
        error: `Quantity must be a whole number from 1 to ${MAX_QUANTITY}.`,
      };
    }
    body.quantity = quantity;
  }
  if (fields.estimatedWeightKg.trim()) {
    const estimatedWeightKg = Number(fields.estimatedWeightKg);
    if (!Number.isFinite(estimatedWeightKg) || estimatedWeightKg < 0.1) {
      return { error: "Estimated weight must be at least 0.1 kg." };
    }
    body.estimatedWeightKg = estimatedWeightKg;
  }
  const conditionRating = fields.conditionRating.trim();
  if (conditionRating) body.conditionRating = conditionRating;
  const zone = fields.zone.trim();
  if (zone) body.zone = zone;
  if (fields.collectionDeadline.trim()) {
    const collectionDeadline = toUtcIso(fields.collectionDeadline);
    if (!collectionDeadline) {
      return { error: "Collection deadline is invalid." };
    }
    body.collectionDeadline = collectionDeadline;
  }
  const notes = fields.notes.trim();
  if (notes) body.notes = notes;
  return { body };
}

function completenessError(batch: Batch): string | null {
  if (
    !batch.category?.trim() ||
    !batch.conditionRating?.trim() ||
    !batch.zone?.trim()
  ) {
    return "Category, condition, and zone are required before submit.";
  }
  if (
    batch.quantity === undefined ||
    !Number.isInteger(batch.quantity) ||
    batch.quantity < 1 ||
    batch.quantity > MAX_QUANTITY
  ) {
    return `Quantity must be a whole number from 1 to ${MAX_QUANTITY} before submit.`;
  }
  if (
    batch.estimatedWeightKg === undefined ||
    !Number.isFinite(batch.estimatedWeightKg) ||
    batch.estimatedWeightKg < 0.1
  ) {
    return "Estimated weight must be at least 0.1 kg before submit.";
  }
  if (!batch.collectionDeadline) {
    return "Collection deadline is required before submit.";
  }
  return null;
}

function DraftForm({
  title,
  fields,
  setFields,
  pending,
  onSubmit,
  submitLabel,
  error,
}: {
  title: string;
  fields: DraftFields;
  setFields: (fields: DraftFields) => void;
  pending: boolean;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  submitLabel: string;
  error: string | null;
}) {
  return (
    <form
      onSubmit={onSubmit}
      className="mt-4 grid w-full min-w-0 max-w-xl gap-3"
      data-testid="donor-form"
    >
      <h2 className="break-words text-lg font-semibold text-slate-900">
        {title}
      </h2>
      {error ? (
        <Banner testId="donor-form-error" tone="warning">
          {error}
        </Banner>
      ) : null}
      <label className="grid min-w-0 gap-1 text-sm text-slate-700">
        Category
        <input
          data-testid="donor-category"
          value={fields.category}
          onChange={(event) =>
            setFields({ ...fields, category: event.target.value })
          }
          className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2"
        />
      </label>
      <label className="grid min-w-0 gap-1 text-sm text-slate-700">
        Quantity
        <input
          data-testid="donor-quantity"
          type="number"
          min={1}
          max={MAX_QUANTITY}
          step={1}
          value={fields.quantity}
          onChange={(event) =>
            setFields({ ...fields, quantity: event.target.value })
          }
          className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2"
        />
      </label>
      <label className="grid min-w-0 gap-1 text-sm text-slate-700">
        Estimated weight (kg)
        <input
          data-testid="donor-weight"
          type="number"
          min={0.1}
          step="0.1"
          value={fields.estimatedWeightKg}
          onChange={(event) =>
            setFields({ ...fields, estimatedWeightKg: event.target.value })
          }
          className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2"
        />
      </label>
      <label className="grid min-w-0 gap-1 text-sm text-slate-700">
        Condition
        <input
          data-testid="donor-condition"
          value={fields.conditionRating}
          onChange={(event) =>
            setFields({ ...fields, conditionRating: event.target.value })
          }
          className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2"
        />
      </label>
      <label className="flex items-center gap-2 text-sm text-slate-700">
        <input
          data-testid="donor-data-bearing"
          type="checkbox"
          checked={fields.isDataBearing}
          onChange={(event) =>
            setFields({ ...fields, isDataBearing: event.target.checked })
          }
        />
        Data bearing
      </label>
      <label className="grid min-w-0 gap-1 text-sm text-slate-700">
        Zone
        <input
          data-testid="donor-zone"
          value={fields.zone}
          onChange={(event) =>
            setFields({ ...fields, zone: event.target.value })
          }
          className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2"
        />
      </label>
      <label className="grid min-w-0 gap-1 text-sm text-slate-700">
        Collection deadline
        <input
          data-testid="donor-deadline"
          type="datetime-local"
          value={fields.collectionDeadline}
          onChange={(event) =>
            setFields({ ...fields, collectionDeadline: event.target.value })
          }
          className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2"
        />
      </label>
      <label className="grid min-w-0 gap-1 text-sm text-slate-700">
        Notes
        <textarea
          data-testid="donor-notes"
          value={fields.notes}
          onChange={(event) =>
            setFields({ ...fields, notes: event.target.value })
          }
          className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2"
          maxLength={500}
        />
      </label>
      <button
        type="submit"
        data-testid="donor-save"
        disabled={pending}
        className="w-fit rounded-md bg-teal-800 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
      >
        {pending ? "Saving…" : submitLabel}
      </button>
    </form>
  );
}

export function DonorBatchList() {
  const { session } = useSession();
  const [batches, setBatches] = useState<Batch[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [editing, setEditing] = useState<Batch | null>(null);
  const [fields, setFields] = useState<DraftFields>(EMPTY_FIELDS);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const editKey = useRetryKey();
  const submitKey = useRetryKey();

  async function reload() {
    if (!session) {
      return;
    }
    setLoadError(null);
    setBatches(null);
    try {
      const page = await listBatches(session.tokens.accessToken);
      setBatches(page.data);
    } catch (error) {
      setLoadError(error);
      setBatches([]);
    }
  }

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void reload();
    }, 0);
    return () => window.clearTimeout(timer);
    // Session identity is enough; reload closes over the current token.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.tokens.accessToken]);

  async function onSaveEdit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session || !editing) {
      return;
    }
    const parsed = parseDraft(fields);
    if ("error" in parsed) {
      setActionError(parsed.error);
      return;
    }
    setPending(true);
    setActionError(null);
    try {
      const updated = await editBatchDraft(
        session.tokens.accessToken,
        editing.batchId,
        editing.version,
        parsed.body,
        editKey.keyFor(
          JSON.stringify([editing.batchId, editing.version, parsed.body]),
        ),
      );
      editKey.clear();
      setNotice(
        `Draft ${updated.batchId} saved at version ${updated.version}.`,
      );
      setEditing(null);
      await reload();
    } catch (error) {
      setActionError(
        isWorkflowError(error)
          ? error.message
          : "The draft could not be saved.",
      );
    } finally {
      setPending(false);
    }
  }

  async function onSubmit(batch: Batch) {
    if (!session) {
      return;
    }
    const incomplete = completenessError(batch);
    if (incomplete) {
      setActionError(incomplete);
      setNotice(null);
      return;
    }
    setPending(true);
    setActionError(null);
    setNotice(null);
    try {
      const submitted = await submitBatch(
        session.tokens.accessToken,
        batch.batchId,
        batch.version,
        submitKey.keyFor(JSON.stringify([batch.batchId, batch.version])),
      );
      submitKey.clear();
      setNotice(`Batch submitted. Status is ${submitted.status}.`);
      await reload();
    } catch (error) {
      const banner = bannerForError(error, "donor-submit");
      setActionError(banner.text);
    } finally {
      setPending(false);
    }
  }

  const loadBanner = loadError ? bannerForError(loadError, "donor-list") : null;

  return (
    <section data-testid="donor-list" className="min-w-0 max-w-4xl">
      <p className="mb-4 max-w-xl text-sm text-slate-600">
        Drafts can be edited and submitted. Submitted batches stay visible and
        cannot be changed from this screen.
      </p>
      {USE_LOCAL_BATCH_MOCK ? (
        <Banner testId="donor-local-mock" tone="info">
          Local mock data. The draft row can be edited and submitted. The other
          rows stay read only.
        </Banner>
      ) : null}
      {notice ? (
        <Banner testId="donor-notice" tone="success">
          {notice}
        </Banner>
      ) : null}
      {actionError ? (
        <Banner testId="donor-action-error" tone="warning">
          {actionError}
        </Banner>
      ) : null}
      {batches === null ? (
        <LoadingLine testId="donor-loading">Loading batches…</LoadingLine>
      ) : null}
      {loadBanner ? (
        <Banner testId={loadBanner.testId} tone={loadBanner.tone}>
          {loadBanner.text}
        </Banner>
      ) : null}
      {batches && batches.length === 0 && !loadError ? (
        <Banner testId="donor-empty" tone="info">
          No batches yet. Use New request to create a draft.
        </Banner>
      ) : null}
      {batches && batches.length > 0 ? (
        <div className="mt-4 min-w-0 overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="text-slate-500">
              <tr>
                <th className="py-2 pr-3">Category</th>
                <th className="py-2 pr-3">Status</th>
                <th className="py-2 pr-3">Zone</th>
                <th className="py-2 pr-3">Deadline</th>
                <th className="py-2">Actions</th>
              </tr>
            </thead>
            <tbody>
              {batches.map((batch) => (
                <tr
                  key={batch.batchId}
                  className="border-t border-slate-200"
                  data-testid={`donor-row-${batch.batchId}`}
                >
                  <td className="py-2 pr-3">{batch.category ?? "—"}</td>
                  <td className="py-2 pr-3">{batch.status}</td>
                  <td className="py-2 pr-3">{batch.zone ?? "—"}</td>
                  <td className="py-2 pr-3">
                    {formatWhen(batch.collectionDeadline)}
                  </td>
                  <td className="py-2">
                    {batch.status === "DRAFT" ? (
                      <span className="flex gap-3">
                        <button
                          type="button"
                          className="font-medium text-teal-800"
                          onClick={() => {
                            setEditing(batch);
                            setFields(fieldsFromBatch(batch));
                            setActionError(null);
                          }}
                        >
                          Edit
                        </button>
                        <button
                          type="button"
                          data-testid={`donor-submit-${batch.batchId}`}
                          className="font-medium text-teal-800"
                          disabled={pending}
                          onClick={() => void onSubmit(batch)}
                        >
                          Submit
                        </button>
                      </span>
                    ) : (
                      <span className="text-slate-500">Read only</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      {editing ? (
        <DraftForm
          title={`Edit draft ${editing.batchId}`}
          fields={fields}
          setFields={setFields}
          pending={pending}
          onSubmit={(event) => void onSaveEdit(event)}
          submitLabel="Save draft"
          error={null}
        />
      ) : null}
    </section>
  );
}

export function DonorBatchForm() {
  const { session } = useSession();
  const [fields, setFields] = useState<DraftFields>(EMPTY_FIELDS);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<Batch | null>(null);
  const createKey = useRetryKey();

  async function onCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session) {
      return;
    }
    const parsed = parseDraft(fields);
    if ("error" in parsed) {
      setError(parsed.error);
      setCreated(null);
      return;
    }
    setPending(true);
    setError(null);
    try {
      const batch = await createBatchDraft(
        session.tokens.accessToken,
        parsed.body,
        createKey.keyFor(JSON.stringify(parsed.body)),
      );
      createKey.clear();
      setCreated(batch);
      setFields(EMPTY_FIELDS);
    } catch (caught) {
      setCreated(null);
      setError(
        isWorkflowError(caught)
          ? caught.message
          : "The draft could not be created.",
      );
    } finally {
      setPending(false);
    }
  }

  return (
    <section data-testid="donor-create" className="min-w-0 max-w-xl">
      {created ? (
        <Banner testId="donor-created" tone="success">
          Draft {created.batchId} created with status {created.status}.
        </Banner>
      ) : null}
      <DraftForm
        title="New collection request"
        fields={fields}
        setFields={setFields}
        pending={pending}
        onSubmit={(event) => void onCreate(event)}
        submitLabel="Create draft"
        error={error}
      />
    </section>
  );
}
