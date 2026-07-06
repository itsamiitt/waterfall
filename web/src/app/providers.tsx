// app/providers.tsx — QueryClientProvider (staleTime tiers in api/keys.ts), SSE manager
// provider, Toast live region, top-level ErrorBoundary, theme attribute owner (doc 08 §2).
import { Component, useEffect, useMemo, type ReactNode } from "react";
import { MutationCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { isApiError, setUnauthorizedHandler } from "../api/client";
import { SseContext, SseManager } from "../api/sse";
import { staleTimes } from "../api/keys";
import { ToastRegion } from "../design/primitives";
import { toast, useToastStore } from "./toast";

function makeQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // Freshness is the SSE manager's job (doc 08 §4/§5): no focus refetch, no intervals.
        refetchOnWindowFocus: false,
        staleTime: staleTimes.config,
        retry: 1,
      },
      mutations: {
        // Retrying a write is an explicit user action reusing the SAME Idempotency-Key (G2).
        retry: 0,
      },
    },
    mutationCache: new MutationCache({
      // Uniform error surface (doc 08 §8): mutation errors toast the envelope code.
      onError: (error) => {
        if (isApiError(error)) {
          if (error.code === "mfa_required") return; // handled by the step-up dialog locally
          toast.error(`${error.code}: ${error.message}`);
        } else {
          toast.error(error instanceof Error ? error.message : "request failed");
        }
      },
    }),
  });
}

class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div className="auth-card" role="alert">
          <h1>Something went wrong</h1>
          <p className="form-error">{this.state.error.message}</p>
          <button className="p-btn" data-variant="primary" onClick={() => location.assign("/")}>
            Reload
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}

function Toasts() {
  const items = useToastStore((s) => s.toasts);
  const dismiss = useToastStore((s) => s.dismiss);
  return <ToastRegion items={items} onDismiss={dismiss} />;
}

export function AppProviders({ children }: { children: ReactNode }) {
  const queryClient = useMemo(makeQueryClient, []);
  const sse = useMemo(() => new SseManager({ queryClient }), [queryClient]);

  useEffect(() => {
    // 401 interceptor (doc 08 §7): clear the cache, close the SSE stream, redirect to
    // /login?next=<current-path>. Uses location.assign so no stale in-memory state survives.
    setUnauthorizedHandler(() => {
      sse.close();
      queryClient.clear();
      const here = location.pathname + location.search;
      if (!location.pathname.startsWith("/login") && !location.pathname.startsWith("/mfa")) {
        location.assign(`/login?next=${encodeURIComponent(here)}`);
      }
    });
    return () => {
      setUnauthorizedHandler(null);
      sse.close();
    };
  }, [queryClient, sse]);

  return (
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <SseContext.Provider value={sse}>
          {children}
          <Toasts />
        </SseContext.Provider>
      </QueryClientProvider>
    </ErrorBoundary>
  );
}
