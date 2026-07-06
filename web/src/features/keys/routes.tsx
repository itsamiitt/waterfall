// features/keys — lazy route boundary (doc 08 §3). One Component dispatches the two Module 3
// routes: /keys (grid) and /keys/import (wizard).
import { useLocation } from "react-router";
import KeysPage from "./KeysPage";
import ImportWizard from "./ImportWizardPage";
import "./keys.css";

export function Component() {
  const { pathname } = useLocation();
  if (pathname.endsWith("/import")) return <ImportWizard />;
  return <KeysPage />;
}
