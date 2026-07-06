import { NavLink } from "react-router";
import { Badge } from "./Badge";

export interface TabSpec {
  id: string;
  label: string;
  badge?: string;
}

export interface TabsProps {
  /** Each tab id is a route segment — tabs are deep links, never local state (doc 08 §6.2). */
  tabs: readonly TabSpec[];
  /** Base route the segment appends to, e.g. `/providers/hunter`. */
  basePath: string;
  value: string;
}

export function Tabs({ tabs, basePath, value }: TabsProps) {
  return (
    <nav className="p-tabs" aria-label="Sections">
      {tabs.map((t) => (
        <NavLink
          key={t.id}
          to={`${basePath}/${t.id}`}
          aria-current={t.id === value ? "page" : undefined}
        >
          {t.label}
          {t.badge ? <Badge status="neutral" label={t.badge} icon="dot" /> : null}
        </NavLink>
      ))}
    </nav>
  );
}
