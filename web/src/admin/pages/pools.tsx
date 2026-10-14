// Traffic pools (GitHub issue #6): chosen protocols count to a pool with its own limit,
// apart from the main traffic. Pools are made here; protocols join one in their own
// settings, and tariffs and users give each pool its limit.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../api/client";
import { invalidatePoolRelated, usePools, useTariffs } from "../../api/hooks";
import { Confirm, Drawer, DrawerFooter } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Switch } from "../../components/switch";
import { Button, EmptyState, Field, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { bytes } from "../../lib/format";

type Pool = Schemas["PoolView"];
type Tariff = Schemas["TariffView"];

/** A pool's limit in each tariff that names one: "Standard 100 GB". */
function tariffLimits(pool: Pool, tariffs: Tariff[] | undefined): string[] {
  return (tariffs ?? []).flatMap((tr) => {
    const l = tr.pools.find((p) => p.pool_id === pool.id);
    return l?.traffic_limit != null ? [`${tr.name} ${bytes(l.traffic_limit)}`] : [];
  });
}

/** The pools; a new one is started from the page's header (`creating`) or the empty list. */
export function PoolsCard({ creating, onCreateClose }: { creating: boolean; onCreateClose: () => void }) {
  const pools = usePools();
  const tariffs = useTariffs();
  const qc = useQueryClient();
  const toast = useToast();
  const [edit, setEdit] = useState<Pool | "new" | null>(null);
  const [removing, setRemoving] = useState<Pool | null>(null);
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/pools/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      setRemoving(null);
      invalidatePoolRelated(qc);
      toast.ok(t("pools.deleted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });

  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("pools.title")}</h2>
          <div className="card-sub">{t("pools.sub")}</div>
        </div>
      </div>
      <QueryBoundary
        query={pools}
        pending={
          <div className="flex flex-col gap-2">
            {[0, 1].map((i) => (
              <Skeleton key={i} style={{ height: 56 }} />
            ))}
          </div>
        }
      >
        {(list) =>
          list.length === 0 ? (
            <EmptyState title={t("pools.emptyTitle")} text={t("pools.none")}>
              <Button variant="primary" onClick={() => setEdit("new")}>
                <Plus size={16} aria-hidden /> {t("pools.add")}
              </Button>
            </EmptyState>
          ) : (
            <ul className="row-list">
              {list.map((p) => {
                const limits = tariffLimits(p, tariffs.data);
                return (
                  <li key={p.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-3 py-3">
                    <div className="min-w-0">
                      <div className="text-[13px] font-semibold">{p.name}</div>
                      <dl className="mt-1 grid gap-1 text-xs">
                        <div className="flex flex-wrap gap-x-2">
                          <dt className="text-[var(--ink-500)]">{t("pools.protocols")}</dt>
                          <dd className="min-w-0 text-[var(--ink-700)]">
                            {p.inbounds.length ? (
                              p.inbounds.join(", ")
                            ) : (
                              <Link to="/inbounds" className="link-btn">
                                {t("pools.empty")}
                              </Link>
                            )}
                          </dd>
                        </div>
                        <div className="flex flex-wrap gap-x-2">
                          <dt className="text-[var(--ink-500)]">{t("pools.limits")}</dt>
                          <dd className="min-w-0 text-[var(--ink-700)]">{limits.length ? limits.join(" · ") : t("pools.noLimits")}</dd>
                        </div>
                      </dl>
                    </div>
                    <div className="flex gap-1">
                      <button type="button" className="icon-btn" aria-label={t("pools.rename", { name: p.name })} onClick={() => setEdit(p)}>
                        <Pencil size={16} />
                      </button>
                      <button type="button" className="icon-btn" aria-label={t("pools.delete", { name: p.name })} onClick={() => setRemoving(p)}>
                        <Trash2 size={16} />
                      </button>
                    </div>
                  </li>
                );
              })}
            </ul>
          )
        }
      </QueryBoundary>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("pools.hint")}</p>
      <PoolDrawer
        pool={creating ? "new" : edit}
        onClose={() => {
          setEdit(null);
          onCreateClose();
        }}
      />
      <Confirm
        open={!!removing}
        onOpenChange={(v) => !v && setRemoving(null)}
        title={t("pools.deleteTitle", { name: removing?.name ?? "" })}
        text={t("pools.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </section>
  );
}

/** A new pool, or a new name for one. */
function PoolDrawer({ pool, onClose }: { pool: Pool | "new" | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const isNew = pool === "new";
  const [name, setName] = useState("");
  useEffect(() => {
    if (pool) setName(pool === "new" ? "" : pool.name);
  }, [pool]);
  const save = useMutation({
    mutationFn: () =>
      pool === "new" || !pool
        ? unwrap(api.POST("/api/v1/pools", { body: { name: name.trim() } }))
        : unwrap(api.PATCH("/api/v1/pools/{id}", { params: { path: { id: pool.id } }, body: { name: name.trim() } })),
    onSuccess: () => {
      invalidatePoolRelated(qc);
      toast.ok(isNew ? t("pools.created") : t("pools.renamed"));
      onClose();
    },
  });
  const error = save.error instanceof ApiError ? (save.error.fields.name ?? errorText(save.error)) : save.error ? errorText(save.error) : "";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (name.trim()) save.mutate();
  };
  return (
    <Drawer
      open={!!pool}
      onOpenChange={(v) => {
        if (!v) {
          save.reset();
          onClose();
        }
      }}
      title={isNew ? t("pools.new") : t("pools.edit")}
      meta={t("pools.drawerMeta")}
      footer={
        <DrawerFooter form="pool-form" onCancel={onClose} loading={save.isPending} disabled={!name.trim()}>
          {isNew ? t("pools.add") : t("common.save")}
        </DrawerFooter>
      }
    >
      <form id="pool-form" onSubmit={submit} noValidate>
        <Field label={t("pools.name")} htmlFor="pool-name" error={error} hint={t("pools.nameHint")}>
          <input id="pool-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={40} placeholder={t("pools.placeholder")} aria-invalid={!!error} autoFocus />
        </Field>
      </form>
    </Drawer>
  );
}

/** Pool limits of a tariff or a user: GB per pool, empty = unlimited. With closed/onClosed
 * each pool has an access switch: a closed pool is left out of the subscription and the
 * nodes turn the user away from it, so it has no limit. */
export function PoolLimitsField({
  pools,
  value,
  onChange,
  closed,
  onClosed,
}: {
  pools: Pool[];
  value: Record<number, string>;
  onChange: (v: Record<number, string>) => void;
  closed?: Record<number, boolean>;
  onClosed?: (v: Record<number, boolean>) => void;
}) {
  if (pools.length === 0) return null;
  return (
    <ul className="row-list rounded-2xl border border-[var(--hairline)] px-3">
      {pools.map((p) => {
        const shut = !!closed?.[p.id];
        return (
          <li key={p.id} className={`grid items-center gap-3 py-2 ${onClosed ? "grid-cols-[minmax(0,1fr)_auto_132px]" : "grid-cols-[minmax(0,1fr)_132px]"}`}>
            <div className="min-w-0">
              <div className="truncate text-[13px] font-medium">{p.name}</div>
              {p.inbounds.length ? <div className="truncate text-xs text-[var(--ink-500)]">{p.inbounds.join(", ")}</div> : null}
            </div>
            {onClosed ? <Switch checked={!shut} label={t("pools.accessOf", { name: p.name })} onChange={(on) => onClosed({ ...closed, [p.id]: !on })} /> : null}
            {shut ? (
              <span className="text-xs text-[var(--ink-500)]">{t("pools.closed")}</span>
            ) : (
              <label className="input-unit">
                <input
                  className="input num"
                  inputMode="decimal"
                  value={value[p.id] ?? ""}
                  onChange={(e) => onChange({ ...value, [p.id]: e.target.value })}
                  placeholder="∞"
                  aria-label={t("pools.limitOf", { name: p.name })}
                />
                <span aria-hidden>{t("units.gb")}</span>
              </label>
            )}
          </li>
        );
      })}
    </ul>
  );
}
