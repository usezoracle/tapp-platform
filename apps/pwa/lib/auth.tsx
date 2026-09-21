"use client";

/**
 * Auth for the Tapp PWA.
 *
 * Two ways in, both ending in the same session: Google OAuth (the PWA gets an
 * ID token, POSTs it to /v1/auth/google, which verifies it against Google's
 * JWKS and returns a JWT pair) and email with a password.
 *
 * A session is a pair of tokens and who they belong to. It used to also carry
 * a Sui address and a flag saying whether that address had been derived
 * properly or fallen back to a development salt; both are gone with the chain
 * they were for.
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

export interface Session {
  jwt: string;
  refreshJwt: string;
  email: string;
  scope: string;
}

const STORAGE_KEY = "tapp.session.v1";
/**
 * Where the API lives. Required, and not defaulted to localhost.
 *
 * A build with this unset used to ship silently and send every request to a
 * machine the user does not have, which reads as "the network is down" rather
 * than "this was built without an API URL".
 */
const API_BASE = process.env.NEXT_PUBLIC_API_BASE_URL;

function apiBase(): string {
  if (!API_BASE) {
    throw new Error(
      "This app was built without NEXT_PUBLIC_API_BASE_URL set, so it does not know where the API is.",
    );
  }
  return API_BASE;
}

interface GoogleAuthResponse {
  access_token: string;
  refresh_token: string;
  email: string;
  scope: string;
  is_new_user: boolean;
}

interface RailsEnvelope<T> {
  status: "success" | "error";
  message: string;
  data?: T;
}

/** Exchange a Google ID-token credential for an API session. */
export async function signInWithGoogleCredential(idToken: string): Promise<Session> {
  const res = await fetch(`${apiBase()}/v1/auth/google`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Client-Type": "web",
      "ngrok-skip-browser-warning": "1",
    },
    body: JSON.stringify({ id_token: idToken }),
  });
  const body = (await res.json()) as RailsEnvelope<GoogleAuthResponse>;
  if (!res.ok || body.status !== "success" || !body.data) {
    throw new Error(body.message || `Google sign-in failed (${res.status})`);
  }

  const session: Session = {
    jwt: body.data.access_token,
    refreshJwt: body.data.refresh_token,
    email: body.data.email,
    scope: body.data.scope,
  };
  if (typeof window !== "undefined") {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
  }
  return session;
}

export function formatApiErrorMessage(body: any, fallback: string): string {
  if (Array.isArray(body?.data) && body.data.length > 0) {
    const messages = body.data
      .map((item: any) => {
        if (item?.field && item?.message) {
          return `${item.field}: ${item.message}`;
        }
        if (typeof item === "string") return item;
        return item?.message || "";
      })
      .filter(Boolean);
    if (messages.length > 0) {
      return messages.join(". ");
    }
  }
  if (typeof body?.data === "string" && body.data) {
    return body.data;
  }
  if (body?.message && body.message !== "Failed to validate payload") {
    return body.message;
  }
  return body?.message || fallback;
}

export async function signInWithEmailAndPassword(
  email: string,
  pass: string,
): Promise<Session> {
  const res = await fetch(`${apiBase()}/v1/auth/login`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Client-Type": "web",
      "ngrok-skip-browser-warning": "1",
    },
    body: JSON.stringify({ email, password: pass }),
  });
  const body = (await res.json().catch(() => ({}))) as {
    status?: string;
    message?: string;
    data?: any;
  };
  if (!res.ok || body.status !== "success" || !body.data || !body.data.accessToken) {
    throw new Error(formatApiErrorMessage(body, `Sign-in failed (${res.status})`));
  }

  const session: Session = {
    jwt: body.data.accessToken,
    refreshJwt: body.data.refreshToken,
    email: email,
    scope: (body.data.scopes || ["sender"]).join(" "),
  };
  if (typeof window !== "undefined") {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
  }
  return session;
}

export async function signUpWithEmailAndPassword(
  email: string,
  pass: string,
  firstName = "Cardholder",
  lastName = "User",
): Promise<Session> {
  const res = await fetch(`${apiBase()}/v1/auth/register`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Client-Type": "web",
      "ngrok-skip-browser-warning": "1",
    },
    body: JSON.stringify({
      email,
      password: pass,
      firstName,
      lastName,
      scopes: ["sender"],
    }),
  });
  const body = (await res.json().catch(() => ({}))) as {
    status?: string;
    message?: string;
    data?: any;
  };
  if (!res.ok || body.status !== "success" || !body.data || !body.data.accessToken) {
    throw new Error(formatApiErrorMessage(body, `Sign-up failed (${res.status})`));
  }

  const session: Session = {
    jwt: body.data.accessToken,
    refreshJwt: body.data.refreshToken,
    email: body.data.email,
    scope: "sender",
  };
  if (typeof window !== "undefined") {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
  }
  return session;
}

export async function requestPasswordReset(
  email: string,
): Promise<{ message: string; devOtp?: string }> {
  const res = await fetch(`${apiBase()}/v1/auth/reset-password-token`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Client-Type": "web",
      "ngrok-skip-browser-warning": "1",
    },
    body: JSON.stringify({ email }),
  });
  const body = (await res.json().catch(() => ({}))) as {
    status?: string;
    message?: string;
    data?: any;
  };
  if (!res.ok || body.status !== "success") {
    throw new Error(formatApiErrorMessage(body, "Failed to send reset password code"));
  }
  return {
    message: body.message || "A reset code has been sent to your email",
    devOtp: body.data?.devOtp,
  };
}

export async function completePasswordReset(
  email: string,
  resetToken: string,
  pass: string,
): Promise<void> {
  const res = await fetch(`${apiBase()}/v1/auth/reset-password`, {
    method: "PATCH",
    headers: {
      "Content-Type": "application/json",
      "Client-Type": "web",
      "ngrok-skip-browser-warning": "1",
    },
    body: JSON.stringify({
      email,
      resetToken,
      password: pass,
    }),
  });
  const body = (await res.json().catch(() => ({}))) as {
    status?: string;
    message?: string;
    data?: any;
  };
  if (!res.ok || body.status !== "success") {
    throw new Error(formatApiErrorMessage(body, "Failed to reset password"));
  }
}

export function signOut(): void {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(STORAGE_KEY);
  notifySessionChange();
}

/* ------------------------------------------------------------------ */
/*  Session-change notifier — lets out-of-band updates (token refresh  */
/*  or forced sign-out) re-sync every useSession() consumer.           */
/* ------------------------------------------------------------------ */

const sessionListeners = new Set<() => void>();
function notifySessionChange(): void {
  sessionListeners.forEach((fn) => fn());
}
function subscribeSessionChange(fn: () => void): () => void {
  sessionListeners.add(fn);
  return () => {
    sessionListeners.delete(fn);
  };
}

/* ------------------------------------------------------------------ */
/*  Access-token refresh (silent, single-flight)                       */
/* ------------------------------------------------------------------ */

interface RefreshResponse {
  accessToken: string;
  refreshToken: string;
}

function parseStoredSession(): Session | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as Session) : null;
  } catch {
    return null;
  }
}

let refreshInFlight: Promise<string | null> | null = null;

/**
 * Exchange the stored refresh token for a fresh access token. The rails
 * access JWT lives only ~15 min; this rotates + persists both tokens so
 * the session survives up to the 7-day refresh window.
 *
 * Single-flighted: concurrent 401s share one rotation. Otherwise the
 * second caller would replay an already-consumed refresh token and the
 * backend's replay detection would kill the whole family.
 *
 * `failedToken` is the access token that just 401'd — if the stored
 * token has already advanced past it (another request refreshed first),
 * hand that back instead of rotating again.
 *
 * Returns the new access token, or null when there's no usable session
 * or the refresh token itself is rejected (the session is then cleared).
 */
export function refreshAccessToken(failedToken?: string): Promise<string | null> {
  const current = parseStoredSession();
  if (failedToken && current?.jwt && current.jwt !== failedToken) {
    return Promise.resolve(current.jwt);
  }
  if (refreshInFlight) return refreshInFlight;
  refreshInFlight = doRefresh().finally(() => {
    refreshInFlight = null;
  });
  return refreshInFlight;
}

async function doRefresh(): Promise<string | null> {
  const current = parseStoredSession();
  if (!current?.refreshJwt) return null;

  let res: Response;
  try {
    res = await fetch(`${apiBase()}/v1/auth/refresh`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "ngrok-skip-browser-warning": "1",
      },
      body: JSON.stringify({ refreshToken: current.refreshJwt }),
    });
  } catch {
    // Network blip — transient. Keep the session; just fail this attempt.
    return null;
  }

  if (res.status === 401) {
    // Refresh token expired/revoked — the session is genuinely dead.
    signOut();
    return null;
  }

  const body = (await res.json().catch(() => ({}))) as RailsEnvelope<RefreshResponse>;
  if (!res.ok || body.status !== "success" || !body.data?.accessToken) {
    return null; // unexpected (5xx, etc.) — transient, keep the session
  }

  const updated: Session = {
    ...current,
    jwt: body.data.accessToken,
    refreshJwt: body.data.refreshToken,
  };
  if (typeof window !== "undefined") {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(updated));
  }
  notifySessionChange();
  return updated.jwt;
}

function readSession(): Session | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    return JSON.parse(raw) as Session;
  } catch {
    return null;
  }
}

/* ------------------------------------------------------------------ */
/*  Shared session context                                            */
/* ------------------------------------------------------------------ */

interface SessionCtx {
  session: Session | null;
  hydrated: boolean;
  refresh: () => void;
  clear: () => void;
  login: (session: Session) => void;
}

const SessionContext = createContext<SessionCtx | null>(null);

/**
 * Wrap the app in `<SessionProvider>` so every `useSession()` call
 * shares a single session state. When any component calls `refresh()`
 * or `clear()`, every consumer re-renders with the new value.
 */
export function SessionProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null);
  const [hydrated, setHydrated] = useState(false);

  useEffect(() => {
    setSession(readSession());
    setHydrated(true);
    // Re-sync when a token refresh (or forced sign-out) updates storage
    // out-of-band, so consumers always hold the freshest access token.
    return subscribeSessionChange(() => setSession(readSession()));
  }, []);

  const refresh = useCallback(() => setSession(readSession()), []);

  const clear = useCallback(() => {
    signOut();
    setSession(null);
  }, []);

  const login = useCallback((newSession: Session) => {
    if (typeof window !== "undefined") {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify(newSession));
    }
    setSession(newSession);
  }, []);

  // Memoised so consumers re-render when the session actually changes, not on
  // every render of this provider. An object literal here gave every consumer
  // a new context value each time, which is how a token refresh could restart
  // an in-flight card claim in another component and strand it.
  const value = useMemo(
    () => ({ session, hydrated, refresh, clear, login }),
    [session, hydrated, refresh, clear, login],
  );

  return (
    <SessionContext.Provider value={value}>
      {children}
    </SessionContext.Provider>
  );
}

/**
 * Reactive session hook. Must be used inside a `<SessionProvider>`.
 * All consumers share the same state — calling `refresh()` in one
 * component immediately updates the BottomNav, Navbar, etc.
 */
export function useSession(): SessionCtx {
  const ctx = useContext(SessionContext);
  if (!ctx) {
    throw new Error("useSession() must be used inside <SessionProvider>");
  }
  return ctx;
}
