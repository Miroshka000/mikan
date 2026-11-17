// Diagnostics: what the panel's server and its nodes say about themselves, without a shell.
// The logs (redacted at the source), a check of the host with the restart, whether a
// server opens from Russia, and, for a support operator whose access allows it, the
// terminal. The same page serves the admin
// and the operator; the API decides what each may do.
import { keepPreviousData, useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { ClipboardCopy, FileText, Globe, MapPin, Power, RefreshCw, Search, Stethoscope, TerminalSquare } from "lucide-react";
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { useSupportMode } from "../../api/support";
import { QueryBoundary } from "../../components/query";
import { Confirm } from "../../components/overlay";
import { Tabs } from "../../components/tabs";
import { Switch } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, EmptyState, Field, PageHeader, Segmented, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { ago, time } from "../../lib/format";
import { useServers } from "../../lib/node-label";
import { DIAG_TABS } from "../search";
import { GeoTab } from "./geo-check";
import { CheckSkeleton, ReachView, StatusMark } from "./node-check";
import { ServerPicks, useServerPicks } from "./server-picks";
import { CardHead } from "../../components/layout";
import { Select } from "../../components/select";

const TerminalTab = lazy(() => import("./diag-terminal").then((m) => ({ default: m.TerminalTab })));

type Tab = (typeof DIAG_TABS)[number];
type Report = Schemas["HostReport"];

export function DiagnosticsPage() {
  const { tab, source } = useSearch({ from: "/_app/diagnostics" });
  const navigate = useNavigate({ from: "/diagnostics" });
  const who = useSupportMode();
  const servers = useServers();
  const terminal = who?.level === "terminal";
  const current: Tab = tab === "terminal" && !terminal ? "logs" : (tab ?? "logs");
  const shown = servers.some((s) => s.id === source) ? (source as string) : "panel";
  const go = (next: Tab) => void navigate({ search: { tab: next === "logs" ? undefined : next, source: shown === "panel" ? undefined : shown }, replace: true });
  const pick = (id: string) => void navigate({ search: { tab: current === "logs" ? undefined : current, source: id === "panel" ? undefined : id }, replace: true });
  const tabs: { id: Tab; label: string; icon: typeof FileText }[] = [
    { id: "logs", label: t("diag.tabs.logs"), icon: FileText },
    { id: "check", label: t("diag.tabs.check"), icon: Stethoscope },
    { id: "reach", label: t("diag.tabs.reach"), icon: Globe },
    { id: "geo", label: t("diag.tabs.geo"), icon: MapPin },
    ...(terminal ? [{ id: "terminal" as const, label: t("diag.tabs.terminal"), icon: TerminalSquare }] : []),
  ];
  return (
    <>
      <PageHeader title={t("nav.diagnostics")} sub={t("diag.subtitle")} />
      <Tabs id="diag" tabs={tabs} value={current} onChange={go} label={t("diag.sections")}>
        {current === "logs" ? <LogsTab servers={servers} source={shown} onSource={pick} /> : null}
        {current === "check" ? <CheckTab servers={servers} source={shown} onSource={pick} level={who?.level} /> : null}
        {current === "reach" ? <ReachTab /> : null}
        {current === "geo" ? <GeoTab /> : null}
        {current === "terminal" && who ? (
          <Suspense fallback={<Skeleton style={{ height: 320, borderRadius: 20 }} />}>
            <TerminalTab who={who} />
          </Suspense>
        ) : null}
      </Tabs>
    </>
  );
}

function ServerSelect({ servers, source, onSource, id }: { servers: { id: string; label: string }[]; source: string; onSource: (id: string) => void; id: string }) {
  return (
    <Field label={t("diag.server")} htmlFor={id}>
      <Select id={id} className="input min-w-[200px]" value={source} onChange={(e) => onSource(e.target.value)}>
        {servers.map((s) => (
          <option key={s.id} value={s.id}>
            {s.label}
          </option>
        ))}
      </Select>
    </Field>
  );
}

/* ----------------------------------------------------------------- logs */

const LEVELS = ["all", "warn", "error"] as const;
type LevelFilter = (typeof LEVELS)[number];
const TAILS = [200, 500, 1000, 2000] as const;
const RANK: Record<string, number> = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };

function LogsTab({ servers, source, onSource }: { servers: { id: string; label: string }[]; source: string; onSource: (id: string) => void }) {
  const copyText = useCopy();
  const [level, setLevel] = useState<LevelFilter>("all");
  const [q, setQ] = useState("");
  const [tail, setTail] = useState<number>(500);
  const [auto, setAuto] = useState(true);
  const logs = useQuery({
    queryKey: ["support", "logs", source, tail],
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/logs", { params: { query: { source, tail } }, signal })),
    placeholderData: keepPreviousData,
    // The tail moves while something happens; it is not asked for while the tab is hidden.
    refetchInterval: auto ? 5_000 : false,
  });
  const lines = logs.data?.lines;
  const shown = useMemo(() => {
    if (!lines) return [];
    const min = level === "error" ? RANK.ERROR! : level === "warn" ? RANK.WARN! : 0;
    const needle = q.trim().toLowerCase();
    return lines.filter((l) => (RANK[l.level] ?? 1) >= min && (!needle || l.text.toLowerCase().includes(needle)));
  }, [lines, level, q]);
  const box = useRef<HTMLDivElement>(null);
  // Follows the end while the reader is at the end; reading higher up stays where it is.
  const atEnd = useRef(true);
  useEffect(() => {
    const el = box.current;
    if (el && atEnd.current) el.scrollTop = el.scrollHeight;
  }, [shown, source]);
  return (
    <section className="card glass reveal" aria-labelledby="diag-logs-title">
      <CardHead title={t("diag.logs.title")} sub={t("diag.logs.lead")} id="diag-logs-title" />
      <div className="flex flex-wrap items-end gap-x-4 gap-y-1">
        <ServerSelect servers={servers} source={source} onSource={onSource} id="logs-source" />
        <Field label={t("diag.logs.level")}>
          <Segmented<LevelFilter>
            label={t("diag.logs.level")}
            value={level}
            onChange={setLevel}
            options={LEVELS.map((v) => ({ value: v, label: t(`diag.logs.levels.${v}`) }))}
          />
        </Field>
        <Field label={t("diag.logs.tail")} htmlFor="logs-tail">
          <Select id="logs-tail" className="input" value={tail} onChange={(e) => setTail(Number(e.target.value))}>
            {TAILS.map((n) => (
              <option key={n} value={n}>
                {t("diag.logs.lines", { n })}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("diag.logs.search")} htmlFor="logs-search">
          <label className="search-field">
            <Search size={16} aria-hidden />
            <input id="logs-search" type="search" placeholder={t("diag.logs.searchPlaceholder")} value={q} onChange={(e) => setQ(e.target.value)} autoComplete="off" spellCheck={false} />
          </label>
        </Field>
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-[13px] text-[var(--ink-600)]">
          <Switch checked={auto} onChange={setAuto} label={t("diag.logs.auto")} />
          {t("diag.logs.auto")}
        </label>
        <Button size="sm" loading={logs.isFetching && !logs.isPlaceholderData} onClick={() => void logs.refetch()}>
          <RefreshCw size={16} aria-hidden /> {t("diag.logs.refresh")}
        </Button>
        <Button size="sm" disabled={shown.length === 0} onClick={() => void copyText(shown.map((l) => l.text).join("\n"), t("diag.logs.copied"))}>
          <ClipboardCopy size={16} aria-hidden /> {t("diag.logs.copy")}
        </Button>
        {lines ? (
          <span className="ml-auto text-xs text-[var(--ink-500)]" aria-live="polite">
            {t("diag.logs.count", { shown: shown.length, total: lines.length })}
          </span>
        ) : null}
      </div>
      <QueryBoundary query={logs} pending={<Skeleton style={{ height: 320, borderRadius: 12 }} />}>
        {() =>
          shown.length === 0 ? (
            <EmptyState icon={FileText} search={!!lines?.length} title={lines?.length ? t("diag.logs.noMatch") : t("diag.logs.empty")} text={lines?.length ? t("diag.logs.noMatchText") : t("diag.logs.emptyText")} />
          ) : (
            <div
              ref={box}
              className="log-view"
              role="log"
              aria-label={t("diag.logs.viewLabel")}
              aria-live="off"
              tabIndex={0}
              onScroll={(e) => {
                const el = e.currentTarget;
                atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
              }}
            >
              {shown.map((l, i) => (
                <div key={`${l.at}-${i}`} className="log-line">
                  <span className="log-at num">{time(l.at)}</span>
                  <span className={`log-lvl lv-${l.level}`}>{l.level}</span>
                  <span className="log-text">{l.text}</span>
                </div>
              ))}
            </div>
          )
        }
      </QueryBoundary>
    </section>
  );
}

/* ----------------------------------------------------------------- check */

function CheckTab({ servers, source, onSource, level }: { servers: { id: string; label: string }[]; source: string; onSource: (id: string) => void; level?: "view" | "change" | "terminal" }) {
  const toast = useToast();
  const copyText = useCopy();
  const [reports, setReports] = useState<Record<string, Report>>({});
  const [confirm, setConfirm] = useState(false);
  const [requested, setRequested] = useState<Record<string, boolean>>({});
  const nodeId = source === "panel" ? null : Number(source.slice("node:".length));
  const doctor = useMutation({
    mutationFn: (): Promise<Report> => (nodeId === null ? unwrap(api.POST("/api/v1/system/doctor")) : unwrap(api.POST("/api/v1/nodes/{id}/doctor", { params: { path: { id: nodeId } } }))),
    // Which server was asked, to show its answer (or its failure) under that server only.
    onMutate: () => source,
  });
  // The report belongs to the server it was asked of, whichever server is shown by the time it comes.
  const run = () => {
    const at = source;
    doctor.mutate(undefined, { onSuccess: (r) => setReports((m) => ({ ...m, [at]: r })) });
  };
  const restart = useMutation({
    mutationFn: () => (nodeId === null ? unwrap(api.POST("/api/v1/system/restart")) : unwrap(api.POST("/api/v1/nodes/{id}/restart", { params: { path: { id: nodeId } } }))),
    onSuccess: () => {
      setConfirm(false);
      setRequested((m) => ({ ...m, [source]: true }));
    },
    onError: (e) => {
      setConfirm(false);
      toast.error(errorText(e));
    },
  });
  const report = reports[source];
  const readOnly = level === "view";
  const name = servers.find((s) => s.id === source)?.label ?? source;
  // One server's check runs at a time: the mutation's state is the shown server's only while it is the one asked.
  const pending = doctor.isPending && doctor.context === source;
  return (
    <section className="card glass reveal" aria-labelledby="diag-check-title">
      <CardHead title={t("diag.check.title")} sub={t("diag.check.lead")} id="diag-check-title" />
      <div className="flex flex-wrap items-end gap-x-4 gap-y-1">
        <ServerSelect servers={servers} source={source} onSource={onSource} id="check-source" />
        <div className="mb-4 flex flex-wrap gap-2">
          <Button variant="primary" loading={pending} disabled={doctor.isPending} onClick={run}>
            <Stethoscope size={16} aria-hidden /> {report ? t("diag.check.again") : t("diag.check.run")}
          </Button>
          <Button variant="danger" disabled={readOnly || restart.isPending} onClick={() => setConfirm(true)} title={readOnly ? t("diag.check.restartViewOnly") : undefined}>
            <Power size={16} aria-hidden /> {t("diag.check.restart")}
          </Button>
        </div>
      </div>
      {readOnly ? <p className="mb-3 text-xs text-[var(--ink-500)]">{t("diag.check.restartViewOnly")}</p> : null}
      {requested[source] ? (
        <div className="banner info mb-4" role="status">
          <Power size={18} className="shrink-0" aria-hidden />
          <span>{t("diag.check.restartAsked", { name })}</span>
        </div>
      ) : null}
      <div aria-live="polite" aria-busy={pending}>
        {pending ? (
          <>
            <p className="mb-3 text-[13px] text-[var(--ink-600)]">{t("diag.check.wait")}</p>
            <CheckSkeleton />
          </>
        ) : doctor.isError && doctor.context === source ? (
          <div className="banner err mb-3" role="alert">
            {t("diag.check.failed")}: {errorText(doctor.error)}
          </div>
        ) : report ? (
          <ReportView report={report} onCopy={() => void copyText(reportText(report, name), t("diag.check.copied"))} />
        ) : (
          <EmptyState icon={Stethoscope} title={t("diag.check.emptyTitle")} text={t("diag.check.emptyText")} />
        )}
      </div>
      <Confirm
        open={confirm}
        onOpenChange={setConfirm}
        title={t("diag.check.restartTitle", { name })}
        text={t("diag.check.restartText")}
        confirm={t("diag.check.restartConfirm")}
        danger
        loading={restart.isPending}
        onConfirm={() => restart.mutate()}
      />
    </section>
  );
}

const ITEM_STATUS = { ok: "ok", warn: "warn", error: "fail" } as const;

function ReportView({ report, onCopy }: { report: Report; onCopy: () => void }) {
  const rank = { error: 0, warn: 1, ok: 2 } as const;
  const items = report.items.map((it, i) => ({ it, i })).sort((a, b) => rank[a.it.level] - rank[b.it.level] || a.i - b.i);
  const bad = report.errors > 0;
  const warn = report.warnings > 0;
  return (
    <>
      <div className={`banner ${bad ? "err" : warn ? "warn" : "info"} mb-4`} role="status">
        <Stethoscope size={18} className="shrink-0" aria-hidden />
        <span className="min-w-0 flex-1">
          {bad || warn ? [bad ? t("nodeCheck.problems", { n: report.errors }) : "", warn ? t("nodeCheck.warnings", { n: report.warnings }) : ""].filter(Boolean).join(", ") : t("nodeCheck.allOk")}
        </span>
        <Button size="sm" variant="ghost" onClick={onCopy}>
          <ClipboardCopy size={16} aria-hidden /> {t("nodeCheck.copyReport")}
        </Button>
      </div>
      <ul className="row-list">
        {items.map(({ it, i }) => (
          <li key={i} className="flex gap-3 py-3">
            <StatusMark status={ITEM_STATUS[it.level]} />
            <div className="min-w-0 flex-1">
              <div className="text-[13px] font-semibold text-[var(--ink-900)]">{it.label}</div>
              {it.detail ? <p className="mono mt-1 break-words whitespace-pre-wrap text-[var(--ink-600)]">{it.detail}</p> : null}
              {it.fix ? <p className="mt-1 text-[13px] text-[var(--ink-600)]">{it.fix}</p> : null}
            </div>
          </li>
        ))}
      </ul>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("diag.check.checkedAt", { ago: ago(report.at), version: report.version || "?" })}</p>
    </>
  );
}

/** The report as text for a ticket; the host already cut the secrets out of it. */
function reportText(r: Report, name: string): string {
  const mark = { ok: "OK", warn: "WARN", error: "ERROR" } as const;
  return [`${name} (${r.kind}) ${r.version} ${r.at}`, ...r.items.map((it) => `[${mark[it.level]}] ${it.label}${it.detail ? `: ${it.detail}` : ""}${it.fix ? ` (${it.fix})` : ""}`)].join("\n");
}

/* ----------------------------------------------------------------- reach */

/** "22, 443" as ports: one to three, each 1–65535; null when it is not. */
function parsePorts(text: string): number[] | null {
  const parts = text.split(/[\s,;]+/).filter(Boolean);
  const ports = [...new Set(parts.map((p) => (/^\d{1,5}$/.test(p) ? Number(p) : NaN)))];
  return ports.length >= 1 && ports.length <= 3 && ports.every((p) => p >= 1 && p <= 65535) ? ports : null;
}

/** Whether a server opens from Russia, before anything is installed on it or after. */
function ReachTab() {
  const picks = useServerPicks();
  const [host, setHost] = useState("");
  const [portsText, setPortsText] = useState("22");
  const [touched, setTouched] = useState(false);
  const ports = parsePorts(portsText);
  const check = useMutation({
    mutationFn: (body: { host: string; ports: number[] }) => unwrap(api.POST("/api/v1/system/reach", { body })),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (!host.trim() || !ports) return;
    check.mutate({ host: host.trim(), ports });
  };
  return (
    <section className="card glass reveal" aria-labelledby="diag-reach-title">
      <CardHead title={t("diag.reach.title")} sub={t("diag.reach.lead")} id="diag-reach-title" />
      <form className="flex flex-wrap items-start gap-x-4 gap-y-1" onSubmit={submit} noValidate>
        <Field label={t("diag.reach.host")} htmlFor="reach-host">
          <input
            id="reach-host"
            className="input min-w-[240px]"
            value={host}
            onChange={(e) => setHost(e.target.value)}
            placeholder={t("diag.reach.hostPlaceholder")}
            autoComplete="off"
            spellCheck={false}
            inputMode="url"
            required
          />
        </Field>
        <Field label={t("diag.reach.ports")} htmlFor="reach-ports" hint={t("diag.reach.portsHint")} error={touched && !ports ? t("diag.reach.portsBad") : undefined}>
          <input
            id="reach-ports"
            className="input w-[160px]"
            value={portsText}
            onChange={(e) => setPortsText(e.target.value)}
            inputMode="numeric"
            autoComplete="off"
            aria-invalid={touched && !ports}
          />
        </Field>
        <div className="mt-[26px] mb-4">
          <Button type="submit" variant="primary" loading={check.isPending} disabled={check.isPending || !host.trim()}>
            <Globe size={16} aria-hidden /> {t("diag.reach.run")}
          </Button>
        </div>
      </form>
      <ServerPicks picks={picks} value={host} onPick={setHost} />
      <p className="mb-4 text-xs text-[var(--ink-500)]">{t("diag.reach.caveat")}</p>
      <div aria-live="polite" aria-busy={check.isPending}>
        {check.isPending ? (
          <>
            <p className="mb-3 text-[13px] text-[var(--ink-600)]">{t("diag.reach.running")}</p>
            <Skeleton style={{ height: 240, borderRadius: 16 }} />
          </>
        ) : check.isError ? (
          <div className="banner err mb-3" role="alert">
            {errorText(check.error)}
          </div>
        ) : check.data ? (
          <ReachView res={check.data} />
        ) : (
          <EmptyState icon={Globe} title={t("diag.reach.emptyTitle")} text={t("diag.reach.emptyText")} />
        )}
      </div>
    </section>
  );
}
