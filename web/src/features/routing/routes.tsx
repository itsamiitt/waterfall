// features/routing — the lazy route boundary (doc 08 §10). Routes: /routing (scope list),
// /routing/:scope/edit (dnd editor). One Component switches on the presence of :scope, matching
// app/router.tsx which mounts this chunk for both paths.
import { useParams } from "react-router";
import { RoutingListPage } from "./RoutingListPage";
import { RoutingEditorPage } from "./RoutingEditor";
import "./routing.css";

export function Component() {
  const { scope } = useParams();
  return scope ? <RoutingEditorPage /> : <RoutingListPage />;
}
