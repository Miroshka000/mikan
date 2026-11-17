import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ListChecks, Search, ShieldCheck, SlidersHorizontal, TriangleAlert, XCircle } from "lucide-react";
import { useState } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk } from "../../../api/hooks";
import { CardHead } from "../../../components/layout";
import { Drawer } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, ErrorState, Pill, Skeleton } from "../../../components/ui";
import { t, tMaybe } from "../../../i18n";

type Change = Schemas["PostureChange"];
type Preset = "standard" | "secure";

const PRESETS = [
  { id: "standard", icon: SlidersHorizontal },
  { id: "secure", icon: ShieldCheck },
] as const;

const presetKey = ["security-presets"] as const;

/**
 * The two ways the whole installation is set up. Nothing is written by a press on a tile:
 * the panel first shows what the preset changes, node by node, and the admin applies it.
 */
export function SecurityPresetsCard() {
  const state = useQuery({ queryKey: presetKey, queryFn: () => unwrap(api.GET("/api/v1/security/presets")) });
  const [open, setOpen] = useState<Preset | null>(null);
  const current = state.data?.current;
  return (
    <section className="card glass reveal" style={{ "--i": 0 } as React.CSSProperties}>
      <CardHead
        title={t("settings.presets.title")}
        sub={t("settings.presets.sub")}
        aside={current ? <Pill tone={current === "secure" ? "ok" : current === "custom" ? "warn" : "off"}>{t(`settings.presets.now.${current}`)}</Pill> : null}
      />
      {state.isPending ? (
        <div className="grid gap-3 sm:grid-cols-2" role="status" aria-busy aria-label={t("common.loading")}>
          <Skeleton style={{ height: 172 }} />
          <Skeleton style={{ height: 172 }} />
        </div>
      ) : state.isError ? (
        <ErrorState text={errorText(state.error)} onRetry={() => void state.refetch()} />
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          {PRESETS.map(({ id, icon: Icon }) => {
            const active = current === id;
            const left = id === "secure" ? state.data.secure : state.data.standard;
            return (
              <article key={id} className="preset-tile" data-active={active || undefined}>
                <header className="flex items-center gap-3">
                  <span className="preset-icon" aria-hidden>
                    <Icon size={20} />
                  </span>
                  <h3 className="text-[15px] font-semibold">{t(`settings.presets.${id}.name`)}</h3>
                  {active ? (
                    <span className="ml-auto inline-flex items-center gap-1 text-xs font-medium text-[var(--leaf-700)]">
                      <Check size={16} aria-hidden /> {t("settings.presets.active")}
                    </span>
                  ) : null}
                </header>
                <p className="mt-3 text-[13px] leading-5 text-[var(--ink-600)]">{t(`settings.presets.${id}.text`)}</p>
                <div className="mt-4 flex flex-wrap items-center gap-3">
                  <Button size="sm" variant={id === "secure" && !active ? "primary" : "glass"} disabled={active && left === 0} onClick={() => setOpen(id)}>
                    <ListChecks size={16} aria-hidden /> {t("settings.presets.show")}
                  </Button>
                  {!active && left > 0 ? <span className="text-xs text-[var(--ink-500)]">{t("settings.presets.left", { n: left })}</span> : null}
                </div>
              </article>
            );
          })}
        </div>
      )}
      {open ? <PlanDrawer preset={open} onClose={() => setOpen(null)} /> : null}
    </section>
  );
}

/** The preset's changes, read before they are written, and what became of each after. */
function PlanDrawer({ preset, onClose }: { preset: Preset; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  // The panel's own names first and only; sites next to the server are the admin's call.
  const [neighbors, setNeighbors] = useState(false);
  const plan = useQuery({
    queryKey: [...presetKey, "plan", preset, neighbors],
    queryFn: () => unwrap(api.POST("/api/v1/security/presets/{preset}/plan", { params: { path: { preset }, query: { neighbors } } })),
    gcTime: 0,
    staleTime: Infinity,
    retry: false,
  });
  const apply = useMutation({
    mutationFn: (changes: Change[]) => unwrap(api.POST("/api/v1/security/presets/{preset}/apply", { params: { path: { preset } }, body: { changes } })),
    onSuccess: (r) => {
      const failed = r.changes.filter((c) => c.error).length;
      if (failed) toast.error(t("settings.presets.partly", { n: failed }));
      else toast.ok(t("settings.presets.applied"));
      for (const key of [presetKey, qk.settings, qk.nodes, ["inbounds"]]) void qc.invalidateQueries({ queryKey: key });
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const done = apply.data?.changes;
  const changes = done ?? plan.data?.changes ?? [];
  // What the preset does by itself, and what it only offers: an offer is applied when its box is ticked.
  const todo = changes.filter((c) => !c.manual && !c.optional);
  const offers = changes.filter((c) => c.optional);
  const [picked, setPicked] = useState<number[]>([]);
  const toApply = [...todo, ...offers.filter((c) => c.inbound_id !== undefined && picked.includes(c.inbound_id))];
  const manual = changes.filter((c) => c.manual);
  const moves = todo.some((c) => c.code === "through_443" || c.code === "direct" || c.code === "site");
  const groups = groupByNode(todo);
  return (
    <Drawer
      open
      onOpenChange={(v) => !v && onClose()}
      title={t(`settings.presets.${preset}.name`)}
      meta={plan.isSuccess ? t("settings.presets.count", { n: todo.length }) : undefined}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {done ? t("common.close") : t("common.cancel")}
          </Button>
          {done ? null : (
            <Button variant="primary" loading={apply.isPending} disabled={!plan.isSuccess || toApply.length === 0} onClick={() => apply.mutate(toApply)}>
              {t("settings.presets.apply")}
            </Button>
          )}
        </>
      }
    >
      {plan.isPending ? (
        <div className="flex flex-col gap-2" role="status" aria-busy aria-label={t("settings.presets.planning")}>
          <p className="text-[13px] text-[var(--ink-500)]">{t("settings.presets.planning")}</p>
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} style={{ height: 44 }} />
          ))}
        </div>
      ) : plan.isError ? (
        <ErrorState text={errorText(plan.error)} onRetry={() => void plan.refetch()} />
      ) : (
        <>
          {todo.length === 0 ? (
            <p className="banner info">{t("settings.presets.nothing")}</p>
          ) : !done && moves ? (
            <p className="banner warn">
              <TriangleAlert size={18} aria-hidden className="shrink-0" />
              <span>{t("settings.presets.warn")}</span>
            </p>
          ) : null}
          {groups.map((g) => (
            <section key={g.node} className="mt-4">
              <h3 className="text-[13px] font-semibold text-[var(--ink-700)]">{g.node === 0 ? t("settings.site.panel") : g.name || t("nodes.localName")}</h3>
              <ul className="row-list">
                {g.changes.map((c, i) => (
                  <li key={i} className="flex items-start gap-3 py-3">
                    {done ? (
                      c.error ? (
                        <XCircle size={18} aria-hidden className="mt-0.5 shrink-0 text-[var(--berry-600)]" />
                      ) : (
                        <Check size={18} aria-hidden className="mt-0.5 shrink-0 text-[var(--leaf-500)]" />
                      )
                    ) : null}
                    <div className="min-w-0">
                      <div className="text-[13px]">{changeText(c)}</div>
                      {c.error ? <div className="mt-1 text-xs text-[var(--berry-600)]">{tMaybe(`errors.${c.error}`) ?? c.error}</div> : null}
                    </div>
                  </li>
                ))}
              </ul>
            </section>
          ))}
          {offers.length > 0 ? (
            <section className="mt-6">
              <h3 className="text-[13px] font-semibold text-[var(--ink-700)]">{t("settings.presets.offers")}</h3>
              <p className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.presets.offersHint")}</p>
              <ul className="row-list">
                {offers.map((c, i) => (
                  <li key={i} className="py-3">
                    <label className="flex items-start gap-3 text-[13px]">
                      {done ? (
                        c.error ? (
                          <XCircle size={18} aria-hidden className="mt-0.5 shrink-0 text-[var(--berry-600)]" />
                        ) : (
                          <Check size={18} aria-hidden className="mt-0.5 shrink-0 text-[var(--leaf-500)]" />
                        )
                      ) : (
                        <input
                          type="checkbox"
                          className="mt-0.5 h-4 w-4 shrink-0"
                          checked={c.inbound_id !== undefined && picked.includes(c.inbound_id)}
                          onChange={(e) => setPicked((p) => (e.target.checked ? [...p, c.inbound_id!] : p.filter((id) => id !== c.inbound_id)))}
                        />
                      )}
                      <span className="min-w-0">
                        <span className="font-medium">{c.node_name || t("nodes.localName")}</span>
                        <span className="mt-1 block text-[var(--ink-600)]">{changeText(c)}</span>
                        {c.error ? <span className="mt-1 block text-xs text-[var(--berry-600)]">{tMaybe(`errors.api.${c.error}`) ?? c.error}</span> : null}
                      </span>
                    </label>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
          {!done && preset === "secure" && !neighbors && manual.some((c) => c.code === "no_site") ? (
            <div className="mt-6 flex flex-col items-start gap-2">
              <Button size="sm" onClick={() => setNeighbors(true)}>
                <Search size={16} aria-hidden /> {t("settings.presets.neighbors")}
              </Button>
              <p className="text-xs text-[var(--ink-500)]">{t("settings.presets.neighborsHint")}</p>
            </div>
          ) : null}
          {manual.length > 0 ? (
            <section className="mt-6">
              <h3 className="text-[13px] font-semibold text-[var(--ink-700)]">{t("settings.presets.manual")}</h3>
              <ul className="row-list">
                {manual.map((c, i) => (
                  <li key={i} className="py-3 text-[13px]">
                    <span className="font-medium">{c.node_name || t("nodes.localName")}</span>
                    {c.inbound ? <span className="text-[var(--ink-500)]"> · {c.inbound}</span> : null}
                    <div className="mt-1 text-[var(--ink-600)]">{changeText(c)}</div>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
        </>
      )}
    </Drawer>
  );
}

function groupByNode(changes: Change[]): { node: number; name: string; changes: Change[] }[] {
  const out: { node: number; name: string; changes: Change[] }[] = [];
  for (const c of changes) {
    let g = out.find((x) => x.node === c.node);
    if (!g) out.push((g = { node: c.node, name: c.node_name ?? "", changes: [] }));
    g.changes.push(c);
  }
  return out;
}

/** A change in words: the protocol's name first where it is about one. */
function changeText(c: Change): string {
  const p = { ...c.params, inbound: c.inbound ?? "" };
  if (c.code === "front_page" || c.code === "panel_page") {
    return t(`settings.presets.change.${c.code}`, { page: tMaybe(`settings.site.option.${c.params?.page ?? ""}`) ?? c.params?.page ?? "" });
  }
  if (c.code === "site" && !c.params?.from) return t("settings.presets.change.site_plain", p);
  return t(`settings.presets.change.${c.code}`, p);
}
