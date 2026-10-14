import { Link } from "@tanstack/react-router";
import { Plus, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { errorText, type Overview, type User } from "../../api/client";
import { onePeriod, useInbounds, useNode, useNodes, useNodeShares, useOverview, userActions, useServerTraffic, useUserMutation, useUsers } from "../../api/hooks";
import { buckets, TrafficChart, type Range } from "../../components/chart";
import { QueryBoundary, StaleNotice } from "../../components/query";
import { useToast } from "../../components/toast";
import { Avatar, Bar, Button, ErrorState, PageHeader, Pill, Segmented, Skeleton, StatePill } from "../../components/ui";
import { getLocale, t } from "../../i18n";
import { bits, bytes, dateShort, expiryText, maskedAs, num, uptime } from "../../lib/format";
import { nodeLabel } from "../../lib/node-label";

export function Dashboard() {
  const node = useNode();
  const status = node.data ? (node.data.ok ? t("dashboard.statusOk") : t("dashboard.statusDown")) : t("dashboard.statusChecking");
  const today = new Intl.DateTimeFormat(getLocale(), { weekday: "long", day: "numeric", month: "long" }).format(new Date());
  return (
    <>
      <PageHeader
        title={t("dashboard.title")}
        sub={`${today.charAt(0).toUpperCase()}${today.slice(1)} · ${status}`}
        actions={
          <Link to="/users" search={{ state: "all", q: "", create: true }} className="btn btn-primary">
            <Plus size={18} aria-hidden />
            <span className="max-[760px]:hidden">{t("dashboard.newUser")}</span>
          </Link>
        }
      />
      <Kpis />
      {/* The chart and under it the subscriptions to attend to and the top users; the server
          card has the column beside them and is exactly as tall (its protocols scroll), and
          the two cards side by side share their height, so nothing leaves a hole. */}
      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 xl:grid-cols-[minmax(0,1.75fr)_minmax(0,1fr)]">
        <div className="grid min-w-0 gap-4">
          <TrafficCard />
          <div className="grid grid-cols-[minmax(0,1fr)] gap-4 min-[1440px]:grid-cols-2">
            <AttentionCard />
            <TopCard />
          </div>
        </div>
        <ServerCard />
      </div>
      <NodeTrafficCard />
    </>
  );
}

function Kpi({ i, label, value, foot }: { i: number; label: string; value: React.ReactNode; foot: React.ReactNode }) {
  return (
    <section className="card glass kpi reveal" style={{ "--i": i } as React.CSSProperties}>
      <div className="kpi-label">{label}</div>
      <div className="kpi-value num">{value}</div>
      <div className="kpi-foot">{foot}</div>
    </section>
  );
}

/** A card that has nothing to show because its data did not load: a way to try again, never an empty "all good". */
function CardError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  return <ErrorState text={errorText(error)} onRetry={onRetry} />;
}

function Kpis() {
  const o = useOverview();
  const d = o.data;
  if (!d) {
    if (o.isError) {
      return (
        <section className="card glass">
          <CardError error={o.error} onRetry={() => void o.refetch()} />
        </section>
      );
    }
    return (
      <div className="grid grid-cols-2 gap-4 xl:grid-cols-4" role="status" aria-busy aria-label={t("common.loading")}>
        {[0, 1, 2, 3].map((i) => (
          <section key={i} className="card glass kpi">
            <Skeleton style={{ width: "50%" }} />
            <Skeleton style={{ width: "40%", height: 28, marginTop: 8 }} />
          </section>
        ))}
      </div>
    );
  }
  const delta = d.traffic_yesterday > 0 ? Math.round(((d.traffic_today - d.traffic_yesterday) / d.traffic_yesterday) * 100) : null;
  const [value, unit] = bytes(d.traffic_today).split(" ");
  return (
    <>
      {o.isError ? <StaleNotice onRetry={() => void o.refetch()} retrying={o.isFetching} /> : null}
      <KpiGrid d={d} delta={delta} value={value} unit={unit} />
    </>
  );
}

function KpiGrid({ d, delta, value, unit }: { d: Overview; delta: number | null; value?: string; unit?: string }) {
  return (
    <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">
      <Kpi i={0} label={t("dashboard.online")} value={num(d.online)} foot={<span>{t("dashboard.onlineFoot")}</span>} />
      <Kpi i={1} label={t("dashboard.active")} value={num(d.users_active)} foot={<span>{t("dashboard.activeFoot", { total: num(d.users_total) })}</span>} />
      <Kpi
        i={2}
        label={t("dashboard.trafficToday")}
        value={
          <>
            {value}
            <small>{unit}</small>
          </>
        }
        foot={
          delta === null ? (
            <span>{t("dashboard.noTrafficYesterday")}</span>
          ) : (
            <span className={delta >= 0 ? "text-[var(--leaf-700)]" : ""}>{delta >= 0 ? t("dashboard.moreThanYesterday", { pct: delta }) : t("dashboard.lessThanYesterday", { pct: -delta })}</span>
          )
        }
      />
      <Kpi
        i={3}
        label={t("dashboard.expiring7d")}
        value={num(d.expiring_7d)}
        foot={
          d.expiring_7d > 0 ? (
            // The count is of all users, hidden ones too: the list it opens shows them as well.
            <Link to="/users" search={{ state: "expiring", q: "", hidden: "show" }} className="link-btn">
              {t("dashboard.showList")}
            </Link>
          ) : (
            <span>{t("dashboard.noneExpiring")}</span>
          )
        }
      />
    </div>
  );
}

function TrafficCard() {
  const [range, setRange] = useState<Range>("24h");
  const traffic = useServerTraffic(range);
  const points = useMemo(() => buckets(traffic.data?.points ?? [], range), [traffic.data, range]);
  const totalDown = points.reduce((a, p) => a + p.down, 0);
  const totalUp = points.reduce((a, p) => a + p.up, 0);
  return (
    <section className="card glass reveal" style={{ "--i": 4 } as React.CSSProperties} aria-labelledby="traffic-title">
      <div className="card-head">
        <div>
          <h2 className="card-title" id="traffic-title">
            {t("dashboard.traffic")}
          </h2>
          <div className="mt-1.5 flex gap-4 text-[13px] text-[var(--ink-600)]">
            <span className="inline-flex items-center gap-2">
              <i className="h-2 w-2 rounded-full bg-[var(--mikan-500)]" /> {t("chart.down")}
            </span>
            <span className="inline-flex items-center gap-2">
              <i className="h-2 w-2 rounded-full bg-[var(--lagoon-500)]" /> {t("chart.up")}
            </span>
          </div>
        </div>
        <Segmented
          label={t("dashboard.period")}
          value={range}
          onChange={setRange}
          options={[
            { value: "24h", label: t("dashboard.range24h") },
            { value: "7d", label: t("dashboard.range7d") },
            { value: "30d", label: t("dashboard.range30d") },
          ]}
        />
      </div>
      <QueryBoundary query={traffic} pending={<Skeleton style={{ height: 220, borderRadius: 16 }} />}>
        {() => (
          <>
            <TrafficChart points={points} range={range} />
            <div className="mt-3 flex flex-wrap gap-x-6 gap-y-2 text-[13px] text-[var(--ink-500)]">
              <span>
                {t("chart.down")} <b className="num font-semibold text-[var(--ink-900)]">{bytes(totalDown)}</b>
              </span>
              <span>
                {t("chart.up")} <b className="num font-semibold text-[var(--ink-900)]">{bytes(totalUp)}</b>
              </span>
            </div>
          </>
        )}
      </QueryBoundary>
    </section>
  );
}

/** How many protocols the server card lists before "N more". */
const INBOUNDS_SHOWN = 5;

function ServerCard() {
  const node = useNode();
  const inbounds = useInbounds();
  const [allInbounds, setAllInbounds] = useState(false);
  const n = node.data;
  const list = inbounds.data ?? [];
  const memPct = n && n.system.mem_total ? (n.system.mem_used / n.system.mem_total) * 100 : 0;
  return (
    <section className="card glass reveal xl:flex xl:h-0 xl:min-h-full xl:flex-col" style={{ "--i": 5 } as React.CSSProperties} aria-labelledby="srv-title">
      <div className="card-head">
        <div>
          <h2 className="card-title" id="srv-title">
            {t("shell.server")}
          </h2>
          <div className="card-sub">{n?.ok ? `${n.core}${n.started_at ? ` · ${t("dashboard.uptime", { uptime: uptime(n.started_at) })}` : ""}` : t("dashboard.nodeState")}</div>
        </div>
        {n ? n.ok ? <Pill tone="ok">{t("shell.running")}</Pill> : <Pill tone="bad">{t("shell.offline")}</Pill> : null}
      </div>
      {n && !n.ok ? (
        <div className="banner err mb-4" role="alert">
          <span>
            {t("dashboard.nodeUnreachable")} <span className="mono whitespace-nowrap">mikan logs node</span>
          </span>
        </div>
      ) : null}
      {node.isError ? (
        n ? (
          <StaleNotice onRetry={() => void node.refetch()} retrying={node.isFetching} />
        ) : (
          <CardError error={node.error} onRetry={() => void node.refetch()} />
        )
      ) : null}
      <div className="mb-4 grid grid-cols-3 gap-2">
        <div className="metric">
          <div className="metric-label">CPU</div>
          <div className="metric-value num">
            {n ? Math.round(n.system.cpu_percent) : "—"}
            <small>%</small>
          </div>
          <Bar pct={n?.system.cpu_percent ?? 0} className="mt-2" label="CPU" />
        </div>
        <div className="metric">
          <div className="metric-label">{t("dashboard.memory")}</div>
          <div className="metric-value num">
            {n ? Math.round(memPct) : "—"}
            <small>%</small>
          </div>
          <Bar pct={memPct} className="mt-2" label={t("dashboard.memory")} />
        </div>
        <div className="metric">
          <div className="metric-label">{t("dashboard.netDown")}</div>
          <div className="metric-value num text-[15px]">{n ? bits(n.system.net_rx_bps) : "—"}</div>
        </div>
      </div>
      {inbounds.isError && !inbounds.data ? <CardError error={inbounds.error} onRetry={() => void inbounds.refetch()} /> : null}
      {/* Beside the chart (xl) the card is as tall as its row and the protocols scroll in it;
          stacked, the first few are shown and the rest open on request. */}
      <div className="row-list border-t border-[var(--hairline)] [scrollbar-width:thin] xl:min-h-0 xl:flex-1 xl:overflow-y-auto">
        {list.map((l, i) => (
          <div key={l.id} className={`grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 py-3${i >= INBOUNDS_SHOWN && !allInbounds ? " max-xl:hidden" : ""}`}>
            <div className="min-w-0">
              <div className="text-[13px] font-medium">{l.sub_name}</div>
              <div className="truncate text-xs text-[var(--ink-500)]">
                {l.port}/{l.network}
                {l.dest ? ` · ${t("inbounds.maskedAs", { dest: maskedAs(l) })}` : ""}
              </div>
            </div>
            {!l.enabled ? (
              <Pill tone="off">{t("inbounds.off")}</Pill>
            ) : l.status === "error" ? (
              <Pill tone="bad">{t("inbounds.error")}</Pill>
            ) : l.status === "ok" ? (
              <Pill tone="ok">{t("inbounds.ok")}</Pill>
            ) : (
              <Pill tone="off">…</Pill>
            )}
          </div>
        ))}
      </div>
      {list.length > INBOUNDS_SHOWN ? (
        <button type="button" className="link-btn mt-1 text-[13px] xl:hidden" aria-expanded={allInbounds} onClick={() => setAllInbounds((v) => !v)}>
          {allInbounds ? t("dashboard.lessInbounds") : t("dashboard.moreInbounds", { n: list.length - INBOUNDS_SHOWN })}
        </button>
      ) : null}
      <div className="panel-soft mt-3 flex shrink-0 items-start gap-2 p-3 text-xs leading-4 text-[var(--ink-600)]">
        <ShieldCheck size={16} className="shrink-0 text-[var(--leaf-500)]" aria-hidden />
        <span>{t("dashboard.safetyNote")}</span>
      </div>
    </section>
  );
}

/**
 * What each node carried and its share of the whole. With one node there is nothing to
 * compare, and the card is not there.
 */
function NodeTrafficCard() {
  const nodes = useNodes();
  const [range, setRange] = useState<Range>("24h");
  const several = (nodes.data?.length ?? 0) >= 2;
  const shares = useNodeShares(range, several);
  if (!several) return null;
  const total = shares.data?.total ?? 0;
  return (
    <section className="card glass reveal" style={{ "--i": 8 } as React.CSSProperties} aria-labelledby="node-traffic-title">
      <div className="card-head">
        <div>
          <h2 className="card-title" id="node-traffic-title">
            {t("dashboard.nodeTraffic")}
          </h2>
          <div className="card-sub">{shares.data ? t("dashboard.nodeTrafficSub", { total: bytes(total) }) : "…"}</div>
          {/* The per-node accounting is younger than the period: a shorter one shows the same. */}
          {shares.data?.since ? <div className="card-sub">{t("dashboard.nodeTrafficSince", { date: dateShort(shares.data.since) })}</div> : null}
        </div>
        <Segmented
          label={t("dashboard.period")}
          value={range}
          onChange={setRange}
          options={[
            { value: "24h", label: t("dashboard.range24h") },
            { value: "7d", label: t("dashboard.range7d") },
            { value: "30d", label: t("dashboard.range30d") },
          ]}
        />
      </div>
      <QueryBoundary
        query={shares}
        pending={
          <div className="space-y-3" role="status" aria-busy aria-label={t("common.loading")}>
            <Skeleton style={{ height: 36 }} />
            <Skeleton style={{ height: 36 }} />
          </div>
        }
      >
        {(data) =>
          total === 0 ? (
            <p className="py-6 text-center text-[13px] text-[var(--ink-500)]">{t("dashboard.nodeTrafficEmpty")}</p>
          ) : (
            <ul className="row-list" aria-busy={shares.isPlaceholderData}>
              {data.items.map((n) => {
                const pct = (n.bytes / total) * 100;
                return (
                  <li key={n.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]">
                    <span className="truncate text-[13px] font-medium">{nodeLabel(n)}</span>
                    <span className="num text-right text-[13px] sm:order-3 sm:min-w-[132px]">
                      <b className="font-semibold">{bytes(n.bytes)}</b> <span className="text-[var(--ink-500)]">{percent(pct)}</span>
                    </span>
                    <Bar pct={pct} label={nodeLabel(n)} className="col-span-2 sm:order-2 sm:col-span-1 [&>i]:!bg-[var(--mikan-400)]" />
                  </li>
                );
              })}
            </ul>
          )
        }
      </QueryBoundary>
    </section>
  );
}

/** A share of the whole: tenths below 10 % (a node with 0.4 % is not "0 %"), whole numbers above. */
function percent(p: number): string {
  return `${p < 10 ? (Math.round(p * 10) / 10).toLocaleString(getLocale()) : Math.round(p)}%`;
}

const ATTENTION_ROWS = 5;

function AttentionCard() {
  // Out of traffic, then expiring, then expired: one request, five rows, every half minute.
  const attention = useUsers({ state: "attention", q: "" }, { limit: ATTENTION_ROWS, refetchInterval: 30_000 });
  const toast = useToast();
  const extend = useUserMutation(userActions.extend);
  const list: User[] = attention.data?.items ?? [];
  // A list that did not load is not "all good": the users it would have shown are missing.
  const failed = attention.data === undefined && attention.isError ? attention : undefined;
  const loading = !failed && attention.data === undefined;
  const retry = () => void attention.refetch();
  return (
    <section className="card glass reveal flex flex-col" style={{ "--i": 6 } as React.CSSProperties} aria-labelledby="att-title">
      <div className="card-head">
        <div>
          <h2 className="card-title" id="att-title">
            {t("dashboard.attention")}
          </h2>
          <div className="card-sub">{loading || failed ? "…" : list.length ? t("dashboard.attentionSub") : t("dashboard.allGood")}</div>
        </div>
      </div>
      {failed ? (
        <CardError error={failed.error} onRetry={retry} />
      ) : loading ? (
        <div className="space-y-3" role="status" aria-busy aria-label={t("common.loading")}>
          <Skeleton style={{ height: 36 }} />
          <Skeleton style={{ height: 36 }} />
        </div>
      ) : list.length === 0 ? (
        <p className="flex flex-1 items-center justify-center py-6 text-center text-[13px] text-[var(--ink-500)]">{t("dashboard.attentionEmpty")}</p>
      ) : (
        <div className="row-list">
          {list.map((u) => {
            const exp = expiryText(u.expires_at);
            return (
              <div key={u.id} className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 py-3">
                <Avatar name={u.name} seed={u.id} />
                <div className="min-w-0">
                  <div className="flex items-center gap-2 text-[13px]">
                    <b className="truncate font-semibold">{u.name}</b>
                    <StatePill state={u.state} />
                  </div>
                  <div className="text-xs text-[var(--ink-500)]">
                    {u.state === "limited" ? t("dashboard.used", { bytes: bytes(u.used_up + u.used_down) }) : u.expires_at ? t("dashboard.until", { date: dateShort(u.expires_at), left: exp.text }) : ""}
                  </div>
                </div>
                {u.state === "limited" ? (
                  <Link to="/users" search={{ state: "all", q: "", user: u.id }} className="btn btn-glass btn-sm">
                    {t("common.open")}
                  </Link>
                ) : (
                  <Button
                    size="sm"
                    loading={extend.isPending && extend.variables?.id === u.id}
                    onClick={() =>
                      extend.mutate(
                        { id: u.id, ...onePeriod(u) },
                        { onSuccess: (r) => toast.ok(t("dashboard.extended", { name: u.name, date: dateShort(r.expires_at!) })), onError: () => toast.error(t("dashboard.extendFailed")) },
                      )
                    }
                  >
                    {u.billing_day != null ? t("dashboard.plusMonth") : t("dashboard.plus30")}
                  </Button>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function TopCard() {
  const o = useOverview();
  const top = o.data?.top_month ?? [];
  const max = top[0]?.bytes ?? 1;
  return (
    <section className="card glass reveal flex flex-col" style={{ "--i": 7 } as React.CSSProperties} aria-labelledby="top-title">
      <div className="card-head">
        <div>
          <h2 className="card-title" id="top-title">
            {t("dashboard.top")}
          </h2>
          <div className="card-sub">{t("dashboard.topSub")}</div>
        </div>
      </div>
      {o.data === undefined && o.isError ? (
        <CardError error={o.error} onRetry={() => void o.refetch()} />
      ) : o.isPending ? (
        <Skeleton style={{ height: 120 }} />
      ) : top.length === 0 ? (
        <p className="flex flex-1 items-center justify-center py-6 text-center text-[13px] text-[var(--ink-500)]">{t("dashboard.topEmpty")}</p>
      ) : (
        top.map((u, i) => (
          <Link key={u.id} to="/users" search={{ state: "all", q: "", user: u.id }} className="grid grid-cols-[24px_minmax(0,1fr)_auto] items-center gap-3 rounded-xl py-2 hover:bg-[var(--hover)]">
            <span className="font-display text-xs text-[var(--ink-400)]">{i + 1}</span>
            <div className="min-w-0">
              <div className="mb-1.5 truncate text-[13px] font-medium">{u.name}</div>
              <Bar pct={(u.bytes / max) * 100} className="[&>i]:!bg-[var(--mikan-400)]" />
            </div>
            <span className="num min-w-[72px] text-right text-[13px] font-medium">{bytes(u.bytes)}</span>
          </Link>
        ))
      )}
    </section>
  );
}
