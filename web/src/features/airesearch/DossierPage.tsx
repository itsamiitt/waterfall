// features/airesearch — the full stored Dossier document, read-only (docs/research-intelligence/06).
// The Dossier is arbitrary composite JSON (per-section confidence + provenance; AI-inferred values
// carry source_type=ai_inference). It is rendered verbatim in a copyable JSON block — never reshaped
// or re-scored client-side.
import { useNavigate, useParams } from "react-router";
import { isApiError } from "../../api/client";
import { Button, CodeBlock, EmptyState } from "../../design/primitives";
import { useDossier } from "./api";
import { dossierHeadline } from "./logic";

export default function DossierPage() {
  const { id = "" } = useParams();
  const nav = useNavigate();
  const q = useDossier(id);
  const headline = q.data ? dossierHeadline(q.data) : null;

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>{headline ?? id}</h1>
        <span className="page-header-meta">
          <Button size="sm" variant="ghost" onClick={() => nav("/research")}>
            ← all dossiers
          </Button>
        </span>
      </div>

      {q.isError ? (
        <EmptyState
          variant={isApiError(q.error) && q.error.status === 404 ? "zero-results" : "error"}
          title={isApiError(q.error) && q.error.status === 404 ? "No dossier for this id" : "Could not load dossier"}
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Back to dossiers", onClick: () => nav("/research") }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading dossier" />
      ) : (
        <CodeBlock language="json" copyable code={JSON.stringify(q.data, null, 2)} />
      )}
    </>
  );
}
