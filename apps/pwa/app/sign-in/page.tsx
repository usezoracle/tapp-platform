"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { PiEyeBold, PiEyeSlashBold, PiCheckBold } from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { InputError } from "@/components/ui/InputError";
import { Logo } from "@/components/ui/Logo";
import { inputClasses, linkClasses } from "@/components/ui/Styles";
import { AnimatedComponent, slideInOut } from "@/components/ui/AnimatedComponents";
import { cn } from "@/lib/utils";
import {
  useSession,
  signInWithEmailAndPassword,
  signUpWithEmailAndPassword,
  requestPasswordReset,
  completePasswordReset,
} from "@/lib/auth";

export default function SignInPage() {
  return (
    <Suspense fallback={<Screen centered />}>
      <SignInBody />
    </Suspense>
  );
}

type AuthMode = "sign-in" | "sign-up" | "forgot-password" | "reset-code";

const COPY: Record<AuthMode, { title: string; subtitle: (email: string) => string; cta: string }> = {
  "sign-in":         { title: "Sign in",        subtitle: () => "Your Freedom wallet and card.",             cta: "Sign in" },
  "sign-up":         { title: "Create account", subtitle: () => "A Freedom account, ready in a minute.",      cta: "Create account" },
  "forgot-password": { title: "Reset password", subtitle: () => "We will email you a 6-digit code.",       cta: "Send code" },
  "reset-code":      { title: "New password",   subtitle: (e) => `Enter the code sent to ${e}.`,           cta: "Update password and sign in" },
};

function SignInBody() {
  const router = useRouter();
  const params = useSearchParams();
  const nextHref = params.get("next") ?? "/";
  const { hydrated, session, login } = useSession();

  const [mode, setMode] = useState<AuthMode>("sign-in");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [firstName, setFirstName] = useState("");
  const [resetCode, setResetCode] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState(false);

  const [error, setError] = useState<string | null>(null);
  const [infoMsg, setInfoMsg] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (hydrated && session) router.replace(nextHref);
  }, [hydrated, session, router, nextHref]);

  if (hydrated && session) return null;

  function switchMode(newMode: AuthMode) {
    setError(null);
    setInfoMsg(null);
    setMode(newMode);
  }

  async function submit() {
    setError(null);
    setInfoMsg(null);

    if (mode === "sign-in") {
      if (!email || !password) return setError("Enter your email and password.");
      setLoading(true);
      try {
        login(await signInWithEmailAndPassword(email, password));
        router.replace(nextHref);
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : "Invalid email or password.");
      } finally {
        setLoading(false);
      }
      return;
    }

    if (mode === "sign-up") {
      if (!email || !password) return setError("Fill in every required field.");
      if (password.length < 8) return setError("Password must be at least 8 characters.");
      if (password !== confirmPassword) return setError("Passwords do not match.");
      setLoading(true);
      try {
        login(
          await signUpWithEmailAndPassword(email, password, firstName.trim() || "Cardholder", "User"),
        );
        router.replace(nextHref);
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : "Account creation failed.");
      } finally {
        setLoading(false);
      }
      return;
    }

    if (mode === "forgot-password") {
      if (!email) return setError("Enter your account email.");
      setLoading(true);
      try {
        const res = await requestPasswordReset(email);
        setInfoMsg(res.message);
        if (res.devOtp) setResetCode(res.devOtp);
        setMode("reset-code");
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : "Could not send the reset code.");
      } finally {
        setLoading(false);
      }
      return;
    }

    if (mode === "reset-code") {
      if (!resetCode.trim()) return setError("Enter the 6-digit code.");
      if (password.length < 8) return setError("New password must be at least 8 characters.");
      if (password !== confirmPassword) return setError("New passwords do not match.");
      setLoading(true);
      try {
        await completePasswordReset(email, resetCode.trim(), password);
        setInfoMsg("Password reset. Signing you in.");
        login(await signInWithEmailAndPassword(email, password));
        router.replace(nextHref);
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : "Could not reset the password.");
      } finally {
        setLoading(false);
      }
    }
  }

  const isPasswordValid = password.length >= 8;
  const copy = COPY[mode];
  const inFlow = mode === "forgot-password" || mode === "reset-code";

  return (
    <Screen className="py-4">
      <AnimatedComponent variant={slideInOut} className="grid gap-6">
        <Logo className="h-6" />

        <PageHeader
          title={copy.title}
          subtitle={copy.subtitle(email)}
          hideBack={!inFlow}
          back={inFlow ? () => switchMode("sign-in") : undefined}
        />

        {infoMsg ? <InfoBanner icon={<PiCheckBold />}>{infoMsg}</InfoBanner> : null}

        <form
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
          className="grid gap-4"
        >
          {mode === "sign-up" && (
            <Field
              id="name"
              label="Full name"
              type="text"
              autoComplete="name"
              placeholder="Alex Morgan"
              value={firstName}
              onChange={setFirstName}
            />
          )}

          {mode !== "reset-code" && (
            <Field
              id="email"
              label="Email"
              type="email"
              required
              autoComplete="email"
              placeholder="name@example.com"
              value={email}
              onChange={setEmail}
            />
          )}

          {mode === "reset-code" && (
            <Field
              id="code"
              label="6-digit code"
              type="text"
              inputMode="numeric"
              required
              maxLength={8}
              placeholder="123456"
              value={resetCode}
              onChange={setResetCode}
              className="font-mono tracking-[0.2em]"
              action={
                <button type="button" onClick={() => void submit()} className={cn(linkClasses, "text-xs")}>
                  Resend code
                </button>
              }
            />
          )}

          {mode !== "forgot-password" && (
            <Field
              id="password"
              label={mode === "reset-code" ? "New password" : "Password"}
              value={password}
              onChange={setPassword}
              action={
                mode === "sign-in" ? (
                  <button
                    type="button"
                    onClick={() => switchMode("forgot-password")}
                    className={cn(linkClasses, "text-xs")}
                  >
                    Forgot password?
                  </button>
                ) : null
              }
              hint={
                (mode === "sign-up" || mode === "reset-code") && password.length > 0 ? (
                  <span className={cn("flex items-center gap-1.5", isPasswordValid ? "text-positive-fg" : "text-fg-muted")}>
                    <span className={cn("size-1.5 rounded-full", isPasswordValid ? "bg-positive" : "bg-caution")} />
                    {isPasswordValid ? "Meets requirements" : "At least 8 characters"}
                  </span>
                ) : null
              }
            >
              <PasswordInput
                id="password"
                value={password}
                onChange={setPassword}
                shown={showPassword}
                onToggle={() => setShowPassword((s) => !s)}
                autoComplete={mode === "sign-in" ? "current-password" : "new-password"}
              />
            </Field>
          )}

          {(mode === "sign-up" || mode === "reset-code") && (
            <Field
              id="confirm"
              label={mode === "reset-code" ? "Confirm new password" : "Confirm password"}
              value={confirmPassword}
              onChange={setConfirmPassword}
              hint={
                confirmPassword && password !== confirmPassword ? (
                  <span className="text-negative-fg">Passwords do not match</span>
                ) : null
              }
            >
              <PasswordInput
                id="confirm"
                value={confirmPassword}
                onChange={setConfirmPassword}
                shown={showConfirmPassword}
                onToggle={() => setShowConfirmPassword((s) => !s)}
                autoComplete="new-password"
              />
            </Field>
          )}

          {error && <InputError message={error} />}

          <Button type="submit" loading={loading} className="mt-2">
            {copy.cta}
          </Button>
        </form>

        <p className="text-[13px] text-fg-muted">
          {mode === "sign-in" && (
            <>
              No account?{" "}
              <button type="button" onClick={() => switchMode("sign-up")} className={linkClasses}>
                Create one
              </button>
            </>
          )}
          {mode === "sign-up" && (
            <>
              Already have an account?{" "}
              <button type="button" onClick={() => switchMode("sign-in")} className={linkClasses}>
                Sign in
              </button>
            </>
          )}
          {inFlow && (
            <button type="button" onClick={() => switchMode("sign-in")} className={linkClasses}>
              Back to sign in
            </button>
          )}
        </p>

        <p className="text-xs text-fg-subtle">Base mainnet · non-custodial · encrypted.</p>
      </AnimatedComponent>
    </Screen>
  );
}

function PasswordInput({
  id,
  value,
  onChange,
  shown,
  onToggle,
  autoComplete,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  shown: boolean;
  onToggle: () => void;
  autoComplete: string;
}) {
  return (
    <div className="relative">
      <input
        id={id}
        type={shown ? "text" : "password"}
        required
        minLength={8}
        autoComplete={autoComplete}
        placeholder="At least 8 characters"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={cn(inputClasses, "pr-10")}
      />
      <button
        type="button"
        aria-label={shown ? "Hide password" : "Show password"}
        onClick={onToggle}
        className="focus-ring absolute top-1/2 right-1 grid size-8 -translate-y-1/2 place-items-center rounded-sm text-fg-muted transition-colors hover:text-fg [&>svg]:size-4"
      >
        {shown ? <PiEyeSlashBold /> : <PiEyeBold />}
      </button>
    </div>
  );
}
