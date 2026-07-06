// features/rotation — lazy route boundary (doc 08 §3). Dispatches /key-pools (list),
// /key-pools/:id (detail), /rotation (engine view).
import { useLocation, useParams } from "react-router";
import PoolsList from "./PoolsList";
import PoolDetail from "./PoolDetail";
import RotationView from "./RotationView";
import "./rotation.css";

export function Component() {
  const { id } = useParams();
  const { pathname } = useLocation();
  if (id) return <PoolDetail />;
  if (pathname.startsWith("/rotation")) return <RotationView />;
  return <PoolsList />;
}
