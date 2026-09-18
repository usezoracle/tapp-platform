/**
 * The one transport. Knows the base URL, the `{status, message, data}`
 * envelope, and what to do about a 401: refresh once, retry once.
 */

import { API_BASE, formatApiErrorMessage, refreshAccessToken } from "../auth";

export class ApiError extends Error {
  readonly status: number;
  readonly data?: unknown;

  constructor(status: number, message: string, data?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.data = data;
  }

  /** `400 Invalid business details` carries `data: {field: problem}`. */
  get fieldProblems(): Record<string, string> | null {
    if (this.status !== 400 || !this.data || typeof this.data !== "object" || Array.isArray(this.data)) {
      return null;
    }
    const out: Record<string, string> = {};
    for (const [k, v] of Object.entries(this.data as Record<string, unknown>)) {
      if (typeof v === "string") out[k] = v;
    }
    return Object.keys(out).length > 0 ? out : null;
  }
}

interface Envelope<T> {
  status?: "success" | "error";
  message?: string;
  data?: T;
}

interface RequestOptions {
  body?: unknown;
  token?: string;
  signal?: AbortSignal;
}

export async function request<T>(
  method: string,
  path: string,
  { body, token, signal }: RequestOptions = {},
  retried = false,
): Promise<T> {
  const headers: Record<string, string> = { "ngrok-skip-browser-warning": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });

  const json = (await res.json().catch(() => ({}))) as Envelope<T>;

  if (!res.ok || json.status === "error") {
    if (res.status === 401 && token && !retried && !path.startsWith("/v1/auth/")) {
      const fresh = await refreshAccessToken(token);
      if (fresh) return request<T>(method, path, { body, token: fresh, signal }, true);
    }
    throw new ApiError(res.status, formatApiErrorMessage(json, `Request failed (${res.status})`), json.data);
  }

  return json.data as T;
}
