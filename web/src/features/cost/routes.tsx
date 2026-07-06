// features/cost — lazy route boundary for /cost and /budgets (doc 12 §P11). A single feature
// chunk serves both routes; the pathname selects the page (router.tsx is not edited in P11).
import { useLocation } from "react-router";
import { RequireRole } from "../../app/guards";
import CostPage from "./CostPage";
import BudgetsPage from "./BudgetsPage";

export function Component() {
  const { pathname } = useLocation();
  const isBudgets = pathname.startsWith("/budgets");
  return (
    <RequireRole group="cost.read">{isBudgets ? <BudgetsPage /> : <CostPage />}</RequireRole>
  );
}
