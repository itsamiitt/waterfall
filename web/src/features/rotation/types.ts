// features/rotation/types.ts — Module 4 DTOs (doc 04 §2.4 pools + §2.5 rotation), feature-local.

export interface KeyPool {
  id: string;
  provider_id: string;
  name: string;
  selector: string;
  strategy: string;
  strategy_params?: Record<string, unknown>;
  member_count?: number;
  status?: string;
  owner_tenant_id?: string | null;
  updated_at?: string;
}

export interface PoolMember {
  key_id: string;
  label: string;
  status: string;
  weight?: number;
  success_ewma?: number;
  latency_ewma_ms?: number;
  credits_remaining?: number;
}

export interface PoolDetail extends KeyPool {
  members?: PoolMember[];
}

/** One of the 12 strategies from GET /rotation/strategies (closed vocab, doc 04 §2.5). */
export interface Strategy {
  id: string;
  label?: string;
  description?: string;
  param_schema?: Record<string, unknown>;
}
export interface StrategyCatalog {
  strategies: Strategy[];
}

/** GET /key-pools/{id}/selection-state — per-instance cache; diagnostic, not truth (doc 04 §2.5). */
export interface SelectionState {
  strategy: string;
  ring_index?: number;
  epoch?: number;
  available?: number;
  total?: number;
  bands?: Record<string, number>;
  [extra: string]: unknown;
}

export interface SimSlot {
  key_id: string;
  label?: string;
  count: number;
  pct: number;
}
export interface SimulateResult {
  draws: number;
  distribution: SimSlot[];
}

/** GET/PUT /rotation/triggers — error-class → KM-3 transition config (doc 04 §2.5, doc 07). */
export interface TriggerRule {
  error_class: string;
  transition: string;
  cooldown_s?: number;
  auto?: boolean;
}
export interface Triggers {
  rules: TriggerRule[];
}

export interface PoolFilter {
  provider_id?: string;
  strategy?: string;
}
