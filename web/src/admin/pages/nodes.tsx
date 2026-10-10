import { useMutation, useQueryClient } from "@tanstack/react-query";
import * as Menu from "@radix-ui/react-dropdown-menu";
import { Activity, ArrowDown, ArrowUp, ArrowUpCircle, ArrowUpDown, Check, Cloud, Copy, Gauge, KeyRound, LoaderCircle, MoreHorizontal, Pencil, Plus, ShieldCheck, Stethoscope, Trash2, Waypoints } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useNodes } from "../../api/hooks";
import { CertDrawer, certUntil } from "../../components/cert-drawer";
import { Confirm, Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Bar, Button, EmptyState, Field, PageHeader, Pill, Skeleton } from "../../components/ui";
import { Switch } from "../../components/switch";
import { t, tMaybe } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { bytes, num } from "../../lib/format";
import { nodeLabel } from "../../lib/node-label";
import { CascadeDrawer } from "./node-cascade";
import { HelloLine, JoinProgress, NodeCheckDrawer, NodeProblem, SkewLine, type FixActions } from "./node-check";
import { SpeedDrawer } from "./node-speed";
import { NodeTrafficDrawer } from "./node-traffic";
import { WarpDrawer } from "./node-warp";

type Node = Schemas["NodeInfo"];
type Joined = { id: number; name: string; key: string; command: string };

/** What a server before 0.5.0.2 runs once by hand: from then on the panel updates it. */
const OLD_NODE_COMMAND = "mikan update";

/** The panel can update this node itself and it is behind. */
const updatable = (n: Node) => !n.local && n.behind && n.can_update;
const updating = (n: Node) => n.update?.state === "running";

export function NodesPage() {
  const nodes = useNodes();
  const toUpdate = (nodes.data ?? []).filter(updatable);
  const qc = useQueryClient();
  const toast = useToast();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Node | null>(null);
  const [rekeying, setRekeying] = useState<Node | null>(null);
  const [removing, setRemoving] = useState<Node | null>(null);
  const [joined, setJoined] = useState<Joined | null>(null);
  const [warpOf, setWarpOf] = useState<Node | null>(null);
  const [speedOf, setSpeedOf] = useState<Node | null>(null);
  const [trafficOf, setTrafficOf] = useState<Node | null>(null);
  const [cascadeOf, setCascadeOf] = useState<Node | null>(null);
  const [certOf, setCertOf] = useState<Node | null>(null);
  const [checkOf, setCheckOf] = useState<Node | null>(null);
  // The arrows that order the nodes show only while ordering: most of the time they are noise.
  const [ordering, setOrdering] = useState(false);
  const many = (nodes.data?.length ?? 0) > 1;
  const [updateOf, setUpdateOf] = useState<Node | "all" | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.nodes });
    void qc.invalidateQueries({ queryKey: qk.inbounds });
  };
  const rekey = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/nodes/{id}/key", { params: { path: { id } } })),
    onSuccess: (r) => {
      setRekeying(null);
      setJoined({ id: r.node.id, name: nodeLabel(r.node), key: r.key, command: r.command });
    },
    onSettled: refresh,
    onError: (e) => toast.error(errorText(e)),
  });
  // The node's server does the update (a backup, the signed release, a restart): this only asks.
  const update = useMutation({
    mutationFn: async (id: number | "all") => {
      if (id === "all") await unwrap(api.POST("/api/v1/nodes/update-all"));
      else await unwrap(api.POST("/api/v1/nodes/{id}/update", { params: { path: { id } } }));
    },
    onSuccess: () => {
      toast.ok(t("nodes.updateAsked"));
      setUpdateOf(null);
    },
    onSettled: refresh,
    onError: (e) => {
      toast.error(errorText(e));
      setUpdateOf(null);
    },
  });
  // The order of the servers in the subscriptions. The page shows the new order at once;
  // when the panel refuses, the old one comes back.
  const order = useMutation({
    mutationFn: ({ ids }: { ids: number[]; moved: number }) => unwrap(api.PUT("/api/v1/nodes/order", { body: { ids } })),
    onMutate: async ({ ids }) => {
      await qc.cancelQueries({ queryKey: qk.nodes });
      const prev = qc.getQueryData<Node[]>(qk.nodes);
      if (prev) {
        const byId = new Map(prev.map((n) => [n.id, n]));
        qc.setQueryData<Node[]>(
          qk.nodes,
          ids.flatMap((id) => byId.get(id) ?? []),
        );
      }
      return { prev };
    },
    onError: (e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(qk.nodes, ctx.prev);
      toast.error(errorText(e));
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: qk.nodes }),
  });
  const move = (list: Node[], idx: number, by: -1 | 1) => {
    const ids = list.map((n) => n.id);
    const to = idx + by;
    if (order.isPending || to < 0 || to >= ids.length) return;
    [ids[idx], ids[to]] = [ids[to]!, ids[idx]!];
    order.mutate({ ids, moved: list[idx]!.id });
  };
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/nodes/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      toast.ok(t("nodes.deleted"));
      setRemoving(null);
    },
    onSettled: refresh,
    // A node still in use names what goes through it: all of it, not just the first.
    onError: (e) =>
      toast.error(e instanceof ApiError && e.status === 409 && e.detail === "node_in_use" ? `${t("errors.api.node_in_use")} ${e.messages.join("; ")}` : errorText(e)),
  });

  // The fixes the check and the joining steps offer as buttons.
  const fixesFor = (n: Node | undefined, close: () => void): FixActions =>
    n
      ? {
          rekey: n.local
            ? undefined
            : () => {
                close();
                setRekeying(n);
              },
          update: updatable(n)
            ? () => {
                close();
                setUpdateOf(n);
              }
            : undefined,
          edit: () => {
            close();
            setEditing(n);
          },
        }
      : {};
  const joinedNode = joined ? nodes.data?.find((x) => x.id === joined.id) : undefined;

  return (
    <>
      <PageHeader
        title={t("nav.nodes")}
        sub={t("nodes.subtitle")}
        actions={
          <>
            {many ? (
              <Button aria-pressed={ordering} onClick={() => setOrdering((o) => !o)} aria-label={ordering ? t("nodes.orderDone") : t("nodes.orderEdit")}>
                {ordering ? <Check size={18} aria-hidden /> : <ArrowUpDown size={18} aria-hidden />}
                <span className="max-[760px]:hidden">{ordering ? t("nodes.orderDone") : t("nodes.orderEdit")}</span>
              </Button>
            ) : null}
            {toUpdate.length > 1 ? (
              <Button loading={update.isPending && update.variables === "all"} disabled={update.isPending || toUpdate.some(updating)} onClick={() => setUpdateOf("all")}>
                <ArrowUpCircle size={18} aria-hidden />
                <span className="max-[760px]:hidden">{t("nodes.updateAll", { n: toUpdate.length })}</span>
              </Button>
            ) : null}
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus size={18} aria-hidden />
              <span className="max-[760px]:hidden">{t("nodes.add")}</span>
            </Button>
          </>
        }
      />
      <QueryBoundary
        query={nodes}
        pending={
          <div className="grid gap-4 lg:grid-cols-2">
            {[0, 1].map((i) => (
              <Skeleton key={i} style={{ height: 220, borderRadius: 20 }} />
            ))}
          </div>
        }
        wrap={(state) => <section className="card glass">{state}</section>}
      >
        {(list) =>
          list.length === 0 ? (
            <section className="card glass">
              <EmptyState title={t("nodes.emptyTitle")} text={t("nodes.emptyText")} />
            </section>
          ) : (
            <div className="grid gap-4 lg:grid-cols-2">
              {list.length > 1 && list.some((n) => n.local && !n.name) ? (
                <div className="banner warn lg:col-span-2" role="status">
                  <Pencil size={18} className="shrink-0" aria-hidden />
                  <span>{t("nodes.nameLocalHint")}</span>
                </div>
              ) : null}
              {ordering && list.length > 1 ? (
                <p className="banner info lg:col-span-2" role="status">
                  {t("nodes.orderHint")}
                </p>
              ) : null}
              {list.map((n, idx) => (
                <NodeCard key={n.id} n={n} idx={idx} total={list.length} ordering={ordering} sorting={order.isPending} moving={order.isPending && order.variables.moved === n.id} onMove={(by) => move(list, idx, by)} updating={update.isPending && update.variables === n.id} busy={update.isPending} onUpdate={() => setUpdateOf(n)} onEdit={() => setEditing(n)} onWarp={() => setWarpOf(n)} onSpeed={() => setSpeedOf(n)} onTraffic={() => setTrafficOf(n)} onCascade={() => setCascadeOf(n)} onCert={() => setCertOf(n)} onCheck={() => setCheckOf(n)} onRekey={() => setRekeying(n)} onRemove={() => setRemoving(n)} />
              ))}
            </div>
          )
        }
      </QueryBoundary>
      <AddNodeDrawer
        open={adding}
        onOpenChange={setAdding}
        onJoined={(j) => {
          setAdding(false);
          setJoined(j);
        }}
      />
      <EditNodeDrawer node={editing} onClose={() => setEditing(null)} />
      <KeyDrawer joined={joined} onClose={() => setJoined(null)} actions={fixesFor(joinedNode, () => setJoined(null))} />
      <NodeCheckDrawer node={checkOf} onClose={() => setCheckOf(null)} actions={fixesFor(checkOf ?? undefined, () => setCheckOf(null))} />
      <WarpDrawer node={warpOf ? { id: warpOf.id, name: nodeLabel(warpOf) } : null} onClose={() => setWarpOf(null)} />
      <SpeedDrawer node={speedOf ? { id: speedOf.id, name: nodeLabel(speedOf) } : null} onClose={() => setSpeedOf(null)} />
      <NodeTrafficDrawer node={trafficOf ? { id: trafficOf.id, name: nodeLabel(trafficOf) } : null} onClose={() => setTrafficOf(null)} />
      <CascadeDrawer node={cascadeOf ? { id: cascadeOf.id, name: nodeLabel(cascadeOf) } : null} onClose={() => setCascadeOf(null)} />
      <CertDrawer
        open={!!certOf}
        onClose={() => setCertOf(null)}
        title={t("cert.nodeTitle")}
        meta={certOf ? nodeLabel(certOf) : undefined}
        lead={t("cert.nodeLead")}
        current={certOf?.certificate ?? null}
        save={(cert, key) => unwrap(api.PUT("/api/v1/nodes/{id}/certificate", { params: { path: { id: certOf!.id } }, body: { cert, key } })).then(() => qc.invalidateQueries({ queryKey: qk.nodes }))}
        clear={() => unwrap(api.DELETE("/api/v1/nodes/{id}/certificate", { params: { path: { id: certOf!.id } } })).then(() => qc.invalidateQueries({ queryKey: qk.nodes }))}
        clearLabel={t("cert.nodeClear")}
        clearText={t("cert.nodeClearText")}
      />
      <Confirm
        open={!!updateOf && !update.isPending}
        onOpenChange={(v) => !v && setUpdateOf(null)}
        title={updateOf === "all" ? t("nodes.updateAllTitle", { n: toUpdate.length }) : t("nodes.updateTitle", { name: updateOf ? nodeLabel(updateOf) : "" })}
        text={updateOf === "all" ? t("nodes.updateAllText") : t("nodes.updateText")}
        confirm={t("nodes.update")}
        loading={update.isPending}
        onConfirm={() => updateOf && update.mutate(updateOf === "all" ? "all" : updateOf.id)}
      />
      <Confirm
        open={!!rekeying}
        onOpenChange={(v) => !v && setRekeying(null)}
        title={t("nodes.rekeyTitle", { name: rekeying ? nodeLabel(rekeying) : "" })}
        text={t("nodes.rekeyText")}
        confirm={t("nodes.rekeyConfirm")}
        loading={rekey.isPending}
        onConfirm={() => rekeying && rekey.mutate(rekeying.id)}
      />
      <Confirm
        open={!!removing}
        onOpenChange={(v) => !v && setRemoving(null)}
        title={t("nodes.deleteTitle", { name: removing ? nodeLabel(removing) : "" })}
        text={t("nodes.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </>
  );
}

function NodeCard({
  n,
  idx,
  total,
  ordering,
  sorting,
  moving,
  onMove,
  updating: asking,
  busy,
  onUpdate,
  onEdit,
  onWarp,
  onSpeed,
  onTraffic,
  onCascade,
  onCert,
  onCheck,
  onRekey,
  onRemove,
}: {
  n: Node;
  idx: number;
  /** How many nodes there are: with one there is nothing to order. */
  total: number;
  /** The admin is ordering the nodes: the arrows show. */
  ordering: boolean;
  /** The new order is on its way to the panel: no second move now. */
  sorting: boolean;
  /** This node is the one being moved. */
  moving: boolean;
  onMove: (by: -1 | 1) => void;
  /** The request for this node is on its way to the panel. */
  updating: boolean;
  /** Some node's request is: no second one now. */
  busy: boolean;
  onUpdate: () => void;
  onEdit: () => void;
  onWarp: () => void;
  onSpeed: () => void;
  onTraffic: () => void;
  onCascade: () => void;
  onCert: () => void;
  onCheck: () => void;
  onRekey: () => void;
  onRemove: () => void;
}) {
  const mem = n.mem_total ? Math.round((n.mem_used / n.mem_total) * 100) : 0;
  return (
    <section className="card glass reveal flex flex-col" style={{ "--i": idx } as React.CSSProperties}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="font-display truncate text-lg font-medium tracking-tight">{nodeLabel(n)}</h2>
          <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--ink-500)]">
            <span className="mono">{n.host || "—"}</span>
            <span>·</span>
            <span>{n.local ? t("nodes.kindLocal") : t("nodes.kindRemote")}</span>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <NodeStatus n={n} />
          {total > 1 && ordering ? (
            <>
              <span className="num w-6 text-center text-xs text-[var(--ink-500)]" role="img" aria-label={t("nodes.position", { n: idx + 1, total })}>
                {moving ? <LoaderCircle size={14} className="spin inline" aria-hidden /> : idx + 1}
              </span>
              <button type="button" className="icon-btn" disabled={sorting || idx === 0} aria-busy={moving || undefined} aria-label={t("nodes.moveUp", { name: nodeLabel(n) })} title={t("nodes.moveUp", { name: nodeLabel(n) })} onClick={() => onMove(-1)}>
                <ArrowUp size={18} aria-hidden />
              </button>
              <button type="button" className="icon-btn" disabled={sorting || idx === total - 1} aria-busy={moving || undefined} aria-label={t("nodes.moveDown", { name: nodeLabel(n) })} title={t("nodes.moveDown", { name: nodeLabel(n) })} onClick={() => onMove(1)}>
                <ArrowDown size={18} aria-hidden />
              </button>
            </>
          ) : null}
        </div>
      </div>
      {n.status === "error" && n.enabled ? <NodeProblem n={n} onCheck={onCheck} actions={{ rekey: onRekey, update: updatable(n) ? onUpdate : undefined, edit: onEdit }} /> : null}
      {n.status === "ok" ? <SkewLine n={n} /> : null}
      <HelloLine n={n} actions={{ edit: onEdit }} />
      <dl className="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 text-[13px]">
        <div>
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.protocols")}</dt>
          <dd className="num">{n.status === "ok" ? t("nodes.listeners", { ok: n.listeners_ok, total: n.listeners }) : num(n.inbounds)}</dd>
        </div>
        <div>
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.conns")}</dt>
          <dd className="num">{n.status === "ok" ? num(n.conns) : "—"}</dd>
        </div>
        <div>
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.cpu")}</dt>
          <dd>
            {n.status === "ok" ? (
              <>
                <div className="flex items-center gap-2">
                  <Bar pct={n.cpu_percent} className="flex-1" label={t("nodes.cpu")} />
                  <span className="num w-10 text-right">{Math.round(n.cpu_percent)}%</span>
                </div>
                {/* The bar is the whole server (on the panel's own one, the panel and its database too). */}
                {n.proc_cpu_percent !== undefined ? (
                  <div className="num mt-1 text-xs text-[var(--ink-500)]">{t("nodes.cpuNode", { pct: n.proc_cpu_percent < 1 ? "<1" : Math.round(n.proc_cpu_percent) })}</div>
                ) : null}
              </>
            ) : (
              "—"
            )}
          </dd>
        </div>
        <div>
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.memory")}</dt>
          <dd>
            {n.status === "ok" && n.mem_total ? (
              <div className="flex items-center gap-2" title={`${bytes(n.mem_used)} / ${bytes(n.mem_total)}`}>
                <Bar pct={mem} className="flex-1" label={t("nodes.memory")} />
                <span className="num w-10 text-right">{mem}%</span>
              </div>
            ) : (
              "—"
            )}
          </dd>
        </div>
        <div className="col-span-2">
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.traffic24h")}</dt>
          <dd className="num">{bytes(n.traffic_24h)}</dd>
        </div>
        {!n.local ? (
          <div className="col-span-2">
            <dt className="text-xs text-[var(--ink-500)]">{t("nodes.api")}</dt>
            <dd className="mono truncate">{n.address}</dd>
          </div>
        ) : null}
        {n.version ? (
          <div className="col-span-2">
            <dt className="text-xs text-[var(--ink-500)]">{t("nodes.version")}</dt>
            <dd className="flex flex-wrap items-center gap-2">
              <span className="num">{n.version}</span>
              {n.behind ? <Pill tone="warn">{t("nodes.behind")}</Pill> : null}
            </dd>
          </div>
        ) : null}
        {n.certificate ? (
          <div className="col-span-2">
            <dt className="text-xs text-[var(--ink-500)]">{t("nodes.cert")}</dt>
            <dd className={n.certificate.error ? "text-[var(--berry-600)]" : undefined}>
              {n.certificate.error ? (tMaybe(`errors.acme.${n.certificate.error}`) ?? n.certificate.error) : t("nodes.certOwn", { until: certUntil(n.certificate.not_after) })}
            </dd>
          </div>
        ) : null}
      </dl>
      <NodeUpdate n={n} />
      {/* The actions stay at the card's bottom, level with the card beside it. */}
      <div className="flex-1" aria-hidden />
      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-[var(--hairline)] pt-4">
        {updatable(n) ? (
          <Button size="sm" variant="primary" loading={asking || updating(n)} disabled={busy || updating(n)} onClick={onUpdate}>
            <ArrowUpCircle size={16} aria-hidden /> {updating(n) ? t("nodes.updatingShort") : t("nodes.update")}
          </Button>
        ) : null}
        <Button size="sm" onClick={onEdit}>
          <Pencil size={16} aria-hidden /> {t("nodes.configure")}
        </Button>
        <Button size="sm" onClick={onTraffic}>
          <Activity size={16} aria-hidden /> {t("nodeTraffic.button")}
        </Button>
        <Menu.Root>
          <Menu.Trigger asChild>
            <button type="button" className="icon-btn ml-auto" aria-label={t("nodes.moreActions", { name: nodeLabel(n) })}>
              <MoreHorizontal size={18} aria-hidden />
            </button>
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content className="menu glass-strong" align="end" sideOffset={6}>
              <Menu.Item className="menu-item" onSelect={onCheck}>
                <Stethoscope size={16} aria-hidden /> {t("nodeCheck.button")}
              </Menu.Item>
              <Menu.Item className="menu-item" onSelect={onWarp}>
                <Cloud size={16} aria-hidden /> WARP
              </Menu.Item>
              <Menu.Item className="menu-item" onSelect={onCascade}>
                <Waypoints size={16} aria-hidden /> {t("cascade.title")}
              </Menu.Item>
              <Menu.Item className="menu-item" onSelect={onSpeed}>
                <Gauge size={16} aria-hidden /> {t("speed.button")}
              </Menu.Item>
              <Menu.Item className="menu-item" onSelect={onCert}>
                <ShieldCheck size={16} aria-hidden /> {t("nodes.certButton")}
              </Menu.Item>
              {!n.local ? (
                <>
                  <Menu.Item className="menu-item" onSelect={onRekey}>
                    <KeyRound size={16} aria-hidden /> {t("nodes.rekey")}
                  </Menu.Item>
                  <Menu.Separator className="menu-sep" />
                  <Menu.Item className="menu-item danger" onSelect={onRemove}>
                    <Trash2 size={16} aria-hidden /> {t("common.delete")}
                  </Menu.Item>
                </>
              ) : null}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      </div>
    </section>
  );
}

/**
 * How the node's update goes, or why it did not: the updater on its server says. A node too
 * old to be updated from the panel gets the one command to run on its server instead.
 */
function NodeUpdate({ n }: { n: Node }) {
  const copyText = useCopy();
  const u = n.update;
  if (n.local || !n.behind) return null;
  if (u?.state === "running") {
    return (
      <p className="mt-3 text-[13px] text-[var(--ink-500)]" role="status">
        {t("nodes.updating", { v: u.version })}
      </p>
    );
  }
  if (u?.state === "failed") {
    return (
      <p className="mt-3 text-[13px] text-[var(--berry-600)]" role="alert">
        {t("nodes.updateFailed", { v: u.version, from: u.from || n.version || "", e: (u.error ?? "").split("\n")[0] ?? "" })}
      </p>
    );
  }
  if (n.can_update || n.status !== "ok") return null;
  return (
    <div className="mt-3">
      <p className="mb-2 text-[13px] text-[var(--ink-500)]">{t("nodes.oldNode")}</p>
      <div className="link-field">
        <span className="mono break-all text-xs">{OLD_NODE_COMMAND}</span>
        <button type="button" className="icon-btn" onClick={() => void copyText(OLD_NODE_COMMAND, t("nodes.commandCopied"))} aria-label={t("nodes.copyCommand")}>
          <Copy size={18} />
        </button>
      </div>
    </div>
  );
}

function NodeStatus({ n }: { n: Node }) {
  if (!n.enabled) return <Pill tone="off">{t("nodes.disabled")}</Pill>;
  if (n.status === "ok") return <Pill tone="ok">{t("nodes.online")}</Pill>;
  if (n.status === "error") return <Pill tone="bad">{t("nodes.offline")}</Pill>;
  return <Pill tone="off">{t("nodes.checking")}</Pill>;
}

function AddNodeDrawer({ open, onOpenChange, onJoined }: { open: boolean; onOpenChange: (v: boolean) => void; onJoined: (j: Joined) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const init = { name: "", host: "", domain: "", api_port: "" };
  const [form, setForm] = useState(init);
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => {
    if (open) {
      setForm(init);
      setErrors({});
    }
    // Reset only when the drawer opens.
  }, [open]);
  const create = useMutation({
    mutationFn: (body: Schemas["CreateNodeInputBody"]) => unwrap(api.POST("/api/v1/nodes", { body })),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: qk.nodes });
      void qc.invalidateQueries({ queryKey: qk.inbounds });
      onJoined({ id: r.node.id, name: nodeLabel(r.node), key: r.key, command: r.command });
    },
    onError: (e) => {
      if (e instanceof ApiError && Object.keys(e.fields).length) setErrors(e.fields);
      else toast.error(errorText(e));
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const port = form.api_port.trim();
    if (port && !/^\d{1,5}$/.test(port)) {
      setErrors({ api_port: t("errors.api.bad_port") });
      return;
    }
    create.mutate({ name: form.name.trim(), host: form.host.trim(), domain: form.domain.trim() || undefined, api_port: port ? Number(port) : undefined });
  };
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => {
    setForm((f) => ({ ...f, [k]: e.target.value }));
    setErrors(({ [k]: _, ...rest }) => rest);
  };
  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      title={t("nodes.newTitle")}
      meta={t("nodes.newMeta")}
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" type="submit" form="add-node" loading={create.isPending}>
            {t("nodes.addSubmit")}
          </Button>
        </>
      }
    >
      <form id="add-node" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("nodes.name")} htmlFor="n-name" hint={t("nodes.nameHint")} error={errors.name}>
          <input id="n-name" className="input" value={form.name} onChange={set("name")} placeholder={t("nodes.namePlaceholderNew")} maxLength={48} autoComplete="off" aria-invalid={!!errors.name} />
        </Field>
        <Field label={t("nodes.host")} htmlFor="n-host" hint={t("nodes.hostHint")} error={errors.host}>
          <input id="n-host" className="input mono" value={form.host} onChange={set("host")} placeholder="203.0.113.10" autoComplete="off" aria-invalid={!!errors.host} />
        </Field>
        <Field label={t("nodes.domain")} htmlFor="n-domain" hint={t("nodes.domainHint")} error={errors.domain}>
          <input id="n-domain" className="input mono" value={form.domain} onChange={set("domain")} placeholder="us.example.com" autoComplete="off" aria-invalid={!!errors.domain} />
        </Field>
        <Field label={t("nodes.apiPort")} htmlFor="n-port" hint={t("nodes.apiPortHint")} error={errors.api_port}>
          <input id="n-port" className="input max-w-[160px]" inputMode="numeric" value={form.api_port} onChange={set("api_port")} placeholder={t("nodes.apiPortAuto")} aria-invalid={!!errors.api_port} />
        </Field>
      </form>
    </Drawer>
  );
}

/** The join key is shown once: the panel keeps only its fingerprint. */
function KeyDrawer({ joined, onClose, actions }: { joined: Joined | null; onClose: () => void; actions: FixActions }) {
  const copyText = useCopy();
  const copy = () => joined && copyText(joined.command, t("nodes.commandCopied"));
  return (
    <Drawer
      open={!!joined}
      onOpenChange={(v) => !v && onClose()}
      title={t("nodes.keyTitle")}
      meta={joined?.name}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t("nodes.keyDone")}
        </Button>
      }
    >
      <div className="pt-5">
        <div className="banner warn mb-4">
          <KeyRound size={18} className="shrink-0" aria-hidden />
          <span>{t("nodes.keyOnce")}</span>
        </div>
        <ol className="mb-4 list-decimal space-y-2 pl-5 text-[13px]">
          <li>{t("nodes.keyStep1")}</li>
          <li>{t("nodes.keyStep2")}</li>
        </ol>
        <div className="link-field">
          <span className="mono break-all text-xs">{joined?.command}</span>
          <button type="button" className="icon-btn" onClick={() => void copy()} aria-label={t("nodes.copyCommand")}>
            <Copy size={18} />
          </button>
        </div>
        {joined ? <JoinProgress id={joined.id} actions={actions} /> : null}
      </div>
    </Drawer>
  );
}

function EditNodeDrawer({ node, onClose }: { node: Node | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [form, setForm] = useState({ name: "", public_name: "", host: "", domain: "", enabled: true });
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => {
    if (node) {
      setForm({ name: node.name, public_name: node.public_name, host: node.host, domain: node.domain, enabled: node.enabled });
      setErrors({});
    }
  }, [node]);
  const save = useMutation({
    mutationFn: ({ id, body }: { id: number; body: Schemas["PatchNodeInputBody"] }) => unwrap(api.PATCH("/api/v1/nodes/{id}", { params: { path: { id } }, body })),
    onSuccess: () => {
      toast.ok(t("nodes.saved"));
      onClose();
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.nodes });
      void qc.invalidateQueries({ queryKey: qk.inbounds });
    },
    onError: (e) => {
      if (e instanceof ApiError && Object.keys(e.fields).length) setErrors(e.fields);
      else toast.error(errorText(e));
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!node) return;
    const body: Schemas["PatchNodeInputBody"] = { name: form.name.trim(), public_name: form.public_name.trim(), enabled: form.enabled };
    if (!node.local) {
      body.host = form.host.trim();
      body.domain = form.domain.trim();
    }
    save.mutate({ id: node.id, body });
  };
  const set = (k: "name" | "public_name" | "host" | "domain") => (e: React.ChangeEvent<HTMLInputElement>) => {
    setForm((f) => ({ ...f, [k]: e.target.value }));
    setErrors(({ [k]: _, ...rest }) => rest);
  };
  return (
    <Drawer
      open={!!node}
      onOpenChange={(v) => !v && onClose()}
      title={node ? nodeLabel(node) : ""}
      meta={node?.local ? t("nodes.kindLocal") : t("nodes.kindRemote")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" type="submit" form="edit-node" loading={save.isPending}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form id="edit-node" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("nodes.name")} htmlFor="e-name" hint={t("nodes.nameHint")} error={errors.name}>
          <input id="e-name" className="input" value={form.name} onChange={set("name")} placeholder={t("nodes.namePlaceholderEdit")} maxLength={48} autoComplete="off" aria-invalid={!!errors.name} />
        </Field>
        <Field label={t("nodes.publicName")} htmlFor="e-public-name" hint={t("nodes.publicNameHint")} error={errors.public_name}>
          <input id="e-public-name" className="input" value={form.public_name} onChange={set("public_name")} placeholder={t("nodes.publicNamePlaceholder")} maxLength={80} autoComplete="off" aria-invalid={!!errors.public_name} />
        </Field>
        {node && !node.local ? (
          <>
            <Field label={t("nodes.host")} htmlFor="e-host" hint={t("nodes.hostHint")} error={errors.host}>
              <input id="e-host" className="input mono" value={form.host} onChange={set("host")} autoComplete="off" aria-invalid={!!errors.host} />
            </Field>
            <Field label={t("nodes.domain")} htmlFor="e-domain" hint={t("nodes.domainHint")} error={errors.domain}>
              <input id="e-domain" className="input mono" value={form.domain} onChange={set("domain")} autoComplete="off" aria-invalid={!!errors.domain} />
            </Field>
          </>
        ) : (
          <p className="mb-4 text-[13px] text-[var(--ink-500)]">{t("nodes.localAddress")}</p>
        )}
        <Field label={t("nodes.serving")} hint={t("nodes.servingHint")}>
          <Switch checked={form.enabled} onChange={(v) => setForm((f) => ({ ...f, enabled: v }))} label={t("nodes.serving")} />
        </Field>
      </form>
    </Drawer>
  );
}
