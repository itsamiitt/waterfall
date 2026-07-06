// api/sse.ts — the SSE manager: the client half of the doc 04 §3 contract (ADR-0019).
//
//   - Exactly ONE EventSource per browser tab, on GET /v1/admin/streams?topics=<csv> where
//     csv is the union of topics declared by mounted views (useSseTopics refcounts).
//   - Topic-set changes close and reopen the stream with the new query string, carrying the
//     last seen event id as ?last_event_id= (a fresh EventSource cannot set the Last-Event-ID
//     header; the server treats both identically — doc 04 §3.1).
//   - QoS split (doc 04 §3.4): *.tick replaces snapshots via setQueryData; *.changed /
//     *.fired / *.resolved / *.progress invalidate the entity queries; the `reset` control
//     event forces snapshot refetch for its listed topics.
//   - Disconnect degradation: exponential backoff with jitter from the server's retry:5000
//     hint, plus ONE central 15s interval that invalidates active topics' snapshot queries —
//     the only interval timer permitted in the application (doc 08 §5).

import { useContext, useEffect, useMemo, useSyncExternalStore, createContext } from "react";
import type { QueryClient } from "@tanstack/react-query";
import { invalidateTopic, qk } from "./keys";
import type { OverviewSnapshot, SseEnvelope, SseResetPayload, SseTopic, TileValue } from "./types";

export type SseStatus = "idle" | "connecting" | "live" | "reconnecting" | "degraded";

// Minimal structural EventSource so unit tests inject a scripted fake (doc 12 P8 gate #4).
export interface EventSourceLike {
  addEventListener(type: string, listener: (ev: MessageEvent) => void): void;
  close(): void;
  onopen: ((ev: Event) => void) | null;
  onerror: ((ev: Event) => void) | null;
}
export type EventSourceFactory = (url: string) => EventSourceLike;

/** Closed event vocabulary per topic (doc 04 §3.2). New verbs are additive doc-first changes. */
export const TOPIC_EVENTS: Record<SseTopic, readonly string[]> = {
  overview: ["overview.tiles.tick"],
  provider: ["provider.health.changed"],
  key: ["key.status.changed"],
  queue: ["queue.stats.tick"],
  worker: ["worker.state.changed"],
  alert: ["alert.event.fired", "alert.event.resolved"],
  import: ["import.batch.progress"],
  approval: ["approval.request.changed"],
};

const RETRY_BASE_MS = 5_000; // server `retry:` hint (doc 04 §3.5)
const RETRY_CAP_MS = 60_000;
const DEGRADED_POLL_MS = 15_000;
/** Attempts before `reconnecting` is reported as `degraded` (the 15s fallback is already
 * running from the first failure; this only changes what the indicator announces). */
const DEGRADED_AFTER_ATTEMPTS = 2;

export interface SseManagerOptions {
  queryClient: QueryClient;
  /** Injected in tests; defaults to the browser EventSource. */
  eventSourceFactory?: EventSourceFactory;
  streamPath?: string;
  /** Injected in tests for deterministic jitter; defaults to Math.random. */
  random?: () => number;
}

export class SseManager {
  private readonly qc: QueryClient;
  private readonly factory: EventSourceFactory;
  private readonly streamPath: string;
  private readonly random: () => number;

  private refcounts = new Map<SseTopic, number>();
  private es: EventSourceLike | null = null;
  private connectedCsv = "";
  private lastEventId: string | null = null;
  private attempts = 0;
  private everConnected = false;

  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;

  private status: SseStatus = "idle";
  private statusListeners = new Set<() => void>();

  constructor(opts: SseManagerOptions) {
    this.qc = opts.queryClient;
    this.factory =
      opts.eventSourceFactory ??
      ((url: string) => new EventSource(url) as unknown as EventSourceLike);
    this.streamPath = opts.streamPath ?? "/v1/admin/streams";
    this.random = opts.random ?? Math.random;
  }

  // ---- topic subscription (refcounted) ----

  /** Register a mounted view's topics; returns the deregister function for unmount. */
  subscribe(topics: readonly SseTopic[]): () => void {
    for (const t of topics) {
      this.refcounts.set(t, (this.refcounts.get(t) ?? 0) + 1);
    }
    this.sync();
    let released = false;
    return () => {
      if (released) return;
      released = true;
      for (const t of topics) {
        const n = this.refcounts.get(t) ?? 0;
        if (n <= 1) this.refcounts.delete(t);
        else this.refcounts.set(t, n - 1);
      }
      this.sync();
    };
  }

  activeTopics(): SseTopic[] {
    return [...this.refcounts.keys()].sort();
  }

  private topicsCsv(): string {
    return this.activeTopics().join(",");
  }

  /** Reconcile the connection with the current topic union. */
  private sync(): void {
    const csv = this.topicsCsv();
    if (csv === this.connectedCsv && (this.es !== null || this.reconnectTimer !== null)) {
      return; // set unchanged and a connection (or scheduled retry) exists
    }
    this.clearReconnect();
    if (this.es) {
      this.es.close();
      this.es = null;
    }
    if (csv === "") {
      this.connectedCsv = "";
      this.stopPolling();
      this.attempts = 0;
      this.setStatus("idle");
      return;
    }
    this.connect(csv);
  }

  // ---- connection lifecycle ----

  private connect(csv: string): void {
    this.connectedCsv = csv;
    this.setStatus(this.everConnected || this.attempts > 0 ? "reconnecting" : "connecting");

    let url = `${this.streamPath}?topics=${encodeURIComponent(csv)}`;
    if (this.lastEventId !== null) {
      url += `&last_event_id=${encodeURIComponent(this.lastEventId)}`;
    }

    const es = this.factory(url);
    this.es = es;

    es.onopen = () => {
      if (this.es !== es) return;
      this.everConnected = true;
      this.attempts = 0;
      this.stopPolling();
      this.setStatus("live");
    };
    es.onerror = () => {
      if (this.es !== es) return;
      es.close();
      this.es = null;
      this.scheduleReconnect();
    };

    const seen = new Set<string>();
    for (const topic of csv.split(",") as SseTopic[]) {
      for (const name of TOPIC_EVENTS[topic] ?? []) {
        if (seen.has(name)) continue;
        seen.add(name);
        es.addEventListener(name, (ev) => {
          if (this.es !== es) return;
          this.handleEvent(name, ev);
        });
      }
    }
    es.addEventListener("reset", (ev) => {
      if (this.es !== es) return;
      this.handleReset(ev);
    });
  }

  private scheduleReconnect(): void {
    // Degradation fallback runs from the first failure; the indicator reports `reconnecting`
    // first, then `degraded` once the outage persists (doc 08 §5).
    this.startPolling();
    this.setStatus(this.attempts >= DEGRADED_AFTER_ATTEMPTS ? "degraded" : "reconnecting");

    const exp = Math.min(RETRY_BASE_MS * 2 ** this.attempts, RETRY_CAP_MS);
    const delay = exp / 2 + this.random() * (exp / 2); // jitter: thundering-herd guard
    this.attempts += 1;

    this.clearReconnect();
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      const csv = this.topicsCsv();
      if (csv === "") {
        this.sync();
        return;
      }
      this.connect(csv);
    }, delay);
  }

  private clearReconnect(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  private startPolling(): void {
    if (this.pollTimer !== null) return;
    this.pollTimer = setInterval(() => {
      for (const topic of this.activeTopics()) invalidateTopic(this.qc, topic);
      if (this.status === "reconnecting") this.setStatus("degraded");
    }, DEGRADED_POLL_MS);
  }

  private stopPolling(): void {
    if (this.pollTimer !== null) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }

  /** Tear down entirely (logout / auth loss). */
  close(): void {
    this.clearReconnect();
    this.stopPolling();
    if (this.es) {
      this.es.close();
      this.es = null;
    }
    this.connectedCsv = "";
    this.lastEventId = null;
    this.attempts = 0;
    this.setStatus("idle");
  }

  // ---- event routing (QoS split, doc 04 §3.4) ----

  private handleEvent(name: string, ev: MessageEvent): void {
    if (typeof ev.lastEventId === "string" && ev.lastEventId !== "") {
      this.lastEventId = ev.lastEventId;
    }
    let envelope: SseEnvelope;
    try {
      envelope = JSON.parse(String(ev.data)) as SseEnvelope;
    } catch {
      return; // malformed frame: the next tick/invalidate corrects any missed state
    }

    if (name.endsWith(".tick")) {
      this.applyTick(name, envelope);
      return;
    }
    // *.changed / *.fired / *.resolved / *.progress -> invalidate; refetch server truth.
    const topic = name.split(".")[0] as SseTopic;
    invalidateTopic(this.qc, topic);
  }

  private applyTick(name: string, envelope: SseEnvelope): void {
    if (name === "overview.tiles.tick") {
      const payload = envelope.payload as { tiles?: Record<string, TileValue> };
      if (!payload?.tiles) return;
      this.qc.setQueryData<OverviewSnapshot>(qk.overview.snapshot, (prev) => ({
        generated_at: envelope.ts,
        tiles: { ...(prev?.tiles ?? {}), ...payload.tiles },
      }));
      return;
    }
    if (name === "queue.stats.tick") {
      // Coalesced per-queue state-count vector (doc 04 §3.2): replace the fleet snapshot.
      this.qc.setQueryData(qk.queues.snapshot, envelope.payload);
      return;
    }
    // Unknown *.tick (additive server change): fall back to invalidation — never stale.
    invalidateTopic(this.qc, name.split(".")[0] as SseTopic);
  }

  private handleReset(ev: MessageEvent): void {
    if (typeof ev.lastEventId === "string" && ev.lastEventId !== "") {
      this.lastEventId = ev.lastEventId;
    }
    let payload: SseResetPayload | null = null;
    try {
      payload = JSON.parse(String(ev.data)) as SseResetPayload;
    } catch {
      payload = null;
    }
    const topics = payload?.topics?.length ? payload.topics : this.activeTopics();
    for (const t of topics) invalidateTopic(this.qc, t);
  }

  // ---- status store (useSyncExternalStore contract) ----

  getStatus = (): SseStatus => this.status;

  subscribeStatus = (listener: () => void): (() => void) => {
    this.statusListeners.add(listener);
    return () => this.statusListeners.delete(listener);
  };

  private setStatus(next: SseStatus): void {
    if (next === this.status) return;
    this.status = next;
    for (const l of [...this.statusListeners]) l();
  }

  // test-only inspection seam
  get lastSeenEventId(): string | null {
    return this.lastEventId;
  }
}

// ---- React glue ----

export const SseContext = createContext<SseManager | null>(null);

/** Declare the SSE topics a view needs while mounted (doc 08 §5). */
export function useSseTopics(topics: readonly SseTopic[]): void {
  const manager = useContext(SseContext);
  const key = topics.join(",");
  const stable = useMemo(() => topics.slice(), [key]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!manager || stable.length === 0) return;
    return manager.subscribe(stable);
  }, [manager, stable]);
}

const idleStatus = (): SseStatus => "idle";
const noopSubscribe = () => () => {};

/** Connection state for the top-bar indicator (live / reconnecting / degraded). */
export function useSseStatus(): SseStatus {
  const manager = useContext(SseContext);
  return useSyncExternalStore(
    manager ? manager.subscribeStatus : noopSubscribe,
    manager ? manager.getStatus : idleStatus,
    idleStatus,
  );
}
