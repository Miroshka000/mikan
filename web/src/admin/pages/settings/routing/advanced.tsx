import { Card } from "../../../../components/layout";
import { Select } from "../../../../components/select";
import { SwitchRow } from "../../../../components/switch";
import { Field, Segmented } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { CoreBalance } from "./balance";
import type { Routes, SetRoutes } from "./model";

/** How the server groups look in the apps, each core's own balance, and the switches for
 * experts. error is a refusal of the servers' settings: the common ones', or a core's own. */
export function AdvancedPart({ routes, setRoutes, error }: { routes: Routes; setRoutes: SetRoutes; error?: { text: string; ofCore: boolean } }) {
  const servers = routes.servers ?? {};
  const setServers = (patch: Partial<NonNullable<Routes["servers"]>>) => setRoutes((r) => ({ ...r, servers: { ...(r.servers ?? {}), ...patch } }));
  const tune = routes.tune ?? {};
  const setTune = (patch: Partial<NonNullable<Routes["tune"]>>) => setRoutes((r) => ({ ...r, tune: { ...(r.tune ?? {}), ...patch } }));
  return (
    <>
      <Card title={t("settings.routesServers")} sub={t("settings.routesServersSub")}>
        <Field label={t("settings.routesAutoKind")} hint={t(servers.auto === "fallback" ? "settings.routesAutoFallbackHint" : "settings.routesAutoFastestHint")}>
          <Segmented
            label={t("settings.routesAutoKind")}
            value={servers.auto === "fallback" ? "fallback" : "fastest"}
            onChange={(v) => setServers({ auto: v === "fallback" ? "fallback" : undefined })}
            options={[
              { value: "fastest", label: t("settings.routesAutoFastest") },
              { value: "fallback", label: t("settings.routesAutoFallback") },
            ]}
          />
        </Field>
        <Field label={t("settings.routesInterval")} htmlFor="s-interval" hint={t("settings.routesIntervalHint")}>
          <Select id="s-interval" className="input max-w-[280px]" value={servers.interval ?? 0} onChange={(e) => setServers({ interval: Number(e.target.value) || undefined })}>
            {[...new Set([0, 60, 120, 600, 1800, servers.interval ?? 0])].map((n) => (
              <option key={n} value={n}>
                {n ? t("settings.routesIntervalSec", { n }) : t("settings.routesIntervalDefault")}
              </option>
            ))}
          </Select>
        </Field>
        <SwitchRow label={t("settings.routesCountries")} sub={t("settings.routesCountriesSub")} checked={!servers.no_countries} onChange={(v) => setServers({ no_countries: v ? undefined : true })} />
        {error && !error.ofCore ? (
          <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
            {error.text}
          </p>
        ) : null}
      </Card>
      <CoreBalance servers={servers} setServers={setServers} error={error?.ofCore ? error.text : undefined} />
      <Card title={t("settings.routesTune")} sub={t("common.forExperts")} i={3}>
        <SwitchRow label={t("settings.routesQuic")} sub={t("settings.routesQuicSub")} checked={!!tune.block_quic} onChange={(v) => setTune({ block_quic: v || undefined })} />
        <SwitchRow label={t("settings.routesSniffer")} sub={t("settings.routesSnifferSub")} checked={!!tune.sniffer} onChange={(v) => setTune({ sniffer: v || undefined })} />
        <SwitchRow label={t("settings.routesRealIP")} sub={t("settings.routesRealIPSub")} checked={!!tune.real_ip} onChange={(v) => setTune({ real_ip: v || undefined })} />
      </Card>
    </>
  );
}
