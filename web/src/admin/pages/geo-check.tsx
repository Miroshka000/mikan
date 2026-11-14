// Which country an address is in for the public geo bases: sites and apps pick a client's
// region by them, and they disagree.
import { useMutation } from "@tanstack/react-query";
import { Globe2, MapPin } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { Button, EmptyState, Field, Skeleton } from "../../components/ui";
import { t, tMaybe } from "../../i18n";
import { ago } from "../../lib/format";
import { countryName, StatusMark } from "./node-check";
import { ServerPicks, useServerPicks } from "./server-picks";
import { useNodes } from "../../api/hooks";
import { nodeLabel } from "../../lib/node-label";
import { CardHead } from "../../components/layout";
import { Select } from "../../components/select";

type Geo = Schemas["GeoCheck"];
type Node = Schemas["NodeInfo"];
type Regions = Schemas["ServiceRegions"];
type Answer = Schemas["RegionAnswer"];

/** 🇩🇪 for DE: two regional indicator letters. */
function flag(code: string): string {
  return String.fromCodePoint(...[...code.toUpperCase()].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
}

/** The verdict, then each base's answer, a disagreeing one marked. */
export function GeoView({ res }: { res: Geo }) {
  const answered = res.answers.filter((a) => a.country);
  return (
    <div className="flex flex-col gap-3">
      {res.country ? (
        <div className={`banner ${res.agree ? "info" : "warn"}`} role="status">
          <span aria-hidden>{flag(res.country)}</span>
          <span>
            {res.agree
              ? t("geo.agree", { country: countryName(res.country), n: answered.length })
              : t("geo.disagree", { country: countryName(res.country), n: answered.filter((a) => a.country === res.country).length, total: answered.length })}
          </span>
        </div>
      ) : (
        <div className="banner err" role="status">
          {t("geo.none")}
        </div>
      )}
      <ul className="flex flex-col divide-y divide-[var(--hairline)] rounded-2xl border border-[var(--hairline)]">
        {res.answers.map((a) => (
          <li key={a.service} className="flex items-center gap-3 px-4 py-2 text-[13px]">
            <StatusMark status={!a.country ? "skip" : a.country === res.country ? "ok" : "warn"} />
            <span className="min-w-0 flex-1 truncate text-[var(--ink-700)]">{a.service}</span>
            {a.country ? (
              <span className="whitespace-nowrap">
                <span aria-hidden>{flag(a.country)} </span>
                {countryName(a.country)}
              </span>
            ) : (
              <span className="text-[var(--ink-500)]">{t(`geo.error.${a.error ?? "failed"}`)}</span>
            )}
          </li>
        ))}
      </ul>
      <p className="text-xs text-[var(--ink-500)]">
        {res.ip ? `${res.ip} · ` : ""}
        {t("geo.checkedAt", { ago: ago(res.at) })}
        {res.cached ? ` · ${t("geo.cached")}` : ""}
      </p>
    </div>
  );
}

/** The brand names the node's checks go by; the yes/no ones say what they check. */
function serviceName(id: string): string {
  return tMaybe(`geo.services.names.${id}`) ?? id;
}

/** One answer: a flag and the country, yes or no, or why there is none. */
function AnswerCell({ a, access }: { a: Answer; access: boolean }) {
  if (a.error) return <span className="text-[var(--ink-500)]">{t(`geo.services.error.${a.error}`)}</span>;
  if (access) {
    return a.open ? (
      <span className="text-[var(--leaf-700)]">{t("geo.services.yes")}</span>
    ) : (
      <span className="text-[var(--berry-600)]">{t("geo.services.no")}</span>
    );
  }
  if (!a.country) return <span className="text-[var(--ink-500)]">—</span>;
  return (
    <span className="whitespace-nowrap" title={countryName(a.country)}>
      <span aria-hidden>{flag(a.country)} </span>
      {a.country}
    </span>
  );
}

/** What each site makes of the node's address, over IPv4 and IPv6. */
export function RegionsView({ res }: { res: Regions }) {
  const countries = res.services.flatMap((s) => (s.access ? [] : [s.v4.country, s.v6.country])).filter(Boolean) as string[];
  const counts = new Map<string, number>();
  for (const c of countries) counts.set(c, (counts.get(c) ?? 0) + 1);
  const [top] = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const v6 = res.services.some((s) => s.v6.error !== "no_ip");
  return (
    <div className="flex flex-col gap-3">
      {top ? (
        <div className={`banner ${counts.size === 1 ? "info" : "warn"}`} role="status">
          <span aria-hidden>{flag(top[0])}</span>
          <span>{counts.size === 1 ? t("geo.services.agree", { country: countryName(top[0]) }) : t("geo.services.disagree", { country: countryName(top[0]) })}</span>
        </div>
      ) : null}
      <div className="overflow-x-auto rounded-2xl border border-[var(--hairline)]">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="text-left text-xs text-[var(--ink-500)]">
              <th className="px-4 py-2 font-medium">{t("geo.services.service")}</th>
              <th className="px-4 py-2 font-medium">IPv4</th>
              <th className="px-4 py-2 font-medium">IPv6</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--hairline)]">
            {res.services.map((s) => (
              <tr key={s.service}>
                <td className="px-4 py-2 text-[var(--ink-700)]">{serviceName(s.service)}</td>
                <td className="px-4 py-2">
                  <AnswerCell a={s.v4} access={s.access} />
                </td>
                <td className="px-4 py-2">
                  <AnswerCell a={s.v6} access={s.access} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="text-xs text-[var(--ink-500)]">
        {v6 ? "" : `${t("geo.services.noV6")} · `}
        {t("geo.checkedAt", { ago: ago(res.at) })}
      </p>
    </div>
  );
}

/** Asks the sites from a node: they see the node's own address, as its clients' apps come. */
function RegionsCheck({ nodeId, compact }: { nodeId: number | null; compact?: boolean }) {
  const run = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/nodes/{id}/regions", { params: { path: { id } } })),
  });
  return (
    <>
      <Button className={compact ? "mt-3" : undefined} size="sm" loading={run.isPending} disabled={run.isPending || nodeId == null} onClick={() => nodeId != null && run.mutate(nodeId)}>
        <Globe2 size={16} aria-hidden /> {t("geo.services.button")}
      </Button>
      <div className="mt-3" aria-live="polite" aria-busy={run.isPending}>
        {run.isPending ? (
          <>
            <p className="mb-3 text-[13px] text-[var(--ink-600)]">{t("geo.services.running")}</p>
            <Skeleton style={{ height: 320, borderRadius: 16 }} />
          </>
        ) : run.isError ? (
          <div className="banner err" role="alert">
            {errorText(run.error)}
          </div>
        ) : run.data ? (
          <RegionsView res={run.data} />
        ) : null}
      </div>
    </>
  );
}

/** The node's address in the geo bases, asked for on purpose: it goes to every base. */
export function NodeGeoCheck({ node }: { node: Node }) {
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/nodes/{id}/geo", { params: { path: { id: node.id } } })),
  });
  return (
    <section className="mt-6 border-t border-[var(--hairline)] pt-5" aria-labelledby="geo-title">
      <h3 id="geo-title" className="text-[15px] font-semibold text-[var(--ink-900)]">
        {t("geo.title")}
      </h3>
      <p className="mt-1 text-[13px] text-[var(--ink-600)]">{t("geo.lead")}</p>
      <p className="mt-2 text-xs text-[var(--ink-500)]">{t("geo.caveat")}</p>
      <Button className="mt-3" size="sm" loading={run.isPending} onClick={() => run.mutate()}>
        <MapPin size={16} aria-hidden /> {t("geo.button")}
      </Button>
      <div className="mt-3" aria-live="polite" aria-busy={run.isPending}>
        {run.isPending ? (
          <>
            <p className="sr-only">{t("geo.running")}</p>
            <Skeleton style={{ height: 200, borderRadius: 16 }} />
          </>
        ) : run.isError ? (
          <div className="banner err" role="alert">
            {errorText(run.error)}
          </div>
        ) : run.data ? (
          <GeoView res={run.data} />
        ) : null}
      </div>
      <h3 className="mt-6 text-[15px] font-semibold text-[var(--ink-900)]">{t("geo.services.title")}</h3>
      <p className="mt-1 text-[13px] text-[var(--ink-600)]">{t("geo.services.lead")}</p>
      <RegionsCheck nodeId={node.id} compact />
    </section>
  );
}

/** The sites' own view, from a node picked here: they answer the address that asks. */
function RegionsCard() {
  const nodes = useNodes();
  const list = (nodes.data ?? []).filter((n) => n.enabled);
  const [picked, setPicked] = useState<number | null>(null);
  const id = picked ?? list[0]?.id ?? null;
  return (
    <section className="card glass reveal mt-4" aria-labelledby="diag-regions-title">
      <CardHead title={t("geo.services.title")} sub={t("geo.services.lead")} id="diag-regions-title" />
      {nodes.isPending ? (
        <Skeleton style={{ height: 40, borderRadius: 12 }} />
      ) : list.length === 0 ? (
        <EmptyState icon={Globe2} title={t("geo.services.noNodesTitle")} text={t("geo.services.noNodesText")} />
      ) : (
        <>
          <Field label={t("geo.services.node")} htmlFor="regions-node">
            <Select id="regions-node" className="input max-w-[320px]" value={id ?? ""} onChange={(e) => setPicked(Number(e.target.value))}>
              {list.map((n) => (
                <option key={n.id} value={n.id}>
                  {nodeLabel(n)}
                </option>
              ))}
            </Select>
          </Field>
          <RegionsCheck nodeId={id} />
        </>
      )}
    </section>
  );
}

/** Any server's address in the geo bases, a node's or one to buy. */
function GeoBasesCard() {
  const picks = useServerPicks();
  const [host, setHost] = useState("");
  const check = useMutation({
    mutationFn: (h: string) => unwrap(api.POST("/api/v1/system/geo", { body: { host: h } })),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (host.trim()) check.mutate(host.trim());
  };
  return (
    <section className="card glass reveal" aria-labelledby="diag-geo-title">
      <CardHead title={t("geo.title")} sub={t("geo.lead")} id="diag-geo-title" />
      <form className="flex flex-wrap items-start gap-x-4 gap-y-1" onSubmit={submit} noValidate>
        <Field label={t("diag.reach.host")} htmlFor="geo-host">
          <input
            id="geo-host"
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
        <div className="mt-[26px] mb-4">
          <Button type="submit" variant="primary" loading={check.isPending} disabled={check.isPending || !host.trim()}>
            <MapPin size={16} aria-hidden /> {t("geo.button")}
          </Button>
        </div>
      </form>
      <ServerPicks picks={picks} value={host} onPick={setHost} />
      <p className="mb-4 text-xs text-[var(--ink-500)]">{t("geo.caveat")}</p>
      <div aria-live="polite" aria-busy={check.isPending}>
        {check.isPending ? (
          <>
            <p className="mb-3 text-[13px] text-[var(--ink-600)]">{t("geo.running")}</p>
            <Skeleton style={{ height: 240, borderRadius: 16 }} />
          </>
        ) : check.isError ? (
          <div className="banner err mb-3" role="alert">
            {errorText(check.error)}
          </div>
        ) : check.data ? (
          <GeoView res={check.data} />
        ) : (
          <EmptyState icon={MapPin} title={t("geo.emptyTitle")} text={t("geo.emptyText")} />
        )}
      </div>
    </section>
  );
}

/** The geo bases for any address, and the sites' own view from a node. */
export function GeoTab() {
  return (
    <>
      <GeoBasesCard />
      <RegionsCard />
    </>
  );
}
