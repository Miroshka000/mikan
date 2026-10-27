// A node's card on the Servers page: its state, address, certificate and update, and what can be done with it.
import * as Menu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Activity, ArrowDown, ArrowUp, ArrowUpCircle, Cloud, Copy, Gauge, KeyRound, LoaderCircle, MoreHorizontal, Pencil, RefreshCw, ShieldCheck, Stethoscope, Trash2, Waypoints } from "lucide-react";
import { api, errorText, unwrap } from "../../../api/client";
import { qk, useNodes } from "../../../api/hooks";
import { AcmeAttempts, AcmeProblem, caName } from "../../../components/acme";
import { certUntil } from "../../../components/cert-drawer";
import { TagChips } from "../../../components/tags";
import { useToast } from "../../../components/toast";
import { Bar, Button, Pill } from "../../../components/ui";
import { t, tMaybe } from "../../../i18n";
import { useCopy } from "../../../lib/copy";
import { bytes, num } from "../../../lib/format";
import { nodeLabel } from "../../../lib/node-label";
import { HelloLine, NodeProblem, SkewLine } from "../node-check";
import type { Node } from "../nodes";

/** What a server before 0.5.0.2 runs once by hand: from then on the panel updates it. */
const OLD_NODE_COMMAND = "mikan update";

/** The panel can update this node itself and it is behind. */
export const updatable = (n: Node) => !n.local && n.behind && n.can_update;

export const updating = (n: Node) => n.update?.state === "running";

export function NodeCard({
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
    <section className="card glass reveal flex min-w-0 flex-col" style={{ "--i": idx } as React.CSSProperties}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="font-display truncate text-lg font-medium tracking-tight">{nodeLabel(n)}</h2>
          <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--ink-500)]">
            <span className="mono min-w-0 break-all">{n.host || "—"}</span>
            <span>·</span>
            <span>{n.local ? t("nodes.kindLocal") : t("nodes.kindRemote")}</span>
          </div>
          {n.tags.length > 0 ? (
            <div className="mt-1.5 text-xs">
              <TagChips tags={n.tags} />
            </div>
          ) : null}
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
      <dl className="mt-4 grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-x-4 gap-y-3 text-[13px] [&>div]:min-w-0">
        <div>
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.protocols")}</dt>
          <dd className="num">{n.status === "ok" ? t("nodes.listeners", { ok: n.listeners_ok, total: n.listeners }) : num(n.inbounds)}</dd>
        </div>
        <div>
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.conns")}</dt>
          <dd className="num">{n.status === "ok" ? num(n.conns) : "—"}</dd>
          {n.status === "ok" ? <CascadeConnsLine cascade={n.cascade} /> : null}
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
          {n.cascade_24h > 0 ? <dd className="num mt-1 text-xs text-[var(--ink-500)]">{t("nodes.cascade24h", { bytes: bytes(n.cascade_24h) })}</dd> : null}
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
        <NodeCertRow n={n} />
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
 * The certificate of the node's protocols on TLS: its own, the public one the panel got for
 * it, the panel's, or a pinned self-signed one, with why a public one is not there yet and
 * a button to get it now.
 */
/** The other nodes' cascades through this one, open now: their sum, and by whom in the hint.
 * The source counts these users and their traffic; here they are the node's load. */
function CascadeConnsLine({ cascade }: { cascade: Node["cascade"] }) {
  const nodes = useNodes();
  const sum = cascade.reduce((a, c) => a + c.conns, 0);
  if (sum === 0) return null;
  const from = cascade
    .map((c) => {
      const src = nodes.data?.find((x) => x.id === c.node_id);
      return `${src ? nodeLabel(src) : `#${c.node_id}`}: ${num(c.conns)}`;
    })
    .join(", ");
  return (
    <dd className="num mt-1 text-xs text-[var(--ink-500)]" title={t("nodes.cascadeFrom", { from })}>
      {t("nodes.cascadeConns", { n: num(sum) })}
      <span className="sr-only">. {t("nodes.cascadeFrom", { from })}</span>
    </dd>
  );
}

function NodeCertRow({ n }: { n: Node }) {
  const qc = useQueryClient();
  const toast = useToast();
  const renew = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/nodes/{id}/certificate/renew", { params: { path: { id: n.id } } })),
    onSuccess: (v) => {
      if (!v.acme?.error) toast.ok(v.acme?.ordering ? t("certs.stillRunning") : t("certs.got"));
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: qk.nodes }),
    onError: (e) => toast.error(errorText(e)),
  });
  const own = n.certificate;
  const tls = n.tls;
  if (!tls && !own) return null;
  const order = tls?.acme;
  const until = certUntil(tls?.not_after ?? undefined);
  let text: string;
  if (own?.error) text = tMaybe(`errors.acme.${own.error}`) ?? own.error;
  else if (tls?.kind === "custom" || (!tls && own)) text = t("nodes.certOwn", { until: certUntil(own?.not_after ?? tls?.not_after ?? undefined) });
  else if (tls?.kind === "acme") text = t("certs.kindAcme", { ca: caName(tls.ca), until });
  else if (tls?.kind === "panel") text = t("certs.kindPanel", { until });
  else text = t("certs.kindSelf");
  const ordering = renew.isPending || !!order?.ordering;
  // A button where the panel orders for the node: none on the panel's own node or with an own one.
  const canOrder = !!order && tls?.kind !== "custom";
  const needed = tls?.kind !== "acme" || !!order?.error;
  return (
    <div className="col-span-2">
      <dt className="text-xs text-[var(--ink-500)]">{t("nodes.cert")}</dt>
      <dd>
        <span className={own?.error ? "text-[var(--berry-600)]" : undefined}>{text}</span>
        {tls?.kind === "acme" && tls.pinned ? <span className="text-xs text-[var(--ink-500)]"> · {t("certs.applying")}</span> : null}
        {ordering ? (
          <span className="mt-1 flex items-center gap-1.5 text-xs text-[var(--ink-500)]" role="status">
            <LoaderCircle size={14} className="spin" aria-hidden /> {t("certs.nodeOrdering")}
          </span>
        ) : order?.error ? (
          <AcmeProblem code={order.error} detail={order.error_detail} holder={order.holder} retryAt={order.retry_at} host={order.identifier} node className="mt-1" />
        ) : null}
        <AcmeAttempts attempts={order?.attempts} className="mt-1" />
        {tls?.pinned && tls.kind !== "acme" ? <p className="mt-1 text-xs text-[var(--ink-500)]">{t("certs.pinnedNoteNode")}</p> : null}
        {canOrder && needed && !(order?.error === "node_outdated" && n.behind) ? (
          <div className="mt-2">
            <Button size="sm" loading={renew.isPending} disabled={ordering} onClick={() => renew.mutate()}>
              <RefreshCw size={16} aria-hidden /> {order?.error ? t("certs.retry") : t("certs.getNow")}
            </Button>
          </div>
        ) : null}
      </dd>
    </div>
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
