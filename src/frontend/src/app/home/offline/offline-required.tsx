/** Inline notice for an action that is not available offline (EWSS-26). */
export function OfflineRequired({ action }: { action?: string }) {
  return (
    <p
      role="note"
      data-testid="collector-offline-required"
      className="text-sm text-slate-600"
    >
      {action ? (
        <span className="font-medium text-slate-800">{action}: </span>
      ) : null}
      This needs a connection.
    </p>
  );
}
