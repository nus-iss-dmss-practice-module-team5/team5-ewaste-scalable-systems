import type { Assignment } from "@/lib/workflow/types";
import {
  ASSIGNMENTS_BY_USER,
  ASSIGNMENTS_STORE,
  requestToPromise,
  withTransaction,
} from "./db";

/** One assignment as the collector last loaded it while online. */
export type SavedAssignment = {
  userId: string;
  assignmentId: string;
  assignment: Assignment;
  /**
   * The batch version a later command must send as If-Match-Version. Null
   * when neither the response nor this browser session knows it.
   */
  batchVersion: number | null;
  /** ISO time the list was saved on this device. */
  savedAt: string;
};

function batchVersionFor(
  assignment: Assignment,
  batchVersions: Record<string, number>,
): number | null {
  return assignment.batchVersion ?? batchVersions[assignment.batchId] ?? null;
}

/**
 * Replaces the user's saved assignments with the latest successful list.
 * `batchVersions` maps batch id to a version known from this session and is
 * used when the response has no batch_version.
 */
export async function saveAssignments(
  userId: string,
  assignments: Assignment[],
  batchVersions: Record<string, number> = {},
  now: Date = new Date(),
): Promise<SavedAssignment[]> {
  const savedAt = now.toISOString();
  const records: SavedAssignment[] = assignments.map((assignment) => ({
    userId,
    assignmentId: assignment.assignmentId,
    assignment,
    batchVersion: batchVersionFor(assignment, batchVersions),
    savedAt,
  }));
  await withTransaction(ASSIGNMENTS_STORE, "readwrite", async (transaction) => {
    const store = transaction.objectStore(ASSIGNMENTS_STORE);
    const keys = await requestToPromise(
      store.index(ASSIGNMENTS_BY_USER).getAllKeys(userId),
    );
    for (const key of keys) {
      store.delete(key);
    }
    for (const record of records) {
      store.put(record);
    }
  });
  return records;
}

export async function loadAssignments(
  userId: string,
): Promise<SavedAssignment[]> {
  return withTransaction(ASSIGNMENTS_STORE, "readonly", (transaction) =>
    requestToPromise<SavedAssignment[]>(
      transaction
        .objectStore(ASSIGNMENTS_STORE)
        .index(ASSIGNMENTS_BY_USER)
        .getAll(userId),
    ),
  );
}

export async function getSavedAssignment(
  userId: string,
  assignmentId: string,
): Promise<SavedAssignment | null> {
  const record = await withTransaction(
    ASSIGNMENTS_STORE,
    "readonly",
    (transaction) =>
      requestToPromise<SavedAssignment | undefined>(
        transaction
          .objectStore(ASSIGNMENTS_STORE)
          .get([userId, assignmentId]),
      ),
  );
  return record ?? null;
}

export async function clearAssignments(userId: string): Promise<void> {
  await withTransaction(ASSIGNMENTS_STORE, "readwrite", async (transaction) => {
    const store = transaction.objectStore(ASSIGNMENTS_STORE);
    const keys = await requestToPromise(
      store.index(ASSIGNMENTS_BY_USER).getAllKeys(userId),
    );
    for (const key of keys) {
      store.delete(key);
    }
  });
}

/** The newest save time among records, or null when there are none. */
export function latestSavedAt(records: SavedAssignment[]): string | null {
  return records.reduce<string | null>(
    (latest, record) =>
      latest === null || record.savedAt > latest ? record.savedAt : latest,
    null,
  );
}
