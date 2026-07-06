// features/health/types.ts — Module 5 DTOs (doc 04 §2.6), feature-local.
import type { DaySegment, HourBucket } from "./timelineModel";

export interface HealthRow {
  provider_id: string;
  display_name?: string;
  health: string;
  uptime_90d_pct?: number;
  p95_ms?: number;
  p99_ms?: number;
  last_error_class?: string | null;
  region?: string;
}

export interface HealthTimeline {
  provider_id: string;
  days: DaySegment[];
  hours: HourBucket[];
}

export interface RegionalRow {
  provider_id: string;
  display_name?: string;
  /** region → health state. */
  cells: Record<string, string>;
}
export interface RegionalMatrix {
  regions: string[];
  providers: RegionalRow[];
}

export interface HealthSchedule {
  provider_id: string;
  interval_s: number;
  jitter_pct: number;
  regions: string[];
}
export interface HealthSchedules {
  schedules: HealthSchedule[];
}

export interface HealthFilter {
  status?: string;
  region?: string;
}
