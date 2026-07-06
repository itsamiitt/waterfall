// features/rotation — strategy picker + params (doc 09 §4.1). Screen → endpoint:
// GET /rotation/strategies (closed vocab of 12) drives the select; PUT /key-pools/{id}/strategy
// saves (epoch bump → PoolState rebuild ≤1s UNVERIFIED → "propagating" until `key` events
// confirm, doc 04 §2.4).
import { useState } from "react";
import { Button, Input, Select, type SelectOption } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { usePutStrategy, useStrategies } from "./api";
import type { KeyPool } from "./types";

export function StrategyForm({ pool }: { pool: KeyPool }) {
  const strategies = useStrategies();
  const put = usePutStrategy(pool.id);
  const [strategy, setStrategy] = useState(pool.strategy);
  const [params, setParams] = useState(JSON.stringify(pool.strategy_params ?? {}, null, 0));
  const [paramErr, setParamErr] = useState<string | undefined>();

  const opts: SelectOption[] = (strategies.data?.strategies ?? []).map((s) => ({
    value: s.id,
    label: s.label ?? s.id,
  }));

  function save() {
    let parsed: Record<string, unknown> = {};
    try {
      parsed = params.trim() ? (JSON.parse(params) as Record<string, unknown>) : {};
      setParamErr(undefined);
    } catch {
      setParamErr("strategy_params must be valid JSON");
      return;
    }
    put.mutate(
      { strategy, strategy_params: parsed },
      { onSuccess: () => toast.success("Strategy saved — propagating (≤1s)") },
    );
  }

  return (
    <div className="section">
      <div className="section-title">Strategy</div>
      <div className="filter-bar">
        <Select
          label="Strategy"
          options={opts}
          value={strategy}
          placeholder={strategies.isPending ? "loading…" : "select strategy"}
          onChange={setStrategy}
        />
        <Input label="strategy_params (JSON)" value={params} onChange={setParams} mono error={paramErr} />
        <Button size="sm" variant="primary" loading={put.isPending} onClick={save}>
          Save strategy
        </Button>
      </div>
      {put.isSuccess ? <p className="p-field-description">propagating — PoolState rebuilding (UNVERIFIED ≤1s)</p> : null}
      {strategies.isError && isApiError(strategies.error) ? (
        <p className="p-field-error">Could not load strategy catalog ({strategies.error.code}).</p>
      ) : null}
    </div>
  );
}
