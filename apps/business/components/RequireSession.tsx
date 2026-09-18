"use client";

import { useRouter } from "next/navigation";
import { useEffect, type ReactNode } from "react";
import { useSession, type Session } from "@/lib/auth";

/**
 * Renders children only with a session; sends everyone else to /sign-in.
 * Until localStorage has been read nothing is drawn, so a signed-in reload
 * does not flash the sign-in screen.
 */
export function RequireSession({ children }: { children: (session: Session) => ReactNode }) {
  const { session, hydrated } = useSession();
  const router = useRouter();

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in");
  }, [hydrated, session, router]);

  if (!hydrated || !session) return null;
  return <>{children(session)}</>;
}
