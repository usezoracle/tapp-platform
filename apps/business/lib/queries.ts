"use client";

import { useQuery } from "@tanstack/react-query";
import { ApiError, getBusiness, getMe, type Business } from "./api";

/**
 * The merchant's business, or null when none has been submitted (404).
 * Any other failure is thrown, so the page can say what went wrong.
 */
export function useBusiness(token: string) {
  return useQuery<Business | null, ApiError>({
    queryKey: ["business", token],
    queryFn: async () => {
      try {
        return await getBusiness(token);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
  });
}

export function useMe(token: string) {
  return useQuery({ queryKey: ["me", token], queryFn: () => getMe(token), staleTime: 5 * 60_000 });
}

export const businessKey = (token: string) => ["business", token] as const;
