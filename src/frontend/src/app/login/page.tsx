import { GuestOnly } from "@/lib/auth/guards";
import { LoginForm } from "./login-form";

function Brand() {
  return (
    <div className="mb-8 flex items-center justify-center gap-3">
      <span className="flex h-10 w-10 items-center justify-center rounded-full border-2 border-teal-700 text-lg text-teal-800">
        ♻
      </span>
      <div>
        <h1 className="text-xl font-bold text-teal-900">E-Waste Platform</h1>
        <p className="text-xs text-slate-500">
          Recycle responsibly. Build a cleaner future.
        </p>
      </div>
    </div>
  );
}

export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ expired?: string }>;
}) {
  const params = await searchParams;

  return (
    <GuestOnly>
      <main className="flex min-h-full flex-1 items-center justify-center bg-slate-100 px-4 py-12">
        <div className="w-full max-w-md rounded-xl bg-white p-8 shadow-md">
          <Brand />
          <h2 className="mb-6 text-2xl font-bold text-slate-900">Sign in</h2>
          <LoginForm expired={params.expired === "1"} />
        </div>
      </main>
    </GuestOnly>
  );
}
