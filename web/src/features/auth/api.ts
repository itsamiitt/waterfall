// auth/api.ts — the ONLY place auth endpoint paths are named (doc 08 §2).
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { post, setCsrfToken } from "../../api/client";
import { qk } from "../../api/keys";
import type {
  LoginRequest,
  MfaConfirmResponse,
  MfaEnrollResponse,
  MfaVerifyRequest,
  SessionResponse,
} from "../../api/types";

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: LoginRequest) => post<SessionResponse>("/auth/login", body),
    onSuccess: (res) => {
      if (res.status === "ok") {
        setCsrfToken(res.csrf_token);
        void qc.invalidateQueries({ queryKey: qk.auth.me });
      }
    },
  });
}

export function useMfaVerify() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: MfaVerifyRequest) => post<SessionResponse>("/auth/mfa/verify", body),
    onSuccess: (res) => {
      if (res.status === "ok") {
        setCsrfToken(res.csrf_token);
        void qc.invalidateQueries({ queryKey: qk.auth.me });
      }
    },
  });
}

/** Begin TOTP enrollment; the provisioning URI is returned exactly once (doc 04 §2.1). */
export function useMfaEnroll() {
  return useMutation({
    mutationFn: () => post<MfaEnrollResponse>("/auth/mfa/enroll"),
  });
}

/** Confirm enrollment with the first code; recovery codes are returned exactly once. */
export function useMfaConfirm() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: MfaVerifyRequest) =>
      post<MfaConfirmResponse>("/auth/mfa/enroll/confirm", body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.auth.me }),
  });
}
