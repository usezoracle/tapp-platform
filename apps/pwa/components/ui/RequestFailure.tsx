"use client";

import { PiWarningBold } from "react-icons/pi";
import { InfoBanner } from "./InfoBanner";
import { ApiError } from "@/lib/api";

/**
 * The server's own words where there are any.
 *
 * `missing` is spelled out when the rail refused for want of a field, because
 * "more details are needed" is not something anybody can act on and "we still
 * need your NIN" is.
 */
export function apiMessage(error: unknown): string {
  if (error instanceof ApiError) {
    const missing = (error.data as { missing?: string[] } | undefined)?.missing;
    if (missing?.length) {
      return `We still need: ${missing.join(", ")}.`;
    }
    return error.message;
  }
  return error instanceof Error ? error.message : "Try again in a moment.";
}

/** A request that did not complete, in the server's words. */
export function RequestFailure({ error }: { error: unknown }) {
  return (
    <InfoBanner tone="warning" icon={<PiWarningBold />}>
      {apiMessage(error)}
    </InfoBanner>
  );
}
