// features/queues — the lazy route boundary (doc 08 §10). Routes: /queues (cards),
// /queues/:name (console), /dead-letters (DLQ). One Component dispatches on the matched path,
// matching app/router.tsx which mounts this chunk for all three.
import { useLocation, useParams } from "react-router";
import { QueuesPage } from "./QueuesPage";
import { QueueConsole } from "./QueueConsole";
import { DeadLettersPage } from "./DeadLettersPage";
import "./queues.css";

export function Component() {
  const { name } = useParams();
  const { pathname } = useLocation();
  if (pathname.startsWith("/dead-letters")) return <DeadLettersPage />;
  if (name) return <QueueConsole name={name} />;
  return <QueuesPage />;
}
