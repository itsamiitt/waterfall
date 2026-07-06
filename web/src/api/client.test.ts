// client.ts unit tests (doc 12 P8 acceptance #3): CSRF + Idempotency-Key headers on every
// mutation, uniform error-envelope -> ApiError, 401 interceptor, §1.3 exemptions.
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  api,
  ApiError,
  get,
  isApiError,
  post,
  setCsrfToken,
  setUnauthorizedHandler,
} from "./client";

// Response factory (a Response body is single-use; each fetch gets a fresh one).
function jsonResponse(status: number, body: unknown, headers?: Record<string, string>) {
  return () =>
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json", ...headers },
    });
}

function mockFetch(makeRes: () => Response) {
  const fn = vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) => makeRes());
  vi.stubGlobal("fetch", fn);
  return fn;
}

afterEach(() => {
  vi.unstubAllGlobals();
  setCsrfToken(null);
  setUnauthorizedHandler(null);
});

describe("api client", () => {
  it("parses the uniform error envelope into a typed ApiError", async () => {
    mockFetch(jsonResponse(422, { error: { code: "validation_failed", message: "bad enum" } }));
    const err = await api("/providers", { method: "POST", body: {} }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const apiErr = err as ApiError;
    expect(apiErr.status).toBe(422);
    expect(apiErr.code).toBe("validation_failed");
    expect(apiErr.message).toBe("bad enum");
    expect(isApiError(apiErr)).toBe(true);
  });

  it("falls back to a generic code when the body is not an envelope", async () => {
    mockFetch(() => new Response("<html>proxy error</html>", { status: 502 }));
    const err = (await get("/overview").catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe("internal");
    expect(err.status).toBe(502);
  });

  it("surfaces Retry-After on 429 rate_limited", async () => {
    mockFetch(
      jsonResponse(429, { error: { code: "rate_limited", message: "slow down" } }, { "Retry-After": "7" }),
    );
    const err = (await post("/keys/bulk", {}).catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe("rate_limited");
    expect(err.retryAfterS).toBe(7);
  });

  it("sends Idempotency-Key on writes and preserves an explicit key across retries", async () => {
    const fetchFn = mockFetch(jsonResponse(200, {}));
    await post("/providers", { id: "p" });
    const auto = new Headers(fetchFn.mock.calls[0]![1]!.headers).get("Idempotency-Key");
    expect(auto).toMatch(/^[0-9a-f-]{36}$/);

    await api("/providers", { method: "POST", body: {}, idempotencyKey: "stable-key-1" });
    await api("/providers", { method: "POST", body: {}, idempotencyKey: "stable-key-1" });
    const k1 = new Headers(fetchFn.mock.calls[1]![1]!.headers).get("Idempotency-Key");
    const k2 = new Headers(fetchFn.mock.calls[2]![1]!.headers).get("Idempotency-Key");
    expect(k1).toBe("stable-key-1");
    expect(k2).toBe("stable-key-1"); // same logical mutation -> G2 replay, not double-apply
  });

  it("generates a fresh key per logical mutation when none is supplied", async () => {
    const fetchFn = mockFetch(jsonResponse(200, {}));
    await post("/providers", {});
    await post("/providers", {});
    const k1 = new Headers(fetchFn.mock.calls[0]![1]!.headers).get("Idempotency-Key");
    const k2 = new Headers(fetchFn.mock.calls[1]![1]!.headers).get("Idempotency-Key");
    expect(k1).not.toBe(k2);
  });

  it("exempts the doc 04 §1.3 pre-session auth writes from Idempotency-Key", async () => {
    const fetchFn = mockFetch(jsonResponse(200, { status: "mfa_required" }));
    await post("/auth/login", { email: "a@b.c", password: "x" });
    await post("/auth/mfa/verify", { code: "1" });
    await post("/auth/logout");
    for (const call of fetchFn.mock.calls) {
      expect(new Headers(call[1]!.headers).get("Idempotency-Key")).toBeNull();
    }
  });

  it("injects X-CSRF-Token on non-GET requests only", async () => {
    setCsrfToken("csrf-abc");
    const fetchFn = mockFetch(jsonResponse(200, {}));
    await get("/overview");
    await post("/providers", {});
    expect(new Headers(fetchFn.mock.calls[0]![1]!.headers).get("X-CSRF-Token")).toBeNull();
    expect(new Headers(fetchFn.mock.calls[1]![1]!.headers).get("X-CSRF-Token")).toBe("csrf-abc");
  });

  it("always sends credentials: include", async () => {
    const fetchFn = mockFetch(jsonResponse(200, {}));
    await get("/overview");
    expect(fetchFn.mock.calls[0]![1]!.credentials).toBe("include");
  });

  it("invokes the 401 interceptor on unauthorized and clears the CSRF token", async () => {
    setCsrfToken("stale");
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    mockFetch(jsonResponse(401, { error: { code: "unauthorized", message: "no session" } }));
    await get("/auth/me").catch(() => {});
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
    // A follow-up write must not reuse the desynced token.
    const fetchFn = mockFetch(jsonResponse(200, {}));
    await post("/providers", {});
    expect(new Headers(fetchFn.mock.calls[0]![1]!.headers).get("X-CSRF-Token")).toBeNull();
  });

  it("does NOT redirect on 401 mfa_required (step-up is handled locally)", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    mockFetch(jsonResponse(401, { error: { code: "mfa_required", message: "code required" } }));
    const err = (await post("/approvals/1/approve", {}).catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe("mfa_required");
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("forces re-auth on 403 csrf_invalid (session irrecoverably desynced)", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    mockFetch(jsonResponse(403, { error: { code: "csrf_invalid", message: "mismatch" } }));
    await post("/providers", {}).catch(() => {});
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });
});
