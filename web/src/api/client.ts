// api/client.ts — the single fetch wrapper for /v1/admin (doc 08 §7).
//   - credentials: "include" (dash_session cookie; HttpOnly — never read here)
//   - X-CSRF-Token on every non-GET (403 csrf_invalid forces re-auth, never retried)
//   - Idempotency-Key (crypto.randomUUID) on every write except the doc 04 §1.3 exemptions;
//     callers pass a stable key to make a user-initiated retry a G2 replay
//   - uniform error envelope -> typed ApiError {status, code, message}
//   - 401 unauthorized -> registered handler (providers.tsx clears cache, closes SSE,
//     redirects /login?next=...); 401 mfa_required is left to the caller (step-up UX)

export const API_BASE = "/v1/admin";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryAfterS: number | null;

  constructor(status: number, code: string, message: string, retryAfterS: number | null = null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.retryAfterS = retryAfterS;
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

// ---- CSRF token custody ----
// Primary custody is module memory (doc 08 §7). A sessionStorage copy survives full-page
// reloads because GET /auth/me does not (yet) re-issue the token; it is cleared on logout and
// on any 401/403 desync. The token authorizes nothing without the HttpOnly cookie.

const CSRF_STORAGE_KEY = "wf.csrf";
let csrfToken: string | null = null;

function storage(): Storage | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null; // storage disabled
  }
}

export function setCsrfToken(token: string | null): void {
  csrfToken = token;
  const s = storage();
  if (!s) return;
  try {
    if (token === null) s.removeItem(CSRF_STORAGE_KEY);
    else s.setItem(CSRF_STORAGE_KEY, token);
  } catch {
    /* quota/denied: memory copy still works for this tab's lifetime */
  }
}

export function getCsrfToken(): string | null {
  if (csrfToken !== null) return csrfToken;
  const stored = storage()?.getItem(CSRF_STORAGE_KEY) ?? null;
  csrfToken = stored;
  return stored;
}

// ---- 401 interceptor seam (registered once by app/providers.tsx) ----

type UnauthorizedHandler = (err: ApiError) => void;
let unauthorizedHandler: UnauthorizedHandler | null = null;

export function setUnauthorizedHandler(h: UnauthorizedHandler | null): void {
  unauthorizedHandler = h;
}

// ---- Idempotency ----

/** Pre-session writes exempt from Idempotency-Key (doc 04 §1.3, closed list). */
const IDEMPOTENCY_EXEMPT = new Set(["/auth/login", "/auth/mfa/verify", "/auth/logout"]);

/** One UUID per logical mutation: create it once (e.g. in useMutation state) and pass it on
 * every retry of the same user action so the server replays instead of double-applying (G2). */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

// ---- Request ----

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  /** Stable key for the logical mutation; auto-generated per call when omitted. */
  idempotencyKey?: string;
  /** Extra headers (e.g. X-MFA-Code for step-up endpoints, doc 04 §1.2). */
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

/** Perform a /v1/admin request. `path` is relative to the base, e.g. "/auth/login". */
export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json", ...opts.headers };

  const mutating = method !== "GET";
  if (mutating) {
    const csrf = getCsrfToken();
    if (csrf) headers["X-CSRF-Token"] = csrf;
    const pathOnly = path.split("?")[0] ?? path;
    if (!IDEMPOTENCY_EXEMPT.has(pathOnly) && !("Idempotency-Key" in headers)) {
      headers["Idempotency-Key"] = opts.idempotencyKey ?? newIdempotencyKey();
    }
  }

  let bodyText: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    bodyText = JSON.stringify(opts.body);
  }

  const res = await fetch(API_BASE + path, {
    method,
    headers,
    body: bodyText,
    credentials: "include",
    signal: opts.signal,
  });

  if (res.status === 204) return undefined as T;

  let parsed: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = null; // non-JSON body (proxy error page); fall through to status handling
    }
  }

  if (!res.ok) {
    const env = parsed as { error?: { code?: string; message?: string } } | null;
    const code = env?.error?.code ?? "internal";
    const message = env?.error?.message ?? `request failed with status ${res.status}`;
    const retryAfterRaw = res.headers.get("Retry-After");
    const retryAfterS = retryAfterRaw !== null ? Number(retryAfterRaw) || null : null;
    const err = new ApiError(res.status, code, message, retryAfterS);

    // Session gone or irrecoverably desynced -> force re-authentication (doc 08 §7).
    // 401 mfa_required stays with the caller: it is the MFA step (login) or step-up (X-MFA-Code).
    if ((res.status === 401 && code === "unauthorized") || (res.status === 403 && code === "csrf_invalid")) {
      setCsrfToken(null);
      unauthorizedHandler?.(err);
    }
    throw err;
  }

  return parsed as T;
}

export const get = <T>(path: string, opts?: Omit<RequestOptions, "method" | "body">) =>
  api<T>(path, { ...opts, method: "GET" });
export const post = <T>(path: string, body?: unknown, opts?: Omit<RequestOptions, "method" | "body">) =>
  api<T>(path, { ...opts, method: "POST", body });
export const patch = <T>(path: string, body?: unknown, opts?: Omit<RequestOptions, "method" | "body">) =>
  api<T>(path, { ...opts, method: "PATCH", body });
export const put = <T>(path: string, body?: unknown, opts?: Omit<RequestOptions, "method" | "body">) =>
  api<T>(path, { ...opts, method: "PUT", body });
export const del = <T>(path: string, opts?: Omit<RequestOptions, "method" | "body">) =>
  api<T>(path, { ...opts, method: "DELETE" });
