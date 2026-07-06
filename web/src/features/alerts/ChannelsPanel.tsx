// features/alerts/ChannelsPanel.tsx — CHANNELS (doc 09 §12.1). Reusable typed contact points;
// config is sealed to secret_envelopes and never echoed. Test-send exercises the REAL notifier
// path (SSRF-guarded) — an egress_blocked result is the feature working, surfaced inline in the
// row. Channel creation requires X-MFA-Code (doc 04 §2.11), collected in the add form.
import { useState } from "react";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { Badge, Button, EmptyState, Input, Select, type SelectOption } from "../../design/primitives";
import { unknownStatus } from "../../lib/status";
import { useChannels, useCreateChannel, useDeleteChannel, useTestChannel } from "./api";
import type { ChannelTestResult } from "./types";
import { CHANNEL_KINDS, type ChannelKind } from "./vocab";

const kindOpts: SelectOption<ChannelKind>[] = CHANNEL_KINDS.map((k) => ({ value: k, label: k }));

export function ChannelsPanel() {
  const channels = useChannels();
  const create = useCreateChannel();
  const del = useDeleteChannel();
  const test = useTestChannel();
  const [results, setResults] = useState<Record<string, ChannelTestResult>>({});
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ name: "", kind: "slack" as ChannelKind, target: "", mfaCode: "" });

  function runTest(id: string) {
    test.mutate(id, {
      onSuccess: (res) => setResults((r) => ({ ...r, [id]: res })),
      onError: (e) =>
        setResults((r) => ({
          ...r,
          [id]: { ok: false, error_code: isApiError(e) ? e.code : "internal" },
        })),
    });
  }

  function submitAdd() {
    const config: Record<string, string> = {};
    config[form.kind === "email" ? "email" : "url"] = form.target;
    create.mutate(
      { name: form.name, kind: form.kind, config, mfaCode: form.mfaCode },
      {
        onSuccess: () => {
          toast.success("Channel created");
          setAdding(false);
          setForm({ name: "", kind: "slack", target: "", mfaCode: "" });
        },
        onError: (e) =>
          toast.error(
            isApiError(e) && e.code === "mfa_required"
              ? "MFA code missing or invalid"
              : isApiError(e)
                ? `Create failed (${e.code})`
                : "Create failed",
          ),
      },
    );
  }

  const items = channels.data?.items ?? [];

  return (
    <section>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "var(--space-3)" }}>
        <h2>Channels</h2>
        <Button size="sm" onClick={() => setAdding((a) => !a)}>
          + Add channel
        </Button>
      </div>
      <p style={{ color: "var(--color-text-muted)", fontSize: "var(--text-sm)" }}>
        Config is sealed to secret_envelopes — URLs are never echoed back.
      </p>

      {adding ? (
        <div style={{ display: "flex", gap: "var(--space-3)", flexWrap: "wrap", alignItems: "flex-end", marginBottom: "var(--space-4)" }}>
          <Input label="Name" value={form.name} onChange={(v) => setForm({ ...form, name: v })} required />
          <Select label="Kind" options={kindOpts} value={form.kind} onChange={(v) => setForm({ ...form, kind: v })} />
          <Input
            label={form.kind === "email" ? "Email address" : "Destination URL"}
            value={form.target}
            onChange={(v) => setForm({ ...form, target: v })}
          />
          <Input
            label="MFA code (X-MFA-Code)"
            value={form.mfaCode}
            onChange={(v) => setForm({ ...form, mfaCode: v })}
            inputMode="numeric"
            autoComplete="one-time-code"
          />
          <Button variant="primary" onClick={submitAdd} loading={create.isPending} disabled={!form.name || !form.mfaCode}>
            Create
          </Button>
        </div>
      ) : null}

      {channels.isError ? (
        <EmptyState
          variant="error"
          title="Could not load channels"
          errorCode={isApiError(channels.error) ? channels.error.code : undefined}
          action={{ label: "Retry", onClick: () => void channels.refetch() }}
        />
      ) : channels.isPending ? (
        <div className="skeleton" style={{ height: 160 }} aria-busy="true" aria-label="Loading channels" />
      ) : items.length === 0 ? (
        <EmptyState variant="zero-data" title="Add a channel — rules cannot notify without one" />
      ) : (
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">name</th>
              <th scope="col">kind</th>
              <th scope="col">status</th>
              <th scope="col">last test</th>
              <th scope="col">actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((c) => {
              const res = results[c.id];
              const st = unknownStatus(c.status);
              return (
                <tr key={c.id}>
                  <td>{c.name}</td>
                  <td>{c.kind}</td>
                  <td>
                    <Badge status={st.token} icon={st.icon} label={st.label} />
                  </td>
                  <td>
                    {res ? (
                      res.ok ? (
                        <Badge status="ok" icon="check" label={`ok ${res.response_code ?? ""}`} />
                      ) : (
                        <Badge status="error" icon="triangle" label={res.error_code ?? "failed"} />
                      )
                    ) : c.last_test ? (
                      c.last_test.ok ? (
                        `ok ${c.last_test.response_code ?? ""}`
                      ) : (
                        (c.last_test.error_code ?? "failed")
                      )
                    ) : (
                      "—"
                    )}
                  </td>
                  <td style={{ display: "flex", gap: "var(--space-2)" }}>
                    <Button size="sm" onClick={() => runTest(c.id)} loading={test.isPending && test.variables === c.id}>
                      Test send
                    </Button>
                    <Button
                      size="sm"
                      variant="danger"
                      onClick={() =>
                        del.mutate(c.id, {
                          onError: (e) =>
                            toast.error(isApiError(e) && e.code === "conflict" ? "Channel is referenced by enabled rules" : "Delete failed"),
                        })
                      }
                    >
                      delete
                    </Button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}
