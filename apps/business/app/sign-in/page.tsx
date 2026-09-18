"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { signInWithEmailAndPassword, useSession } from "@/lib/auth";
import { AuthCard } from "@/components/AuthCard";
import { Button } from "@/components/ui/Button";
import { TextField } from "@/components/ui/Field";
import { Notice } from "@/components/ui/Notice";
import { linkClasses } from "@/components/ui/Styles";

const schema = z.object({
  email: z.string().trim().email("Enter the email you registered with"),
  password: z.string().min(8, "Your password is at least 8 characters"),
});
type Values = z.infer<typeof schema>;

export default function SignInPage() {
  const { session, hydrated, login } = useSession();
  const router = useRouter();
  const [failure, setFailure] = useState<string | null>(null);

  useEffect(() => {
    if (hydrated && session) router.replace("/business");
  }, [hydrated, session, router]);

  const { register, handleSubmit, formState } = useForm<Values>({ resolver: zodResolver(schema) });

  const onSubmit = handleSubmit(async (v) => {
    setFailure(null);
    try {
      login(await signInWithEmailAndPassword(v.email, v.password));
      router.replace("/business");
    } catch (e) {
      setFailure(e instanceof Error ? e.message : "Sign-in failed");
    }
  });

  return (
    <AuthCard
      title="Sign in"
      subtitle="The same account as the Tapp merchant app."
      footer={
        <>
          New to Tapp?{" "}
          <Link href="/register" className={linkClasses}>
            Register a business account
          </Link>
        </>
      }
    >
      <form onSubmit={onSubmit} className="grid gap-4" noValidate>
        <TextField id="email" label="Email" type="email" autoComplete="email" error={formState.errors.email?.message} {...register("email")} />
        <TextField id="password" label="Password" type="password" autoComplete="current-password" error={formState.errors.password?.message} {...register("password")} />
        {failure ? <Notice tone="error">{failure}</Notice> : null}
        <Button type="submit" loading={formState.isSubmitting} className="w-full">
          Sign in
        </Button>
      </form>
    </AuthCard>
  );
}
