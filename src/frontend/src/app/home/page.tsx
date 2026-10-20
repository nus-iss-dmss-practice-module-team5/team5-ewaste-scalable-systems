import { RequireAuth } from "@/lib/auth/guards";
import { HomeShell } from "./home-shell";

export default function HomePage() {
  return (
    <RequireAuth>
      <HomeShell />
    </RequireAuth>
  );
}
