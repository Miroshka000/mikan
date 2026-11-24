import { Search } from "lucide-react";
import { useState } from "react";
import { errorText } from "../../../../api/client";
import { Card } from "../../../../components/layout";
import { EmptyState, ErrorState, Pill, Skeleton } from "../../../../components/ui";
import { t, useLocale } from "../../../../i18n";
import { ServiceBoard } from "./board";
import { matching, moveService } from "./board-model";
import { useCatalog } from "./catalog";
import { MODES, type Mode, type Routes, type SetRoutes } from "./model";
import { useTargets } from "./targets";

/** The mode and where each service goes: the first part of a routing, the settings' or a simple template's. */
export function ModePart({ mode, setMode, routes, setRoutes }: { mode: Mode; setMode: (m: Mode) => void; routes: Routes; setRoutes: SetRoutes }) {
  return (
    <>
      <ModeCard mode={mode} setMode={setMode} />
      <ServicesCard routes={routes} setRoutes={setRoutes} />
    </>
  );
}

/** The mode: how the apps split traffic between the tunnel and the direct path. */
function ModeCard({ mode, setMode }: { mode: Mode; setMode: (m: Mode) => void }) {
  return (
    <Card title={t("settings.routing")} sub={t("settings.routingHint")}>
      <div className="grid gap-2 sm:grid-cols-3" role="radiogroup" aria-label={t("settings.routing")}>
        {MODES.map((m) => (
          <button key={m.id} type="button" role="radio" aria-checked={mode === m.id} className="opt" onClick={() => setMode(m.id)}>
            <span className="font-semibold">{t(m.title)}</span>
            <span className="text-xs text-[var(--ink-500)]">{t(m.sub)}</span>
          </button>
        ))}
      </div>
    </Card>
  );
}

/** Where each service goes, as a board of the places; the ones set apart are counted. */
function ServicesCard({ routes, setRoutes }: { routes: Routes; setRoutes: SetRoutes }) {
  const catalog = useCatalog();
  const locale = useLocale();
  const { targets, loading } = useTargets();
  const [q, setQ] = useState("");
  const services = (catalog.data?.services ?? []).map((svc) => ({ id: svc.id, icon: svc.icon, name: locale === "en" ? svc.name_en : svc.name }));
  const shown = matching(services, q, (s) => s.name);
  const set = Object.keys(routes.services ?? {}).length;
  return (
    <Card title={t("settings.routesServices")} sub={t("settings.routesServicesHint")} i={2} aside={set ? <Pill tone="off">{t("settings.routesChanged", { n: set })}</Pill> : null}>
      <div className="mb-3 flex items-center gap-2">
        <div className="relative w-full max-w-[320px]">
          <Search size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-[var(--ink-400)]" aria-hidden />
          <input className="input w-full pl-9" type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("settings.routesSearch")} aria-label={t("settings.routesSearch")} />
        </div>
      </div>
      {catalog.isError ? <ErrorState text={errorText(catalog.error)} onRetry={() => void catalog.refetch()} /> : null}
      {catalog.isPending || loading ? (
        <div className="board" aria-busy="true">
          {[0, 1, 2].map((n) => (
            <Skeleton key={n} className="h-[120px] w-full rounded-2xl" />
          ))}
        </div>
      ) : null}
      {catalog.data && !loading && !shown.length ? <EmptyState icon={Search} search title={t("settings.routesSearchEmpty")} text={t("settings.routesSearchEmptyText")} /> : null}
      {catalog.data && !loading && shown.length ? (
        <ServiceBoard services={shown} sent={routes.services} targets={targets} onMove={(id, to) => setRoutes((r) => ({ ...r, services: moveService(r.services, id, to) }))} />
      ) : null}
    </Card>
  );
}
