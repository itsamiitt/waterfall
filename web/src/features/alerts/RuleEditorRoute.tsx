// features/alerts/RuleEditorRoute.tsx — data wrapper for the rule builder (/alerts/rules/:id).
// id === "new" is create mode; otherwise the rule is loaded and edited. Keeps all data hooks out
// of the presentational RuleEditor so that stays unit-testable without a QueryClient.
import { useNavigate, useParams } from "react-router";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { Button, EmptyState } from "../../design/primitives";
import { useChannels, useCreateRule, useRule, useTestRule, useUpdateRule } from "./api";
import { RuleEditor, defaultRuleInput } from "./RuleEditor";
import type { AlertRuleInput } from "./types";

export default function RuleEditorRoute() {
  const { id } = useParams();
  const isNew = id === "new" || !id;
  const navigate = useNavigate();
  const rule = useRule(isNew ? undefined : id);
  const channels = useChannels();
  const create = useCreateRule();
  const update = useUpdateRule(id ?? "");
  const test = useTestRule(id ?? "");

  if (!isNew && rule.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load rule"
        errorCode={isApiError(rule.error) ? rule.error.code : undefined}
        action={{ label: "Back to alerts", href: "/alerts" }}
      />
    );
  }
  if ((!isNew && rule.isPending) || channels.isPending) {
    return <div className="skeleton" style={{ height: 360 }} aria-busy="true" aria-label="Loading rule editor" />;
  }

  const initial: AlertRuleInput = isNew
    ? defaultRuleInput()
    : {
        name: rule.data!.name,
        metric: rule.data!.metric,
        scope: rule.data!.scope ?? {},
        op: rule.data!.op,
        threshold: rule.data!.threshold,
        window_s: rule.data!.window_s,
        cooldown_s: rule.data!.cooldown_s,
        severity: rule.data!.severity,
        channels: rule.data!.channels ?? [],
        enabled: rule.data!.enabled,
      };

  function onSubmit(input: AlertRuleInput) {
    const opts = {
      onSuccess: () => {
        toast.success(isNew ? "Rule created" : "Rule saved");
        void navigate("/alerts");
      },
      onError: (e: unknown) =>
        toast.error(
          isApiError(e) && e.code === "validation_failed" ? `Rejected: ${e.message}` : "Save failed",
        ),
    };
    if (isNew) create.mutate(input, opts);
    else update.mutate(input, opts);
  }

  const testMessage = test.data
    ? `Would ${test.data.would_fire ? "FIRE" : "not fire"} now — current value ${test.data.value} (no notification sent)`
    : undefined;

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>{isNew ? "New alert rule" : "Edit alert rule"}</h1>
        <span className="page-header-meta">
          <Button size="sm" onClick={() => void navigate("/alerts")}>
            Back
          </Button>
        </span>
      </div>
      <RuleEditor
        initial={initial}
        channels={channels.data?.items ?? []}
        saving={create.isPending || update.isPending}
        testing={test.isPending}
        testMessage={testMessage}
        onSubmit={onSubmit}
        onTest={isNew ? undefined : () => test.mutate()}
      />
    </>
  );
}
