// features/routing/RoutingLanes.tsx — the draggable lanes (doc 09 §6.1): priority order with
// per-Provider tri-state overrides, a NESTED parallel-group container (bounded cheap prefix,
// VR-6: 2–4), and the sequential/retry/failover chains. All edits flow up via onChange; the
// parent debounces the PATCH. Validation errors anchor to the offending lane (field-anchored,
// not toasts — doc 09 §6.3).
import { useState } from "react";
import { Badge, Button, Input } from "../../design/primitives";
import { SortableList } from "./SortableList";
import { TriStateControl } from "./TriState";
import type {
  EffectiveOverride,
  ProviderOverride,
  RoutingPolicyPayload,
  TriMode,
  ValidationEntry,
} from "./types";

export interface LanesEditorProps {
  payload: RoutingPolicyPayload;
  effective: Record<string, EffectiveOverride>;
  onChange: (next: RoutingPolicyPayload) => void;
  errorsByPath: Map<string, ValidationEntry[]>;
  disabled?: boolean;
}

function overrideFor(payload: RoutingPolicyPayload, provider: string): ProviderOverride {
  return payload.provider_overrides?.[provider] ?? { mode: "inherit" };
}

export function LanesEditor({ payload, effective, onChange, errorsByPath, disabled }: LanesEditorProps) {
  const [newProvider, setNewProvider] = useState("");
  const order = payload.waterfall?.order ?? [];
  const group = payload.waterfall?.parallel_group?.providers ?? [];

  function setWaterfall(patch: Partial<NonNullable<RoutingPolicyPayload["waterfall"]>>) {
    onChange({ ...payload, waterfall: { ...payload.waterfall, ...patch } });
  }

  function setOverride(provider: string, next: ProviderOverride) {
    onChange({
      ...payload,
      provider_overrides: { ...payload.provider_overrides, [provider]: next },
    });
  }

  function addProvider() {
    const id = newProvider.trim().toLowerCase();
    if (!id || order.includes(id)) return;
    setWaterfall({ order: [...order, id] });
    setNewProvider("");
  }

  function removeProvider(provider: string) {
    setWaterfall({
      order: order.filter((p) => p !== provider),
      parallel_group: group.includes(provider)
        ? { providers: group.filter((p) => p !== provider) }
        : payload.waterfall?.parallel_group,
    });
  }

  function toggleInGroup(provider: string) {
    const next = group.includes(provider)
      ? group.filter((p) => p !== provider)
      : [...group, provider];
    setWaterfall({ parallel_group: next.length ? { providers: next } : undefined });
  }

  return (
    <div className="rt-lanes">
      <section aria-label="Priority order">
        <h3 className="rt-lane-title">Priority order (drag to reorder)</h3>
        <SortableList
          items={order}
          ariaLabel="Provider priority order"
          disabled={disabled}
          onReorder={(next) => setWaterfall({ order: next })}
          renderItem={(provider) => {
            const ov = overrideFor(payload, provider);
            const laneErrors = errorsByPath.get(`/provider_overrides/${provider}/mode`) ?? [];
            const excluded = ov.mode === "off";
            const inGroup = group.includes(provider);
            return (
              <div className="rt-lane-row" data-invalid={laneErrors.length > 0 || undefined}>
                <div className="rt-lane-head">
                  <strong className="rt-provider">{provider}</strong>
                  {excluded ? <Badge status="neutral" label="excluded from order" icon="slash" /> : null}
                  {inGroup ? <Badge status="info" label="parallel" icon="dot" /> : null}
                </div>
                <TriStateControl
                  provider={provider}
                  mode={ov.mode}
                  effective={effective[provider]}
                  disabled={disabled}
                  onChange={(mode: TriMode) => setOverride(provider, { ...ov, mode })}
                />
                <div className="rt-lane-actions">
                  <label className="rt-priority">
                    priority
                    <input
                      type="number"
                      min={0}
                      max={1000}
                      className="p-input rt-priority-input"
                      value={ov.priority ?? ""}
                      disabled={disabled}
                      onChange={(e) =>
                        setOverride(provider, {
                          ...ov,
                          priority: e.currentTarget.value === "" ? undefined : Number(e.currentTarget.value),
                        })
                      }
                    />
                  </label>
                  <Button size="sm" variant="ghost" disabled={disabled} onClick={() => toggleInGroup(provider)}>
                    {inGroup ? "Remove from group" : "Add to parallel group"}
                  </Button>
                  <Button size="sm" variant="ghost" disabled={disabled} onClick={() => removeProvider(provider)}>
                    Remove
                  </Button>
                </div>
                {laneErrors.map((e) => (
                  <p key={e.rule} className="rt-lane-error" role="alert">
                    {e.rule}: {e.message}
                  </p>
                ))}
              </div>
            );
          }}
        />
        {!disabled ? (
          <div className="rt-add">
            <Input
              label="Add provider to order"
              value={newProvider}
              onChange={setNewProvider}
              placeholder="provider-slug"
            />
            <Button variant="secondary" onClick={addProvider}>
              Add
            </Button>
          </div>
        ) : null}
      </section>

      <section aria-label="Parallel group" className="rt-group">
        <h3 className="rt-lane-title">
          Parallel group — bounded cheap prefix{" "}
          <span className="rt-muted">(2–4 members, VR-6; each still passes G3/G4)</span>
        </h3>
        {group.length === 0 ? (
          <p className="rt-muted">
            No parallel group. Use "Add to parallel group" on a priority row to fan out that Provider.
          </p>
        ) : (
          <SortableList
            items={group}
            ariaLabel="Parallel group members"
            disabled={disabled}
            onReorder={(next) => setWaterfall({ parallel_group: { providers: next } })}
            renderItem={(p) => <span className="rt-provider">{p}</span>}
          />
        )}
        {group.length > 4 ? (
          <p className="rt-lane-error" role="alert">
            VR-6: parallel group exceeds the cap of 4 — the server validator will reject it.
          </p>
        ) : null}
      </section>

      <ChainSection
        title="Sequential chains"
        chains={payload.waterfall?.sequential_chains ?? []}
        disabled={disabled}
        onChange={(chains) => setWaterfall({ sequential_chains: chains.length ? chains : undefined })}
      />
      <FlatChain
        title="Retry order"
        items={payload.waterfall?.retry_order ?? []}
        disabled={disabled}
        onChange={(items) => setWaterfall({ retry_order: items.length ? items : undefined })}
      />
      <FlatChain
        title="Failover order"
        items={payload.waterfall?.failover_order ?? []}
        disabled={disabled}
        onChange={(items) => setWaterfall({ failover_order: items.length ? items : undefined })}
      />
    </div>
  );
}

function FlatChain({
  title,
  items,
  onChange,
  disabled,
}: {
  title: string;
  items: string[];
  onChange: (items: string[]) => void;
  disabled?: boolean;
}) {
  const [draft, setDraft] = useState("");
  return (
    <section aria-label={title} className="rt-chain">
      <h3 className="rt-lane-title">{title}</h3>
      {items.length === 0 ? (
        <p className="rt-muted">none</p>
      ) : (
        <SortableList
          items={items}
          ariaLabel={title}
          disabled={disabled}
          onReorder={onChange}
          renderItem={(p) => (
            <span className="rt-chain-row">
              <span className="rt-provider">{p}</span>
              {!disabled ? (
                <Button size="sm" variant="ghost" onClick={() => onChange(items.filter((x) => x !== p))}>
                  Remove
                </Button>
              ) : null}
            </span>
          )}
        />
      )}
      {!disabled ? (
        <div className="rt-add">
          <Input label={`Add to ${title}`} value={draft} onChange={setDraft} placeholder="provider-slug" />
          <Button
            variant="secondary"
            onClick={() => {
              const id = draft.trim().toLowerCase();
              if (id && !items.includes(id)) onChange([...items, id]);
              setDraft("");
            }}
          >
            Add
          </Button>
        </div>
      ) : null}
    </section>
  );
}

function ChainSection({
  title,
  chains,
  onChange,
  disabled,
}: {
  title: string;
  chains: string[][];
  onChange: (chains: string[][]) => void;
  disabled?: boolean;
}) {
  return (
    <section aria-label={title} className="rt-chain">
      <h3 className="rt-lane-title">{title}</h3>
      {chains.length === 0 ? <p className="rt-muted">none</p> : null}
      {chains.map((chain, i) => (
        <FlatChain
          key={i}
          title={`Chain ${i + 1}`}
          items={chain}
          disabled={disabled}
          onChange={(items) => {
            const next = chains.map((c, j) => (j === i ? items : c)).filter((c) => c.length > 0);
            onChange(next);
          }}
        />
      ))}
      {!disabled ? (
        <Button size="sm" variant="ghost" onClick={() => onChange([...chains, []])}>
          Add chain
        </Button>
      ) : null}
    </section>
  );
}
