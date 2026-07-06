// features/keys — the /keys page (doc 09 §3.1). Header: provider selector + Add key + Import +
// (Export lives in KeysPanel where the loaded rows are). Screen → endpoint: GET /providers (to
// populate the selector), POST /providers/{id}/keys (add), everything else via KeysPanel.
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Button, EmptyState, Input, Modal, Select, type SelectOption } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { flattenPages } from "../../lib/cursors";
import { toast } from "../../app/toast";
import { useProviders } from "../providers/api";
import { KeysPanel } from "./KeysPanel";
import { useCreateKey } from "./api";

function AddKeyModal({ providerId, open, onClose }: { providerId: string; open: boolean; onClose: () => void }) {
  const create = useCreateKey(providerId);
  const [f, setF] = useState({ label: "", secret: "", region: "", environment: "", mfa: "" });
  const set = (k: keyof typeof f) => (v: string) => setF((s) => ({ ...s, [k]: v }));

  function submit() {
    create.mutate(
      {
        body: {
          label: f.label,
          secret: f.secret,
          region: f.region || undefined,
          environment: f.environment || undefined,
        },
        mfaCode: f.mfa || undefined,
      },
      {
        onSuccess: () => {
          toast.success("Key created — secret sealed, never echoed");
          onClose();
          setF({ label: "", secret: "", region: "", environment: "", mfa: "" });
        },
      },
    );
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Add provider key"
      busy={create.isPending}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={create.isPending} disabled={!f.label || !f.secret} onClick={submit}>
            Create key
          </Button>
        </>
      }
    >
      <Input label="Label" value={f.label} onChange={set("label")} required />
      <Input
        label="Secret"
        value={f.secret}
        onChange={set("secret")}
        mono
        autoComplete="off"
        required
        description="Write-only: sealed in-request, never displayed again (doc 04 §2.4)."
      />
      <Input label="Region" value={f.region} onChange={set("region")} />
      <Input label="Environment" value={f.environment} onChange={set("environment")} />
      <Input label="MFA code" value={f.mfa} onChange={set("mfa")} mono description="Required to add a key (doc 05 §5.4)." />
    </Modal>
  );
}

export default function KeysPage() {
  const [params, setParams] = useSearchParams();
  const provQ = useProviders({}, "priority");
  const providers = flattenPages(provQ.data?.pages);
  const options: SelectOption[] = providers.map((p) => ({ value: p.id, label: p.display_name }));
  const providerId = params.get("provider") ?? providers[0]?.id ?? "";
  const [adding, setAdding] = useState(false);

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Provider keys</h1>
      </div>

      <div className="action-bar">
        <Select
          label="Provider"
          options={options}
          value={providerId}
          placeholder={provQ.isPending ? "loading…" : "select provider"}
          onChange={(v) => setParams((p) => {
            p.set("provider", v);
            return p;
          })}
        />
        <Button size="sm" variant="primary" disabled={!providerId} onClick={() => setAdding(true)}>
          + Add key
        </Button>
        <Link to="/keys/import">
          <Button size="sm">Import</Button>
        </Link>
      </div>

      {provQ.isError ? (
        <EmptyState
          variant="error"
          title="Could not load providers"
          errorCode={isApiError(provQ.error) ? provQ.error.code : undefined}
          action={{ label: "Retry", onClick: () => void provQ.refetch() }}
        />
      ) : providerId === "" ? (
        provQ.isPending ? (
          <div className="skeleton" style={{ height: 320 }} aria-busy="true" />
        ) : (
          <EmptyState variant="zero-data" title="No providers to show keys for" action={{ label: "Go to providers", href: "/providers" }} />
        )
      ) : (
        <KeysPanel key={providerId} providerId={providerId} />
      )}

      {providerId ? <AddKeyModal providerId={providerId} open={adding} onClose={() => setAdding(false)} /> : null}
    </>
  );
}
