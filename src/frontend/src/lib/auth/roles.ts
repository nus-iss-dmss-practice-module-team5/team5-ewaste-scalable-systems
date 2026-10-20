import type { Role } from "./types";

export const ROLE_LABEL: Record<Role, string> = {
  DONOR: "Donor",
  RECYCLER: "Recycler",
  COLLECTOR: "Collector",
  AUDITOR: "Auditor",
  ADMIN: "Administrator",
};

export type NavItem = {
  id: string;
  label: string;
};

export const ROLE_NAV: Record<Role, NavItem[]> = {
  DONOR: [
    { id: "requests", label: "My requests" },
    { id: "new-request", label: "New request" },
  ],
  RECYCLER: [
    { id: "opportunities", label: "Opportunities" },
    { id: "claim", label: "Claim" },
    { id: "processing", label: "Processing" },
  ],
  COLLECTOR: [
    { id: "assignments", label: "Assignments" },
    { id: "history", label: "History" },
    { id: "drafts", label: "Offline drafts" },
  ],
  AUDITOR: [
    { id: "custody", label: "Custody" },
    { id: "impact", label: "Impact" },
  ],
  ADMIN: [
    { id: "users", label: "Users" },
    { id: "settings", label: "Settings" },
  ],
};

export const ROLE_HOME_TITLE: Record<Role, string> = {
  DONOR: "My collection requests",
  RECYCLER: "Matched opportunities",
  COLLECTOR: "Assignments",
  AUDITOR: "Chain of custody",
  ADMIN: "Users",
};
