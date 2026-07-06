// features/providers — lazy route boundary (doc 08 §3). One Component dispatches the three
// Module 2 routes: /providers (list), /providers/compare, /providers/:id/* (detail + tabs).
import { useLocation, useParams } from "react-router";
import ProvidersList from "./ProvidersList";
import ProviderDetail from "./ProviderDetail";
import CompareView from "./CompareView";
import "./providers.css";

export function Component() {
  const { id } = useParams();
  const { pathname } = useLocation();
  if (id) return <ProviderDetail />;
  if (pathname.endsWith("/compare")) return <CompareView />;
  return <ProvidersList />;
}
