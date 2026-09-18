"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { useSession } from "@/lib/auth";

export default function Home() {
  const { session, hydrated } = useSession();
  const router = useRouter();
  useEffect(() => {
    if (!hydrated) return;
    router.replace(session ? "/business" : "/sign-in");
  }, [hydrated, session, router]);
  return null;
}
