"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { PiMapPinLineBold, PiCrosshairBold, PiStorefrontBold } from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { Button } from "@/components/ui/Button";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { SectionLabel, EmptyState, Surface } from "@/components/ui/Surface";
import {
  AnimatedComponent,
  slideInOut,
} from "@/components/ui/AnimatedComponents";
import { Map, Pin } from "@/components/map/Map";
import { AgentPin, YouPin, coverageOf } from "@/components/agents/AgentPin";
import { AgentRow } from "@/components/agents/AgentRow";
import { useLocation, locationOf } from "@/lib/geo/useLocation";
import { zoomForRadius } from "@/lib/geo/mercator";
import { agentsApi, parseAmount, toDecimalString, type Agent } from "@/lib/api";

/** How far out to look, in metres. Deliberately walkable, not city-wide. */
const RADIUS_M = 5_000;

export default function AgentsPage() {
  const { state, request } = useLocation();
  const here = locationOf(state);

  const [amountText, setAmountText] = useState("");
  const amountMinor = useMemo(
    () => (amountText ? parseAmount(amountText, "NGN") : null),
    [amountText],
  );
  const [selected, setSelected] = useState<string | null>(null);

  const agents = useQuery({
    queryKey: ["agents", "nearby", here?.lat, here?.lng, amountMinor],
    enabled: !!here,
    queryFn: () =>
      agentsApi.nearby({
        lat: here!.lat,
        lng: here!.lng,
        radius: RADIUS_M,
        amount: amountMinor ? toDecimalString(amountMinor, "NGN") : undefined,
      }),
    select: (d) => d.agents,
    // Float moves as other people hand cash over, so who can cover an amount
    // changes minute to minute. Stale enough to avoid thrashing, fresh enough
    // that a list somebody is reading is still true when they set off.
    refetchInterval: 30_000,
  });

  const zoom = useMemo(
    () => (typeof window === "undefined" ? 13 : zoomForRadius(RADIUS_M, Math.min(window.innerWidth, 428), here?.lat ?? 9)),
    [here?.lat],
  );

  return (
    <Screen>
      <AnimatedComponent variant={slideInOut} className="grid gap-5 py-8">
        <header className="grid gap-1">
          <h1 className="text-xl font-medium text-[var(--fg)]">Agents near you</h1>
          <p className="text-sm leading-relaxed text-[var(--fg-muted)]">
            People and shops who take cash and put it in your balance. Hand the
            notes over in person; both of you confirm.
          </p>
        </header>

        {/* Amount first, because it changes the answer. An agent list that
            ignores how much you are carrying is a list of places that might
            turn you away. */}
        <Surface kind="sunken" padding="md" radius="2xl" className="grid gap-2">
          <label
            htmlFor="amount"
            className="text-xs font-medium uppercase tracking-wider text-[var(--fg-subtle)]"
          >
            How much are you handing over?
          </label>
          <div className="flex items-center gap-2">
            <span className="text-lg text-[var(--fg-muted)]">₦</span>
            <input
              id="amount"
              inputMode="decimal"
              placeholder="Optional"
              value={amountText}
              onChange={(e) => setAmountText(e.target.value)}
              className="w-full bg-transparent text-lg font-medium tabular-nums text-[var(--fg)] outline-none placeholder:text-[var(--fg-subtle)] placeholder:font-normal placeholder:text-base"
            />
          </div>
          {amountText && amountMinor === null ? (
            <p className="text-xs text-[var(--negative)]">
              That is not an amount in naira.
            </p>
          ) : (
            <p className="text-xs text-[var(--fg-muted)]">
              Agents who cannot cover it are shown, and marked.
            </p>
          )}
        </Surface>

        {state.status !== "ready" ? (
          <LocationGate state={state} onRequest={request} />
        ) : null}

        {here ? (
          <>
            <Surface kind="outline" padding="none" radius="3xl" className="overflow-hidden">
              <Map centre={here} zoom={zoom} className="h-64 w-full">
                {(toScreen) => (
                  <>
                    <Pin at={here} toScreen={toScreen}>
                      <YouPin />
                    </Pin>
                    {(agents.data ?? []).map((a) => (
                      <Pin
                        key={a.id}
                        at={{ lat: a.lat, lng: a.lng }}
                        toScreen={toScreen}
                        onClick={() => setSelected(a.id === selected ? null : a.id)}
                      >
                        <AgentPin
                          coverage={coverageOf(a, amountMinor ?? undefined)}
                          selected={a.id === selected}
                          label={a.id === selected ? a.name : undefined}
                        />
                      </Pin>
                    ))}
                  </>
                )}
              </Map>
            </Surface>

            <SectionLabel
              action={
                <button
                  type="button"
                  onClick={request}
                  className="flex items-center gap-1 text-xs font-medium text-[var(--accent)]"
                >
                  <PiCrosshairBold /> Recentre
                </button>
              }
            >
              {agents.data?.length
                ? `${agents.data.length} within ${RADIUS_M / 1000}km`
                : "Nearby"}
            </SectionLabel>

            <AgentList
              agents={agents.data}
              loading={agents.isLoading}
              error={agents.error}
              amountMinor={amountMinor ?? undefined}
              selected={selected}
              onSelect={(a) => setSelected(a.id === selected ? null : a.id)}
            />
          </>
        ) : null}
      </AnimatedComponent>
    </Screen>
  );
}

function AgentList({
  agents,
  loading,
  error,
  amountMinor,
  selected,
  onSelect,
}: {
  agents?: Agent[];
  loading: boolean;
  error: unknown;
  amountMinor?: number;
  selected: string | null;
  onSelect: (a: Agent) => void;
}) {
  if (loading) {
    return (
      <Surface kind="sunken" radius="3xl" className="grid place-items-center py-10">
        <div className="loader" />
      </Surface>
    );
  }

  // A failed search is not an empty neighbourhood, and must not look like one.
  if (error) {
    return (
      <InfoBanner tone="warning">
        <p className="font-medium text-[var(--fg)]">Couldn&apos;t search for agents</p>
        <p className="mt-1 text-xs">
          {error instanceof Error ? error.message : "Try again in a moment."}
        </p>
      </InfoBanner>
    );
  }

  if (!agents?.length) {
    return (
      <EmptyState icon={<PiStorefrontBold />} title="No agents near you yet">
        The network is still growing here. Try a wider search later, or become
        an agent yourself.
      </EmptyState>
    );
  }

  return (
    <div className="grid gap-2">
      {agents.map((a) => (
        <AgentRow
          key={a.id}
          agent={a}
          amountMinor={amountMinor}
          selected={a.id === selected}
          onSelect={onSelect}
        />
      ))}
    </div>
  );
}

/**
 * The four location states, each said differently.
 *
 * A denial is not an outage and neither is a device that cannot get a fix, so
 * neither gets the generic "please enable location" that people have learned
 * to ignore.
 */
function LocationGate({
  state,
  onRequest,
}: {
  state: ReturnType<typeof useLocation>["state"];
  onRequest: () => void;
}) {
  if (state.status === "locating") {
    return (
      <Surface kind="sunken" radius="3xl" className="grid justify-items-center gap-3 py-8">
        <div className="loader" />
        <p className="text-xs text-[var(--fg-muted)]">Finding you…</p>
      </Surface>
    );
  }

  if (state.status === "denied") {
    return (
      <InfoBanner tone="warning">
        <p className="font-medium text-[var(--fg)]">Location is turned off</p>
        <p className="mt-1 text-xs leading-relaxed">
          Agents are ranked by how far you would have to walk, so this page
          needs to know where you are. Turn it back on in your browser&apos;s
          site settings, then try again.
        </p>
      </InfoBanner>
    );
  }

  if (state.status === "unavailable") {
    return (
      <InfoBanner tone="warning">
        <p className="font-medium text-[var(--fg)]">Couldn&apos;t find you</p>
        <p className="mt-1 text-xs leading-relaxed">{state.reason}</p>
        <Button
          variant="secondary"
          size="sm"
          fullWidth={false}
          className="mt-3"
          onClick={onRequest}
        >
          Try again
        </Button>
      </InfoBanner>
    );
  }

  return (
    <EmptyState
      icon={<PiMapPinLineBold />}
      title="Show agents around you"
      action={
        <Button fullWidth={false} className="px-4" onClick={onRequest}>
          Use my location
        </Button>
      }
    >
      We use it to rank agents by walking distance. It is not stored against
      your account.
    </EmptyState>
  );
}
