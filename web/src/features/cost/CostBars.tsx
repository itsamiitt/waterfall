// features/cost/CostBars.tsx — recharts spend-by-dimension bars (doc 09 §10.1). Loaded via
// React.lazy from CostPage so recharts stays out of the initial chunk (doc 08 §10) and out of
// the node unit tests. Colors are tokens only (no hardcoded hex, doc 08 §6.1); the adjacent
// breakdown table is the accessible representation, so the SVG is aria-hidden.
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatCompact } from "../../lib/format";
import { DIM_FIELD, type CostItem, type GroupBy } from "./types";

export interface CostBarsProps {
  items: CostItem[];
  groupBy: GroupBy;
}

export default function CostBars({ items, groupBy }: CostBarsProps) {
  const field = DIM_FIELD[groupBy];
  const data = items.map((it) => ({
    name: String(it[field] ?? "—"),
    credits: it.credits,
  }));

  return (
    <div aria-hidden="true">
      <ResponsiveContainer width="100%" height={220}>
        <BarChart data={data} margin={{ top: 8, right: 8, bottom: 4, left: 4 }}>
          <CartesianGrid stroke="var(--color-border)" strokeDasharray="2 4" vertical={false} />
          <XAxis
            dataKey="name"
            stroke="var(--color-text-faint)"
            tick={{ fill: "var(--color-text-muted)", fontSize: 12 }}
          />
          <YAxis
            stroke="var(--color-text-faint)"
            tick={{ fill: "var(--color-text-muted)", fontSize: 12 }}
            tickFormatter={(v: number) => formatCompact(v)}
            width={52}
          />
          <Tooltip
            formatter={(v) => [formatCompact(Number(v)), "credits"]}
            contentStyle={{
              background: "var(--color-bg-raised)",
              border: "1px solid var(--color-border)",
              borderRadius: "var(--radius-1)",
              color: "var(--color-text)",
            }}
          />
          <Bar dataKey="credits" fill="var(--color-accent)" isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
