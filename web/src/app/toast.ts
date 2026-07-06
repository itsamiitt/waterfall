// Toast store — UI-only state, so zustand (doc 08 §4: never holds server data).
import { create } from "zustand";
import type { ToastItem, ToastKind } from "../design/primitives";

interface ToastState {
  toasts: ToastItem[];
  push: (kind: ToastKind, message: string, action?: ToastItem["action"], ttlMs?: number) => void;
  dismiss: (id: string) => void;
}

const DEFAULT_TTL_MS = 6_000;

export const useToastStore = create<ToastState>((set, get) => ({
  toasts: [],
  push: (kind, message, action, ttlMs = DEFAULT_TTL_MS) => {
    const id = crypto.randomUUID();
    set((s) => ({ toasts: [...s.toasts, { id, kind, message, action }] }));
    if (ttlMs > 0) {
      setTimeout(() => get().dismiss(id), ttlMs);
    }
  },
  dismiss: (id) => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));

export const toast = {
  success: (message: string, action?: ToastItem["action"]) =>
    useToastStore.getState().push("success", message, action),
  error: (message: string, action?: ToastItem["action"]) =>
    useToastStore.getState().push("error", message, action),
  info: (message: string, action?: ToastItem["action"]) =>
    useToastStore.getState().push("info", message, action),
};
