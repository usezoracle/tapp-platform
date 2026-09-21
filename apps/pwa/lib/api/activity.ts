/**
 * The activity feed: every movement of the holder's money.
 *
 * Derived on the server from the ledger entries themselves rather than from a
 * parallel record of "things that happened", so the feed and the balance above
 * it are the same rows read two ways and cannot disagree.
 */

import { request } from "./http";
import type { Money } from "./money";

export interface Movement {
  id: number;
  txId: string;
  /** Signed from the holder's point of view: positive arrived, negative left. */
  amount: Money;
  /** Which of their accounts moved. */
  account: "available" | "escrow" | "obligation";
  /**
   * The ledger's own vocabulary -- "tap.debit", "handover.settled",
   * "fx.bought". The part before the dot is the domain.
   */
  reason: string;
  refType?: string;
  refId?: string;
  at: string;
  /**
   * Where a card was tapped. Present on movements whose `refType` is
   * "tap"; null on everything else. `symbol` names the business's listing
   * when it has one, which is what a tap there buys a slice of.
   */
  merchant?: Merchant | null;
}

export interface Merchant {
  ref: string;
  name: string;
  symbol: string | null;
}

export interface ActivityPage {
  movements: Movement[];
  /**
   * Opaque. A keyset, not an offset: a movement arriving while somebody reads
   * page one must not push another movement off page two.
   */
  nextCursor?: string;
}

export const activityApi = {
  page: (jwt: string, limit = 30, cursor?: string) => {
    const params = new URLSearchParams({ limit: String(limit) });
    if (cursor) params.set("cursor", cursor);
    return request<ActivityPage>("GET", `/v1/me/activity?${params}`, { token: jwt });
  },
};
