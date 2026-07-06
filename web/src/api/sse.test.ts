// SSE manager unit tests (doc 12 P8 acceptance #4) with a scripted EventSource stub:
// topic refcounting, topic-set-change reconnect with last_event_id, QoS routing
// (tick-replace vs changed-invalidate), reset handling, backoff + 15s degraded fallback.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { SseManager, type EventSourceLike } from "./sse";
import { qk } from "./keys";

class FakeEventSource implements EventSourceLike {
  static instances: FakeEventSource[] = [];
  readonly url: string;
  closed = false;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  private listeners = new Map<string, ((ev: MessageEvent) => void)[]>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: (ev: MessageEvent) => void): void {
    const arr = this.listeners.get(type) ?? [];
    arr.push(listener);
    this.listeners.set(type, arr);
  }

  close(): void {
    this.closed = true;
  }

  open(): void {
    this.onopen?.({} as Event);
  }

  fail(): void {
    this.onerror?.({} as Event);
  }

  emit(type: string, data: unknown, lastEventId = ""): void {
    const ev = { data: JSON.stringify(data), lastEventId } as MessageEvent;
    for (const l of this.listeners.get(type) ?? []) l(ev);
  }

  static latest(): FakeEventSource {
    const es = FakeEventSource.instances[FakeEventSource.instances.length - 1];
    if (!es) throw new Error("no EventSource created");
    return es;
  }
}

let qc: QueryClient;
let manager: SseManager;

beforeEach(() => {
  FakeEventSource.instances = [];
  qc = new QueryClient();
  manager = new SseManager({
    queryClient: qc,
    eventSourceFactory: (url) => new FakeEventSource(url),
    random: () => 0, // deterministic jitter
  });
});

afterEach(() => {
  manager.close();
  qc.clear();
  vi.useRealTimers();
});

const envelope = (payload: unknown, scope: Record<string, string> = { tenant_id: "acme" }) => ({
  v: 1,
  ts: "2026-07-02T12:40:04Z",
  scope,
  payload,
});

describe("topic refcounting and reconnect", () => {
  it("opens ONE stream with the union of subscribed topics", () => {
    manager.subscribe(["overview"]);
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(FakeEventSource.latest().url).toBe("/v1/admin/streams?topics=overview");
  });

  it("a second subscriber to the same topic does NOT reconnect", () => {
    const un1 = manager.subscribe(["overview"]);
    manager.subscribe(["overview"]);
    expect(FakeEventSource.instances).toHaveLength(1);
    un1(); // one of two refs released: still subscribed, still connected
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(FakeEventSource.latest().closed).toBe(false);
  });

  it("topic-set change reconnects with the new csv AND the last seen event id", () => {
    manager.subscribe(["overview"]);
    const first = FakeEventSource.latest();
    first.open();
    first.emit("overview.tiles.tick", envelope({ tiles: {} }), "1782996002101-1");

    manager.subscribe(["queue"]);
    expect(first.closed).toBe(true);
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.latest().url).toBe(
      "/v1/admin/streams?topics=overview%2Cqueue&last_event_id=1782996002101-1",
    );
  });

  it("releasing the last subscriber closes the stream and goes idle", () => {
    const un = manager.subscribe(["overview"]);
    const es = FakeEventSource.latest();
    es.open();
    expect(manager.getStatus()).toBe("live");
    un();
    expect(es.closed).toBe(true);
    expect(manager.getStatus()).toBe("idle");
  });

  it("unsubscribe is idempotent (double-release does not underflow the refcount)", () => {
    const un1 = manager.subscribe(["overview"]);
    manager.subscribe(["overview"]);
    un1();
    un1(); // second call must be a no-op
    expect(manager.activeTopics()).toEqual(["overview"]);
  });
});

describe("QoS event routing (doc 04 §3.4)", () => {
  it("overview.tiles.tick REPLACES the snapshot via setQueryData", () => {
    qc.setQueryData(qk.overview.snapshot, {
      generated_at: "2026-07-02T12:40:00Z",
      tiles: { keys_degraded: { value: 14 }, workers_lost: { value: 1 } },
    });
    manager.subscribe(["overview"]);
    const es = FakeEventSource.latest();
    es.open();
    es.emit(
      "overview.tiles.tick",
      envelope({ tiles: { keys_degraded: { value: 15 } } }),
      "100-1",
    );
    const snap = qc.getQueryData<{ generated_at: string; tiles: Record<string, unknown> }>(
      qk.overview.snapshot,
    );
    expect(snap?.generated_at).toBe("2026-07-02T12:40:04Z");
    expect(snap?.tiles["keys_degraded"]).toEqual({ value: 15 });
    expect(snap?.tiles["workers_lost"]).toEqual({ value: 1 }); // partial tick merges over snapshot
  });

  it("key.status.changed INVALIDATES the entity queries (never merges payloads)", () => {
    const spy = vi.spyOn(qc, "invalidateQueries");
    manager.subscribe(["key"]);
    const es = FakeEventSource.latest();
    es.open();
    es.emit("key.status.changed", envelope({ status: "rate_limited", previous: "active" }), "7-1");
    const keys = spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey));
    expect(keys).toContain(JSON.stringify(qk.keys.root));
    expect(keys).toContain(JSON.stringify(qk.providers.root));
  });

  it("reset forces a snapshot refetch for the listed topics", () => {
    const spy = vi.spyOn(qc, "invalidateQueries");
    manager.subscribe(["overview", "import"]);
    const es = FakeEventSource.latest();
    es.open();
    es.emit("reset", { v: 1, topics: ["import"] }, "9-1");
    const keys = spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey));
    expect(keys).toContain(JSON.stringify(qk.imports.root));
    expect(keys).not.toContain(JSON.stringify(qk.overview.snapshot));
  });

  it("tracks Last-Event-ID from every received event", () => {
    manager.subscribe(["alert"]);
    const es = FakeEventSource.latest();
    es.open();
    es.emit("alert.event.fired", envelope({ severity: "critical" }), "555-2");
    expect(manager.lastSeenEventId).toBe("555-2");
  });
});

describe("disconnect degradation (doc 08 §5)", () => {
  it("error -> reconnecting -> backoff reconnect carries last_event_id", () => {
    vi.useFakeTimers();
    manager.subscribe(["overview"]);
    const first = FakeEventSource.latest();
    first.open();
    first.emit("overview.tiles.tick", envelope({ tiles: {} }), "42-1");
    expect(manager.getStatus()).toBe("live");

    first.fail();
    expect(manager.getStatus()).toBe("reconnecting");

    // random()=0 -> delay = base/2 = 2500ms for the first attempt
    vi.advanceTimersByTime(2500);
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.latest().url).toContain("last_event_id=42-1");

    FakeEventSource.latest().open();
    expect(manager.getStatus()).toBe("live");
  });

  it("while disconnected, the central 15s interval invalidates active-topic snapshots", () => {
    vi.useFakeTimers();
    const spy = vi.spyOn(qc, "invalidateQueries");
    manager.subscribe(["queue"]);
    FakeEventSource.latest().open();
    FakeEventSource.latest().fail();
    spy.mockClear();

    vi.advanceTimersByTime(2500); // reconnect attempt #2 spins up...
    FakeEventSource.latest().fail(); // ...and fails again

    vi.advanceTimersByTime(15_000);
    const keys = spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey));
    expect(keys).toContain(JSON.stringify(qk.queues.root));
    expect(manager.getStatus()).toBe("degraded");
  });

  it("reconnect stops the degraded polling interval", () => {
    vi.useFakeTimers();
    const spy = vi.spyOn(qc, "invalidateQueries");
    manager.subscribe(["queue"]);
    FakeEventSource.latest().open();
    FakeEventSource.latest().fail();
    vi.advanceTimersByTime(2500);
    FakeEventSource.latest().open(); // back to live
    spy.mockClear();
    vi.advanceTimersByTime(45_000);
    expect(spy).not.toHaveBeenCalled(); // no interval survives while live
    expect(manager.getStatus()).toBe("live");
  });

  it("exponential backoff grows the retry delay", () => {
    vi.useFakeTimers();
    manager.subscribe(["overview"]);
    FakeEventSource.latest().fail(); // attempt 0 -> 2500ms
    vi.advanceTimersByTime(2500);
    expect(FakeEventSource.instances).toHaveLength(2);
    FakeEventSource.latest().fail(); // attempt 1 -> 5000ms
    vi.advanceTimersByTime(2500);
    expect(FakeEventSource.instances).toHaveLength(2); // not yet
    vi.advanceTimersByTime(2500);
    expect(FakeEventSource.instances).toHaveLength(3);
  });

  it("status transitions notify subscribers (top-bar indicator contract)", () => {
    const seen: string[] = [];
    manager.subscribeStatus(() => seen.push(manager.getStatus()));
    manager.subscribe(["overview"]);
    FakeEventSource.latest().open();
    expect(seen).toEqual(["connecting", "live"]);
  });
});
