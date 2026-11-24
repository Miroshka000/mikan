import { errorText } from "../../../../api/client";
import { Card } from "../../../../components/layout";
import { Select } from "../../../../components/select";
import { Button, ErrorState, Skeleton } from "../../../../components/ui";
import { t, useLocale } from "../../../../i18n";
import { useCatalog } from "./catalog";
import { MAX_LISTS, type List, type Routes, type SetRoutes } from "./model";
import { TargetSelect } from "./targets";

/** The own rule lists by link, and the apps that go past the tunnel. error is the refusal of the lists. */
export function ListsPart({ routes, setRoutes, error }: { routes: Routes; setRoutes: SetRoutes; error?: string }) {
  const catalog = useCatalog();
  const locale = useLocale();
  const setList = (i: number, patch: Partial<List>) => setRoutes((r) => ({ ...r, lists: (r.lists ?? []).map((l, j) => (j === i ? { ...l, ...patch } : l)) }));
  const addList = () => setRoutes((r) => ({ ...r, lists: [...(r.lists ?? []), { name: `list-${(r.lists?.length ?? 0) + 1}`, url: "", behavior: "classical", format: "yaml", target: "vpn" }] }));
  const dropList = (i: number) => setRoutes((r) => ({ ...r, lists: (r.lists ?? []).filter((_, j) => j !== i) }));
  const toggleDirect = (id: string) =>
    setRoutes((r) => {
      const has = (r.direct ?? []).includes(id);
      return { ...r, direct: has ? (r.direct ?? []).filter((x) => x !== id) : [...(r.direct ?? []), id] };
    });
  const lists = routes.lists ?? [];
  return (
    <>
      <Card title={t("settings.routesLists")} sub={t("settings.routesListsHint")}>
        <div className="flex flex-col gap-3">
          {lists.map((l, i) => (
            <div key={i} className="panel-soft grid gap-2 p-3 sm:grid-cols-[160px_minmax(0,1fr)]">
              <input className="input mono" value={l.name} onChange={(e) => setList(i, { name: e.target.value.toLowerCase() })} maxLength={32} aria-label={t("settings.routesListName")} placeholder="whitelist" />
              <input className="input mono" value={l.url} onChange={(e) => setList(i, { url: e.target.value.trim() })} maxLength={500} inputMode="url" autoComplete="off" spellCheck={false} aria-label={t("settings.routesListUrl")} placeholder="https://raw.githubusercontent.com/…/list.yaml" />
              <div className="flex flex-wrap items-center gap-2 sm:col-span-2">
                <Select className="input max-w-[170px]" value={l.behavior} onChange={(e) => setList(i, { behavior: e.target.value })} aria-label={t("settings.routesListBehavior")}>
                  <option value="classical">{t("settings.routesListClassical")}</option>
                  <option value="domain">{t("settings.routesListDomain")}</option>
                  <option value="ipcidr">{t("settings.routesListIP")}</option>
                </Select>
                <Select className="input max-w-[110px]" value={l.format} onChange={(e) => setList(i, { format: e.target.value })} aria-label={t("settings.routesListFormat")}>
                  <option value="yaml">YAML</option>
                  <option value="text">TXT</option>
                  <option value="mrs">MRS</option>
                </Select>
                <TargetSelect id={`s-list-${i}`} value={l.target} onChange={(v) => setList(i, { target: v })} follow={false} label={t("settings.routesListTarget")} />
                <Button variant="ghost" className="ml-auto" onClick={() => dropList(i)} aria-label={t("settings.routesListRemoveNamed", { name: l.name })}>
                  {t("settings.routesListRemove")}
                </Button>
              </div>
            </div>
          ))}
          {!lists.length ? <p className="text-[13px] text-[var(--ink-500)]">{t("settings.routesListsEmpty")}</p> : null}
          <div>
            <Button variant="ghost" onClick={addList} disabled={lists.length >= MAX_LISTS}>
              + {t("settings.routesListAdd")}
            </Button>
          </div>
          {error ? (
            <p className="text-xs text-[var(--berry-600)]" role="alert">
              {error}
            </p>
          ) : null}
        </div>
      </Card>
      <Card title={t("settings.routesApps")} sub={t("settings.routesAppsHint")} i={2}>
        {catalog.isError ? <ErrorState text={errorText(catalog.error)} onRetry={() => void catalog.refetch()} /> : null}
        {catalog.isPending ? <Skeleton className="h-40 w-full rounded-2xl" /> : null}
        <div className="grid gap-2 sm:grid-cols-2">
          {(catalog.data?.direct ?? []).map((d) => {
            const on = (routes.direct ?? []).includes(d.id);
            return (
              <label key={d.id} className="panel-soft flex cursor-pointer items-start gap-3 p-3 text-sm" style={on ? { borderColor: "var(--mikan-400)" } : undefined}>
                <input type="checkbox" className="check mt-0.5 shrink-0" checked={on} onChange={() => toggleDirect(d.id)} />
                <span>{locale === "en" ? d.name_en : d.name}</span>
              </label>
            );
          })}
        </div>
      </Card>
    </>
  );
}
