// Queue DLQ redrive-confirm tests (doc 12 §P10 gate). Pure copy + helpers, plus a render smoke
// of the redrive ConfirmDialog via react-dom/server (vitest env is node; no jsdom).
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { ConfirmDialog } from "../../design/primitives";
import {
  REDRIVE_ALREADY_GONE,
  REDRIVE_EXPLAINER,
  redriveConsequences,
  replaySummary,
} from "./redrive";
import type { DeadLetter } from "./types";

const JOB: DeadLetter = {
  id: "j-8842",
  workflow_key: "work_email-default",
  attempts: 5,
  last_error: "AUTH: 401 invalid key",
  error_class: "AUTH",
  created_at: "2026-07-02T09:02:11Z",
  dead: true,
};

describe("redrive confirm copy — the G2 idempotency explainer (doc 09 §8.1)", () => {
  it("names attempts reset, at-least-once, exactly-once-effective, and the double-click no-op", () => {
    expect(REDRIVE_EXPLAINER).toMatch(/resets attempts to 0/i);
    expect(REDRIVE_EXPLAINER).toMatch(/at-least-once/i);
    expect(REDRIVE_EXPLAINER).toMatch(/exactly-once-effective/i);
    expect(REDRIVE_EXPLAINER).toMatch(/dead=true guard/i);
  });

  it("consequences enumerate the concrete effects for this job", () => {
    const c = redriveConsequences(JOB);
    expect(c.some((s) => s.includes("j-8842"))).toBe(true);
    expect(c.some((s) => s.includes("5 → 0"))).toBe(true);
    expect(c.some((s) => /Idempotency-Key ledger/i.test(s))).toBe(true);
  });

  it("404 outcome copy is an info message, not an error", () => {
    expect(REDRIVE_ALREADY_GONE).toMatch(/already redriven or gone/i);
  });
});

describe("replaySummary — human filter description", () => {
  it("summarizes an error-class filter", () => {
    expect(replaySummary({ error_class: ["PROVIDER_DOWN", "TRANSIENT"] })).toContain("PROVIDER_DOWN");
  });
  it("defaults to 'all parked jobs' when unfiltered", () => {
    expect(replaySummary({})).toMatch(/all parked jobs/i);
  });
});

describe("redrive ConfirmDialog render", () => {
  it("shows the explainer body + consequence bullets when open", () => {
    const html = renderToStaticMarkup(
      <ConfirmDialog
        open
        onClose={() => {}}
        onConfirm={() => {}}
        title="Redrive j-8842"
        body={REDRIVE_EXPLAINER}
        consequences={redriveConsequences(JOB)}
        confirmLabel="Redrive"
      />,
    );
    expect(html).toContain("Redrive j-8842");
    expect(html).toContain("exactly-once-effective");
    expect(html).toContain("j-8842");
    expect(html).toContain("Redrive"); // confirm button label
  });

  it("renders nothing when closed", () => {
    const html = renderToStaticMarkup(
      <ConfirmDialog open={false} onClose={() => {}} onConfirm={() => {}} title="x" confirmLabel="ok" />,
    );
    expect(html).toBe("");
  });
});
