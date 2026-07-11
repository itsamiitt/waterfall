// features/aimodels/api.ts — the ONLY place the AI models endpoint path is named (doc 08 §2).
//   AIModelsPage  GET /ai/models
import { useQuery } from "@tanstack/react-query";
import { get } from "../../api/client";
import { staleTimes } from "../../api/keys";
import type { ModelsResponse } from "./types";

/** Feature-local query keys (mirrors the api/keys.ts convention without editing shared state). */
export const mk = {
  models: ["ai", "models"] as const,
};

/** GET /ai/models — the platform LLM cascade catalog (free-first order). */
export function useAIModels() {
  return useQuery({
    queryKey: mk.models,
    queryFn: () => get<ModelsResponse>("/ai/models"),
    staleTime: staleTimes.config,
  });
}
