"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { registerWithEmailAndPassword, useSession } from "@/lib/auth";
import { AuthCard } from "@/components/AuthCard";
import { Button } from "@/components/ui/Button";
import { TextField } from "@/components/ui/Field";
import { Notice } from "@/components/ui/Notice";
import { linkClasses } from "@/components/ui/Styles";

const schema = z.object({
  firstName: z.string().trim().min(1, "Your first name"),
  lastName: z.string().trim().min(1, "Your last name"),
  email: z.string().trim().email("A valid email address"),
  password: z.string().min(8, "At least 8 characters").max(128, "At most 128 characters"),
});
type Values = z.infer<typeof schema>;

export default function RegisterPage() {
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
      login(await registerWithEmailAndPassword(v));
      router.replace("/business");
    } catch (e) {
      setFailure(e instanceof Error ? e.message : "Registration failed");
    }
  });

  return (
    <AuthCard
      title="Register"
      subtitle="This creates a merchant account: the same one you sign into the Tapp merchant app with."
      footer={
        <>
          Already registered?{" "}
          <Link href="/sign-in" className={linkClasses}>
            Sign in
          </Link>
        </>
      }
    >
      <form onSubmit={onSubmit} className="grid gap-4" noValidate>
        <div className="grid gap-4 sm:grid-cols-2">
          <TextField id="firstName" label="First name" autoComplete="given-name" error={formState.errors.firstName?.message} {...register("firstName")} />
          <TextField id="lastName" label="Last name" autoComplete="family-name" error={formState.errors.lastName?.message} {...register("lastName")} />
        </div>
        <TextField id="email" label="Email" type="email" autoComplete="email" error={formState.errors.email?.message} {...register("email")} />
        <TextField id="password" label="Password" type="password" autoComplete="new-password" hint="At least 8 characters." error={formState.errors.password?.message} {...register("password")} />
        {failure ? <Notice tone="error">{failure}</Notice> : null}
        <Button type="submit" loading={formState.isSubmitting} className="w-full">
          Create account
        </Button>
      </form>
    </AuthCard>
  );
}
