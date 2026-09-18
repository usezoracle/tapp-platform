"use client";

/**
 * Auth for Tapp Business.
 *
 * Email and password only, on the merchant ("sender") scope. Registering
 * here creates the same account the Tapp merchant app signs into: the API
 * makes one user with a sender profile, and both apps read the same row.
 *
 * A session is the JWT pair and who it belongs to, kept in localStorage.
 * The access token lives ~15 min; `refreshAccessToken` rotates it once on
 * a 401 (see lib/api/http.ts) so an expiry never reaches the user.
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

const STORAGE_KEY = "tapp.business.session.v1";

export const API_BASE =
  process.env.NEXT_PUBLIC_API_BASE_URL || "https://api-production-ce83.up.railway.app";

interface Envelope<T> {
  status?: "success" | "error";
  message?: string;
  data?: T;
}

const baseHeaders = {
  "Content-Type": "application/json",
  "Client-Type": "web",
  "ngrok-skip-browser-warning": "1",
};

/**
 * Turns the API's error envelope into one sentence. Validation failures come
 * as `data: [{field, message}]`; other failures carry only `message`.
 */
export function formatApiErrorMessage(body: Envelope<unknown> | undefined, fallback: string): string {
  const data = body?.data;
  if (Array.isArray(data) && data.length > 0) {
    const messages = data
      .map((item: unknown) => {
        if (typeof item === "string") return item;
        if (item && typeof item === "object") {
          const o = item as { field?: string; message?: string };
          if (o.field && o.message) return `${o.field}: ${o.message}`;
          return o.message ?? "";
        }
        return "";
      })
      .filter(Boolean);
    if (messages.length > 0) return messages.join(". ");
  }
  if (typeof data === "string" && data) return data;
  if (body?.message && body.message !== "Failed to validate payload") return body.message;
  return body?.message || fallback;
}

interface TokenPair {
  accessToken: string;
  refreshToken: string;
  scopes?: string[];
  email?: string;
}

function persist(session: Session): void {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
}

export async function signInWithEmailAndPassword(email: string, password: string): Promise<Session> {
  const res = await fetch(`${API_BASE}/v1/auth/login`, {
    method: "POST",
    headers: baseHeaders,
    body: JSON.stringify({ email, password, scope: "sender" }),
  });
  const body = (await res.json().catch(() => ({}))) as Envelope<TokenPair>;
  if (!res.ok || body.status !== "success" || !body.data?.accessToken) {
    throw new Error(formatApiErrorMessage(body, `Sign-in failed (${res.status})`));
  }
  const scopes = body.data.scopes ?? [];
  if (scopes.length > 0 && !scopes.includes("sender")) {
    throw new Error("This account is not a merchant account. Register a business account to continue.");
  }
  const session: Session = {
    jwt: body.data.accessToken,
    refreshJwt: body.data.refreshToken,
    email,
    scope: scopes.join(" ") || "sender",
  };
  persist(session);
  return session;
}

export async function registerWithEmailAndPassword(input: {
  firstName: string;
  lastName: string;
  email: string;
  password: string;
}): Promise<Session> {
  const res = await fetch(`${API_BASE}/v1/auth/register`, {
    method: "POST",
    headers: baseHeaders,
    body: JSON.stringify({
      firstName: input.firstName,
      lastName: input.lastName,
      email: input.email,
      password: input.password,
      scopes: ["sender"],
      currency: "NGN",
    }),
  });
  const body = (await res.json().catch(() => ({}))) as Envelope<TokenPair>;
  if (!res.ok || body.status !== "success" || !body.data?.accessToken) {
    throw new Error(formatApiErrorMessage(body, `Registration failed (${res.status})`));
  }
  const session: Session = {
    jwt: body.data.accessToken,
    refreshJwt: body.data.refreshToken,
    email: body.data.email ?? input.email,
    scope: "sender",
  };
  persist(session);
  return session;
}

export function signOut(): void {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(STORAGE_KEY);
  notifySessionChange();
}

/* ---------------------------------------------------------------- notify */

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

/* --------------------------------------------------------------- refresh */

export function readSession(): Session | null {
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
 * Exchange the stored refresh token for a fresh access token, single-flighted
 * so two concurrent 401s do not both spend the same refresh token (the API
 * treats a replayed refresh token as theft and revokes the family).
 *
 * `failedToken` is the access token that just 401'd. If storage has already
 * moved past it, that newer token is handed back without another rotation.
 */
export function refreshAccessToken(failedToken?: string): Promise<string | null> {
  const current = readSession();
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
  const current = readSession();
  if (!current?.refreshJwt) return null;

  let res: Response;
  try {
    res = await fetch(`${API_BASE}/v1/auth/refresh`, {
      method: "POST",
      headers: baseHeaders,
      body: JSON.stringify({ refreshToken: current.refreshJwt }),
    });
  } catch {
    return null; // network blip: keep the session, fail this attempt
  }

  if (res.status === 401) {
    signOut(); // the refresh token itself is dead
    return null;
  }

  const body = (await res.json().catch(() => ({}))) as Envelope<TokenPair>;
  if (!res.ok || body.status !== "success" || !body.data?.accessToken) return null;

  const updated: Session = {
    ...current,
    jwt: body.data.accessToken,
    refreshJwt: body.data.refreshToken,
  };
  persist(updated);
  notifySessionChange();
  return updated.jwt;
}

/* --------------------------------------------------------------- context */

interface SessionCtx {
  session: Session | null;
  /** False until localStorage has been read on the client. */
  hydrated: boolean;
  login: (session: Session) => void;
  clear: () => void;
}

const SessionContext = createContext<SessionCtx | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null);
  const [hydrated, setHydrated] = useState(false);

  useEffect(() => {
    setSession(readSession());
    setHydrated(true);
    return subscribeSessionChange(() => setSession(readSession()));
  }, []);

  const login = useCallback((s: Session) => {
    persist(s);
    setSession(s);
  }, []);

  const clear = useCallback(() => {
    signOut();
    setSession(null);
  }, []);

  const value = useMemo(() => ({ session, hydrated, login, clear }), [session, hydrated, login, clear]);

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionCtx {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession() must be used inside <SessionProvider>");
  return ctx;
}
