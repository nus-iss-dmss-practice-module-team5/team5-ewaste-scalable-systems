"use client";

import { HOME_PATH } from "@/lib/auth/config";
import { login } from "@/lib/auth/login";
import { useSession } from "@/lib/auth/session-context";
import { isAuthError } from "@/lib/auth/types";
import Link from "next/link";
import { FormEvent, useId, useState } from "react";

export function LoginForm({ expired = false }: { expired?: boolean }) {
  const { setSession, isOffline, offlineUser } = useSession();
  const errorId = useId();
  const emailId = useId();
  const passwordId = useId();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    // Signing in needs the network; the offline banner already says so.
    if (isOffline) {
      return;
    }
    setError(null);
    setLoading(true);
    try {
      const nextSession = await login(email, password);
      setSession(nextSession);
    } catch (caught) {
      if (isAuthError(caught)) {
        setError(caught.message);
      } else {
        setError("Invalid email or password");
      }
    } finally {
      setLoading(false);
    }
  }

  const banner = error
    ? { tone: "error" as const, text: error }
    : expired
      ? {
          tone: "expired" as const,
          text: "Your session expired. Please sign in again.",
        }
      : null;
  const fieldsInvalid = banner?.tone === "error";

  return (
    <form
      onSubmit={onSubmit}
      className="flex flex-col gap-5"
      noValidate
      aria-busy={loading ? true : undefined}
      data-testid="login-form"
    >
      {isOffline ? (
        <div
          role="status"
          data-testid="login-offline"
          className="flex flex-col gap-1 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900"
        >
          <span>You&apos;re offline. Connect to the internet to sign in.</span>
          {offlineUser ? (
            <Link
              href={HOME_PATH}
              data-testid="login-offline-continue"
              className="font-medium text-teal-800 underline"
            >
              {offlineUser.role === "COLLECTOR"
                ? `Open assignments saved for ${offlineUser.name}`
                : `Continue offline as ${offlineUser.name}`}
            </Link>
          ) : null}
        </div>
      ) : null}
      {banner ? (
        <div
          id={errorId}
          role="alert"
          aria-live="assertive"
          data-testid={
            banner.tone === "expired" ? "login-session-expired" : "login-error"
          }
          className={
            banner.tone === "error"
              ? "flex items-start gap-2 rounded-md border border-red-300 bg-red-50 px-3 py-2 text-sm text-red-800"
              : "flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900"
          }
        >
          <span className="mt-0.5 font-bold" aria-hidden>
            {banner.tone === "error" ? "!" : "i"}
          </span>
          <span>{banner.text}</span>
        </div>
      ) : null}

      <div className="flex flex-col gap-1.5">
        <label
          htmlFor={emailId}
          className="text-sm font-semibold text-slate-700"
        >
          Email
        </label>
        <input
          id={emailId}
          type="email"
          name="email"
          autoComplete="username"
          value={email}
          disabled={loading}
          aria-invalid={fieldsInvalid ? true : undefined}
          aria-describedby={banner ? errorId : undefined}
          data-testid="login-email"
          onChange={(event) => setEmail(event.target.value)}
          className={`rounded-md border px-3 py-2 font-normal text-slate-900 outline-none focus:ring-2 focus:ring-teal-700 disabled:bg-slate-100 ${
            fieldsInvalid ? "border-red-300 bg-red-50/40" : "border-slate-300"
          }`}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <label
          htmlFor={passwordId}
          className="text-sm font-semibold text-slate-700"
        >
          Password
        </label>
        <div className="relative">
          <input
            id={passwordId}
            type={showPassword ? "text" : "password"}
            name="password"
            autoComplete="current-password"
            value={password}
            disabled={loading}
            aria-invalid={fieldsInvalid ? true : undefined}
            aria-describedby={banner ? errorId : undefined}
            data-testid="login-password"
            onChange={(event) => setPassword(event.target.value)}
            className={`w-full rounded-md border px-3 py-2 pr-16 font-normal text-slate-900 outline-none focus:ring-2 focus:ring-teal-700 disabled:bg-slate-100 ${
              fieldsInvalid ? "border-red-300 bg-red-50/40" : "border-slate-300"
            }`}
          />
          <button
            type="button"
            onClick={() => setShowPassword((value) => !value)}
            disabled={loading}
            className="absolute right-2 top-1/2 -translate-y-1/2 text-xs font-medium text-slate-500 hover:text-slate-800 disabled:opacity-50"
          >
            {showPassword ? "Hide" : "Show"}
          </button>
        </div>
      </div>

      <button
        type="submit"
        disabled={loading || isOffline}
        data-testid="login-submit"
        className="inline-flex items-center justify-center gap-2 rounded-md bg-teal-800 px-4 py-2.5 text-sm font-semibold text-white hover:bg-teal-900 disabled:cursor-wait disabled:opacity-70"
      >
        {loading ? (
          <>
            <span
              className="h-4 w-4 animate-spin rounded-full border-2 border-white/40 border-t-white"
              aria-hidden
            />
            Signing in…
          </>
        ) : (
          "Sign in"
        )}
      </button>

      <p className="text-center text-xs text-slate-500">
        Use your organisation account.
      </p>
    </form>
  );
}
