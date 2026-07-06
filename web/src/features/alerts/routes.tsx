// features/alerts — lazy route boundary for /alerts and /alerts/rules/:id (doc 12 §P11).
import { useParams } from "react-router";
import { RequireRole } from "../../app/guards";
import AlertsPage from "./AlertsPage";
import RuleEditorRoute from "./RuleEditorRoute";

export function Component() {
  const params = useParams();
  const isRuleEditor = params.id !== undefined;
  return (
    <RequireRole group="alerts.read">{isRuleEditor ? <RuleEditorRoute /> : <AlertsPage />}</RequireRole>
  );
}
