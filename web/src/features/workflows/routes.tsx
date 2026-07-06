// features/workflows — the lazy route boundary (doc 08 §10). Routes: /workflows (index),
// /workflows/:scope and /workflows/:scope/edit (builder; the editor renders read-only for a
// published version, so the detail route reuses it). Matches app/router.tsx.
import { useParams } from "react-router";
import { WorkflowListPage } from "./WorkflowListPage";
import { WorkflowEditorPage } from "./WorkflowEditor";
import "./workflows.css";

export function Component() {
  const { scope } = useParams();
  return scope ? <WorkflowEditorPage /> : <WorkflowListPage />;
}
