// What is wrong with a node and how to fix it: the reason in plain words on its card, the
// full "Check node" list with a button or a command for each fix, the check of its ports
// from Russia, the live steps of a node being added, and the same check list for the
// panel's own server.
import { useMutation, useQuery } from "@tanstack/react-query";
import { CircleCheck, CircleMinus, CircleX, ClipboardCopy, Copy, Globe, KeyRound, Pencil, RefreshCw, Stethoscope, TriangleAlert, ArrowUpCircle } from "lucide-react";
import { useEffect, type ReactNode } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { useNodes } from "../../api/hooks";
import { Drawer } from "../../components/overlay";
import { Button, Skeleton } from "../../components/ui";
import { t, tMaybe } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { ago, bytes } from "../../lib/format";
import { nodeLabel } from "../../lib/node-label";

type Node = Schemas["NodeInfo"];
type Item = Schemas["CheckItem"];
type Status = Item["status"];
type Params = Record<string, string>;

/** What the buttons of a fix do; a fix without its action shows only its words. */
export type FixActions = { rekey?: () => void; update?: () => void; edit?: () => void };

/** The link failures after the port opened: something answered on it, the node or not. */
const ANSWER_CODES = ["pin_mismatch", "tls", "http_status"];

/** The node's API port, from its host:port. */
export const apiPort = (n: Node) => n.address.slice(n.address.lastIndexOf(":") + 1);

/** Commands of the fixes that only the server can do, filled with the item's facts. */
function commands(fix: string | undefined, p: Params): string[] {
  switch (fix) {
    case "open_port":
      return p.port ? [`ufw allow ${p.port}/tcp`] : [];
    case "start_node":
      return ["mikan status", "mikan logs node"];
    case "old_node":
      return ["mikan update"];
    case "free_port":
      return p.port ? [`ss -ltnp 'sport = :${p.port}'`] : [];
    case "sync_time":
      return ["timedatectl set-ntp true"];
    case "free_disk":
      return ["df -h /opt/mikan", "docker image prune"];
    case "check_outbound":
      return ["mikan doctor"];
    case "restart_panel":
      return ["mikan status", "mikan restart"];
  }
  return [];
}

/** The facts an item's words name, with the derived ones (sizes, the clock's distance, times). */
function factsOf(it: { params?: Params | null }): Params {
  const p: Params = { port: "?", host: "?", name: "", ...(it.params ?? {}) };
  if (p.skew) p.abs = String(Math.abs(Number(p.skew)));
  if (p.free) p.free = bytes(Number(p.free));
  if (p.available) p.available = bytes(Number(p.available));
  if (p.at) p.ago = ago(p.at);
  if (p.name === "mikan~relay") p.name = "relay";
  return p;
}

/** The words of a node's code (nodeapi.Link* and the hello's own). */
export function codeText(code: string | undefined | null, params?: Params | null): string {
  const p = factsOf({ params });
  return (code && tMaybe(`nodeCheck.code.${code}`, p)) || t("nodeCheck.code.unknown", p);
}

function itemText(it: Item): { title: string; text: string } {
  const p = factsOf(it);
  const title = tMaybe(`nodeCheck.id.${it.id}`, p) ?? it.id;
  const text =
    (it.code && tMaybe(`nodeCheck.text.${it.id}.${it.code}`, p)) || tMaybe(`nodeCheck.text.${it.id}.${it.status}`, p) || (it.code && tMaybe(`nodeCheck.code.${it.code}`, p)) || "";
  return { title, text };
}

const STATUS_ICON: Record<Status, { icon: typeof CircleCheck; cls: string }> = {
  ok: { icon: CircleCheck, cls: "text-[var(--leaf-700)]" },
  warn: { icon: TriangleAlert, cls: "text-[var(--honey-600)]" },
  fail: { icon: CircleX, cls: "text-[var(--berry-600)]" },
  skip: { icon: CircleMinus, cls: "text-[var(--ink-400)]" },
};

/** A status as an icon with its word for screen readers: never by colour alone. */
export function StatusMark({ status }: { status: Status }) {
  const { icon: Icon, cls } = STATUS_ICON[status];
  return (
    <span className={`mt-px shrink-0 ${cls}`}>
      <Icon size={18} aria-hidden />
      <span className="sr-only">{t(`nodeCheck.status.${status}`)}</span>
    </span>
  );
}

function Command({ cmd }: { cmd: string }) {
  const copyText = useCopy();
  return (
    <div className="link-field mt-2">
      <span className="mono break-all text-xs">{cmd}</span>
      <button type="button" className="icon-btn" onClick={() => void copyText(cmd, t("nodeCheck.commandCopied"))} aria-label={`${t("nodeCheck.copyCommand")}: ${cmd}`}>
        <Copy size={18} aria-hidden />
      </button>
    </div>
  );
}

/** How to fix it: the words, the panel's button for it, or the commands for the server. */
export function Fix({ fix, params, actions, panelServer }: { fix?: string | null; params?: Params | null; actions?: FixActions; panelServer?: boolean }) {
  if (!fix) return null;
  const p = factsOf({ params });
  const cmds = commands(fix, p);
  const button =
    fix === "rekey" && actions?.rekey ? (
      <Button size="sm" onClick={actions.rekey}>
        <KeyRound size={16} aria-hidden /> {t("nodeCheck.rekey")}
      </Button>
    ) : fix === "update_node" && actions?.update ? (
      <Button size="sm" variant="primary" onClick={actions.update}>
        <ArrowUpCircle size={16} aria-hidden /> {t("nodeCheck.updateNode")}
      </Button>
    ) : fix === "check_host" && actions?.edit ? (
      <Button size="sm" onClick={actions.edit}>
        <Pencil size={16} aria-hidden /> {t("nodeCheck.editHost")}
      </Button>
    ) : null;
  const words = tMaybe(`nodeCheck.fix.${fix}`, p);
  return (
    <div className="mt-2 text-[13px] text-[var(--ink-600)]">
      {words ? <p>{words}</p> : null}
      {cmds.length > 0 ? (
        <>
          <p className="mt-2 text-xs text-[var(--ink-500)]">{panelServer || fix === "restart_panel" ? t("nodeCheck.onPanel") : t("nodeCheck.onNode")}</p>
          {cmds.map((c) => (
            <Command key={c} cmd={c} />
          ))}
        </>
      ) : null}
      {button ? <div className="mt-2 flex flex-wrap gap-2">{button}</div> : null}
    </div>
  );
}

function Details({ text }: { text?: string | null }) {
  if (!text) return null;
  return (
    <details className="mt-2 text-xs">
      <summary className="cursor-pointer text-[var(--ink-500)]">{t("nodeCheck.details")}</summary>
      <code className="mono mt-1 block break-all text-[var(--ink-600)]">{text}</code>
    </details>
  );
}

/** The summary above a check: all good, or how many problems and warnings. */
function Summary({ items }: { items: Item[] }) {
  const fails = items.filter((i) => i.status === "fail").length;
  const warns = items.filter((i) => i.status === "warn").length;
  if (!fails && !warns) {
    return (
      <div className="banner info mb-4" role="status">
        <CircleCheck size={18} className="shrink-0" aria-hidden />
        <span>{t("nodeCheck.allOk")}</span>
      </div>
    );
  }
  const parts = [fails ? t("nodeCheck.problems", { n: fails }) : "", warns ? t("nodeCheck.warnings", { n: warns }) : ""].filter(Boolean);
  return (
    <div className={`banner ${fails ? "err" : "warn"} mb-4`} role="status">
      <TriangleAlert size={18} className="shrink-0" aria-hidden />
      <span>{parts.join(", ")}</span>
    </div>
  );
}

/** A check's lines, problems first. */
export function CheckList({ items, actions, panelServer }: { items: Item[]; actions?: FixActions; panelServer?: boolean }) {
  const rank: Record<Status, number> = { fail: 0, warn: 1, ok: 2, skip: 3 };
  const sorted = items.map((it, i) => ({ it, i })).sort((a, b) => rank[a.it.status] - rank[b.it.status] || a.i - b.i);
  return (
    <>
      <Summary items={items} />
      <ul className="row-list">
        {sorted.map(({ it, i }) => {
          const { title, text } = itemText(it);
          return (
            <li key={i} className="flex gap-3 py-3">
              <StatusMark status={it.status} />
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-semibold text-[var(--ink-900)]">{title}</div>
                {text ? <p className="mt-1 text-[13px] text-[var(--ink-600)]">{text}</p> : null}
                {it.status === "fail" || it.status === "warn" ? <Fix fix={it.fix} params={it.params} actions={actions} panelServer={panelServer} /> : null}
                {it.status !== "ok" ? <Details text={it.detail} /> : null}
              </div>
            </li>
          );
        })}
      </ul>
    </>
  );
}

export function CheckSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <div className="space-y-3" aria-hidden>
      <Skeleton style={{ height: 44, borderRadius: 16 }} />
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex gap-3">
          <Skeleton style={{ height: 20, width: 20, borderRadius: 999 }} />
          <div className="flex-1 space-y-2">
            <Skeleton style={{ height: 12, width: "40%", borderRadius: 6 }} />
            <Skeleton style={{ height: 12, width: "80%", borderRadius: 6 }} />
          </div>
        </div>
      ))}
    </div>
  );
}

/** Copies a check's report: the server masked the addresses, names, ports and keys in it. */
function CopyReport({ report, disabled }: { report?: string; disabled?: boolean }) {
  const copyText = useCopy();
  return (
    <Button disabled={disabled || !report} onClick={() => report && void copyText(report, t("nodeCheck.reportCopied"))}>
      <ClipboardCopy size={16} aria-hidden /> {t("nodeCheck.copyReport")}
    </Button>
  );
}

/** "Check node": everything the panel can find out about a node, each problem with its fix. */
export function NodeCheckDrawer({ node, onClose, actions }: { node: Node | null; onClose: () => void; actions: FixActions }) {
  const id = node?.id;
  const check = useMutation({
    mutationFn: (nodeId: number) => unwrap(api.POST("/api/v1/nodes/{id}/check", { params: { path: { id: nodeId } } })),
  });
  const { mutate, reset } = check;
  useEffect(() => {
    reset();
    if (id != null) mutate(id);
  }, [id, mutate, reset]);
  return (
    <Drawer
      open={!!node}
      onOpenChange={(v) => !v && onClose()}
      title={t("nodeCheck.title")}
      meta={node ? nodeLabel(node) : undefined}
      footer={
        <>
          <CopyReport report={check.data?.report} disabled={check.isPending} />
          <Button variant="primary" loading={check.isPending} onClick={() => id != null && check.mutate(id)}>
            <RefreshCw size={16} aria-hidden /> {t("nodeCheck.again")}
          </Button>
        </>
      }
    >
      <div className="pt-5">
        <p className="mb-4 text-[13px] text-[var(--ink-600)]">{t("nodeCheck.lead")}</p>
        <div aria-live="polite" aria-busy={check.isPending}>
          {check.isPending ? (
            <>
              <p className="sr-only">{t("nodeCheck.running")}</p>
              <CheckSkeleton />
            </>
          ) : check.isError ? (
            <div className="banner err" role="alert">
              <CircleX size={18} className="shrink-0" aria-hidden />
              <span>
                {t("nodeCheck.failed")}: {errorText(check.error)}
              </span>
            </div>
          ) : check.data ? (
            <CheckList items={check.data.items} actions={actions} />
          ) : null}
        </div>
        {node ? <RussiaCheck node={node} /> : null}
      </div>
    </Drawer>
  );
}

/** The node's ports from Russian cities, through check-host.net: asked for on purpose. */
function RussiaCheck({ node }: { node: Node }) {
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/nodes/{id}/check-russia", { params: { path: { id: node.id } } })),
  });
  const res = run.data;
  const label = (p: Schemas["RussiaPort"]) => (p.name === "api" ? t("nodeCheck.russia.api", { port: p.port }) : `${p.name} ${p.port}`);
  const failing = res?.cities.some((c) => c.ports.some((p) => !p.ok));
  return (
    <section className="mt-6 border-t border-[var(--hairline)] pt-5" aria-labelledby="russia-title">
      <h3 id="russia-title" className="text-[15px] font-semibold text-[var(--ink-900)]">
        {t("nodeCheck.russia.title")}
      </h3>
      <p className="mt-1 text-[13px] text-[var(--ink-600)]">{t("nodeCheck.russia.lead")}</p>
      <p className="mt-2 text-xs text-[var(--ink-500)]">{t("nodeCheck.russia.caveat")}</p>
      <Button className="mt-3" size="sm" loading={run.isPending} onClick={() => run.mutate()}>
        <Globe size={16} aria-hidden /> {t("nodeCheck.russia.button")}
      </Button>
      <div className="mt-3" aria-live="polite" aria-busy={run.isPending}>
        {run.isPending ? (
          <>
            <p className="sr-only">{t("nodeCheck.russia.running")}</p>
            <Skeleton style={{ height: 120, borderRadius: 16 }} />
          </>
        ) : run.isError ? (
          <div className="banner err" role="alert">
            {errorText(run.error)}
          </div>
        ) : res ? (
          <>
            <div className={`banner ${failing ? "warn" : "info"} mb-3`} role="status">
              <span>{failing ? t("nodeCheck.russia.someFail") : t("nodeCheck.russia.allOk")}</span>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-left text-[13px]">
                <thead>
                  <tr className="text-xs text-[var(--ink-500)]">
                    <th className="py-2 pr-3 font-medium">{t("nodeCheck.russia.city")}</th>
                    {res.ports.map((p) => (
                      <th key={p.port} className="py-2 pr-3 font-medium">
                        {label(p)}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {res.cities.map((c) => (
                    <tr key={c.id} className="border-t border-[var(--hairline)]">
                      <td className="py-2 pr-3">{c.city || c.id}</td>
                      {c.ports.map((p) => (
                        <td key={p.port} className="py-2 pr-3">
                          <span className="flex items-start gap-1">
                            <StatusMark status={p.ok ? "ok" : p.pending ? "skip" : "fail"} />
                            <span>{p.ok ? t("nodeCheck.russia.ok", { ms: p.ms ?? 0 }) : p.pending ? t("nodeCheck.russia.pending") : p.error}</span>
                          </span>
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <p className="mt-2 text-xs text-[var(--ink-500)]">{t("nodeCheck.russia.checkedAt", { ago: ago(res.at) })}</p>
          </>
        ) : null}
      </div>
    </section>
  );
}

/** Why a node does not answer, with its fix, on the node's card. */
export function NodeProblem({ n, onCheck, actions }: { n: Node; onCheck: () => void; actions: FixActions }) {
  if (n.local) {
    return (
      <div className="mt-3 text-[13px]" role="alert">
        <p className="text-[var(--berry-600)]">{t("nodes.localOffline")}</p>
        <Fix fix="restart_panel" panelServer />
      </div>
    );
  }
  const code = n.error_code ?? "unknown";
  const fix =
    code === "timeout" ? "open_port" : code === "pin_mismatch" ? "rekey" : code === "tls" ? "free_port" : code === "unreachable" ? "check_host" : code === "dns" ? "check_dns" : "start_node";
  return (
    <div className="mt-3 text-[13px]" role="alert">
      <p className="text-[var(--berry-600)]">{codeText(code, n.error_params)}</p>
      <p className="mt-1 text-xs text-[var(--ink-500)]">
        {n.last_ok_at ? t("nodeCheck.lastSeen", { ago: ago(n.last_ok_at) }) : t("nodeCheck.neverSeen")}
        {n.last_ok_at && n.error_since ? ` · ${t("nodeCheck.lostAgo", { ago: ago(n.error_since) })}` : null}
      </p>
      <Fix fix={fix} params={n.error_params} actions={actions} />
      <Details text={n.error} />
      <Button className="mt-3" size="sm" onClick={onCheck}>
        <Stethoscope size={16} aria-hidden /> {t("nodeCheck.button")}
      </Button>
    </div>
  );
}

/** The node's last hello on its card: worth a line when it says something. */
export function HelloLine({ n, actions }: { n: Node; actions: FixActions }) {
  const h = n.hello;
  if (!h || n.local) return null;
  if (h.ip_differs) {
    return (
      <div className="mt-3 text-[13px]" role="status">
        <p className="text-[var(--honey-600)]">{t("nodeCheck.helloIp", { seen: h.seen_ip ?? "?", host: h.host ?? "?" })}</p>
        <Fix fix="check_host" actions={actions} />
      </div>
    );
  }
  if (h.ok) return <p className="mt-3 text-xs text-[var(--ink-500)]">{t("nodeCheck.helloOk", { ago: ago(h.at) })}</p>;
  // A node that does not answer has its reason shown already; this says it is installed.
  return (
    <p className="mt-3 text-xs text-[var(--ink-500)]">
      {t("nodeCheck.helloFail", { ago: ago(h.at) })}: {codeText(h.code, h.params)}
    </p>
  );
}

/** A node's clock far from the panel's breaks REALITY: a line with the fix. */
export function SkewLine({ n }: { n: Node }) {
  const s = n.clock_skew;
  if (s == null || Math.abs(s) < 30) return null;
  return (
    <div className="mt-3 text-[13px]" role="status">
      <p className="text-[var(--honey-600)]">{t(s > 0 ? "nodeCheck.skewAhead" : "nodeCheck.skewBehind", { n: Math.abs(s) })}</p>
      <Fix fix="sync_time" />
    </div>
  );
}

type Step = { key: string; status: "done" | "wait" | "fail"; text: string; extra?: ReactNode };

/**
 * The steps of a node being added, as the panel sees them: polled while the key drawer is
 * open. A failing step shows its reason and fix; closing the drawer changes nothing.
 */
export function JoinProgress({ id, actions }: { id: number; actions: FixActions }) {
  const nodes = useNodes(true);
  const n = nodes.data?.find((x) => x.id === id);
  const ready = !!n && n.status === "ok" && n.listeners > 0 && n.listeners_ok === n.listeners;
  const internet = useQuery({
    queryKey: ["node-join-check", id],
    queryFn: () => unwrap(api.POST("/api/v1/nodes/{id}/check", { params: { path: { id } } })),
    enabled: ready,
    staleTime: Infinity,
    retry: 1,
  });
  if (!n) {
    return nodes.isPending ? <CheckSkeleton rows={4} /> : null;
  }
  const code = n.error_code ?? "";
  const hello = n.hello;
  const ok = n.status === "ok";
  const port = apiPort(n);
  const answered = ANSWER_CODES.includes(code);
  const installed = ok || !!hello || answered;
  const reached = ok || answered;
  // Installed for sure (it said hello), and the panel still cannot get to its port.
  const blocked = !reached && !!hello && !hello.ok;
  const keyBad = code === "pin_mismatch" || code === "tls" || code === "http_status";
  const steps: Step[] = [
    { key: "install", status: installed ? "done" : "wait", text: installed ? t("nodeCheck.join.installDone") : t("nodeCheck.join.install") },
    {
      key: "reach",
      status: reached ? "done" : blocked ? "fail" : "wait",
      text: reached ? t("nodeCheck.join.reach", { port }) : t("nodeCheck.join.reachWait", { port }),
      extra: blocked ? (
        <>
          <p className="mt-1 text-[13px] text-[var(--berry-600)]">{codeText(hello?.code ?? code, hello?.params ?? n.error_params)}</p>
          <Fix fix={(hello?.code ?? code) === "refused" ? "start_node" : (hello?.code ?? code) === "unreachable" ? "check_host" : "open_port"} params={hello?.params ?? n.error_params} actions={actions} />
        </>
      ) : null,
    },
    {
      key: "key",
      status: ok ? "done" : keyBad ? "fail" : "wait",
      text: ok ? t("nodeCheck.join.key") : t("nodeCheck.join.keyWait"),
      extra: keyBad ? (
        <>
          <p className="mt-1 text-[13px] text-[var(--berry-600)]">{codeText(code, n.error_params)}</p>
          <Fix fix={code === "pin_mismatch" ? "rekey" : code === "tls" ? "free_port" : "start_node"} params={n.error_params} actions={actions} />
        </>
      ) : null,
    },
    {
      key: "protocols",
      status: ready ? "done" : ok && n.listeners > 0 ? "fail" : "wait",
      text: ok && n.listeners > 0 ? t("nodeCheck.join.protocols", { ok: n.listeners_ok, total: n.listeners }) : t("nodeCheck.join.protocolsWait"),
    },
  ];
  const net = internet.data?.items.find((i) => i.id === "node_internet");
  const old = internet.data?.items.some((i) => i.id === "diagnose" && i.code === "old_node");
  steps.push({
    key: "internet",
    status: net?.status === "ok" ? "done" : net?.status === "fail" ? "fail" : "wait",
    text:
      net?.status === "ok" ? t("nodeCheck.join.internet") : net?.status === "fail" ? t("nodeCheck.join.internetFail") : old ? t("nodeCheck.join.internetSkip") : t("nodeCheck.join.internetWait"),
    extra: net?.status === "fail" ? <Fix fix="check_outbound" /> : null,
  });
  const done = steps.every((s) => s.status === "done") || (ready && !!old);
  return (
    <section className="mt-5" aria-labelledby="join-title">
      <h3 id="join-title" className="text-[15px] font-semibold text-[var(--ink-900)]">
        {t("nodeCheck.join.title")}
      </h3>
      <ol className="mt-2" aria-live="polite">
        {steps.map((s) => (
          <li key={s.key} className="flex gap-3 py-2">
            <StatusMark status={s.status === "done" ? "ok" : s.status === "fail" ? "fail" : "skip"} />
            <div className="min-w-0 flex-1">
              <div className="text-[13px] text-[var(--ink-900)]">
                {s.text}
                <span className="sr-only">
                  {" "}
                  ({s.status === "done" ? t("nodeCheck.join.stepDone") : s.status === "fail" ? t("nodeCheck.join.stepFail") : t("nodeCheck.join.stepWait")})
                </span>
              </div>
              {s.extra}
            </div>
          </li>
        ))}
      </ol>
      {!installed ? <p className="mt-1 text-xs text-[var(--ink-500)]">{t("nodeCheck.join.installHint")}</p> : null}
      {done ? (
        <div className="banner info mt-3" role="status">
          <CircleCheck size={18} className="shrink-0" aria-hidden />
          <span>{t("nodeCheck.join.done")}</span>
        </div>
      ) : (
        <p className="mt-3 text-xs text-[var(--ink-500)]">{t("nodeCheck.join.closeHint")}</p>
      )}
    </section>
  );
}

/** "Check server" in Settings → System: the same list for the panel's own server. */
export function ServerCheckCard() {
  const check = useMutation({ mutationFn: () => unwrap(api.POST("/api/v1/system/check")) });
  return (
    <section className="card glass reveal" aria-labelledby="server-check-title">
      <div className="card-head">
        <div>
          <h2 className="card-title" id="server-check-title">
            {t("nodeCheck.server.title")}
          </h2>
          <div className="card-sub">{t("nodeCheck.server.lead")}</div>
        </div>
      </div>
      <div aria-live="polite" aria-busy={check.isPending}>
        {check.isPending ? (
          <CheckSkeleton />
        ) : check.isError ? (
          <div className="banner err mb-3" role="alert">
            {t("nodeCheck.failed")}: {errorText(check.error)}
          </div>
        ) : check.data ? (
          <CheckList items={check.data.items} panelServer />
        ) : null}
      </div>
      <div className="mt-4 flex flex-wrap gap-2">
        <Button variant="primary" loading={check.isPending} onClick={() => check.mutate()}>
          <Stethoscope size={16} aria-hidden /> {check.data ? t("nodeCheck.again") : t("nodeCheck.server.button")}
        </Button>
        {check.data ? <CopyReport report={check.data.report} /> : null}
      </div>
    </section>
  );
}
