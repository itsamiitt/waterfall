// Top-bar cross-entity search (GET /v1/admin/search?q=, doc 04 §2.13). The endpoint ships
// with P7 — until it is merged a 404 degrades to a friendly note, never an error state.
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { get, isApiError } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import type { Page, SearchKind, SearchResult } from "../../api/types";

const KIND_ROUTE: Record<SearchKind, (id: string) => string> = {
  provider: (id) => `/providers/${id}`,
  key: () => "/keys",
  pool: (id) => `/key-pools/${id}`,
  workflow: (id) => `/workflows/${id}`,
  worker: () => "/workers",
  queue: (name) => `/queues/${name}`,
  user: () => "/security/users",
};

export function SearchBox() {
  const [q, setQ] = useState("");
  const [open, setOpen] = useState(false);
  const [debounced, setDebounced] = useState("");
  const boxRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);

  useEffect(() => {
    function onDocMouseDown(e: MouseEvent) {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDocMouseDown);
    return () => document.removeEventListener("mousedown", onDocMouseDown);
  }, []);

  const results = useQuery({
    queryKey: qk.search(debounced),
    queryFn: () => get<Page<SearchResult>>(`/search?q=${encodeURIComponent(debounced)}&limit=20`),
    enabled: open && debounced.length >= 2,
    staleTime: staleTimes.telemetry,
    retry: false,
  });

  const unavailable = results.isError && isApiError(results.error) && results.error.status === 404;

  return (
    <div className="searchbox" ref={boxRef} role="search">
      <input
        className="p-input"
        type="search"
        placeholder="Search providers, keys, queues…"
        aria-label="Search"
        value={q}
        onFocus={() => setOpen(true)}
        onChange={(e) => setQ(e.currentTarget.value)}
      />
      {open && debounced.length >= 2 ? (
        <div className="searchbox-results">
          {unavailable ? (
            <p className="searchbox-note">Search is not available yet on this server.</p>
          ) : results.isError ? (
            <p className="searchbox-note">
              Search failed{isApiError(results.error) ? ` (${results.error.code})` : ""}.
            </p>
          ) : results.isPending ? (
            <p className="searchbox-note">Searching…</p>
          ) : results.data.items.length === 0 ? (
            <p className="searchbox-note">No matches.</p>
          ) : (
            <ul>
              {results.data.items.map((r) => (
                <li key={`${r.kind}:${r.id}`}>
                  <Link to={KIND_ROUTE[r.kind]?.(r.id) ?? "/"} onClick={() => setOpen(false)}>
                    <span className="searchbox-kind">{r.kind}</span>
                    <span>{r.label}</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </div>
  );
}
