// The speed test of a node's own way to the internet: latency, jitter and loss of DNS
// queries, then download and upload against Cloudflare. Run on the admin's word, the
// latest hundred kept.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Gauge } from "lucide-react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, SPEEDTESTS_LIMIT, useSpeedTests } from "../../api/hooks";
import { Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { bits, dateShort, time } from "../../lib/format";

type SpeedTest = Schemas["SpeedTestView"];

const ms = (v: number) => (v < 0 ? "-" : t("speed.ms", { n: v < 10 ? v.toFixed(1) : Math.round(v) }));
const pct = (v: number) => `${v % 1 ? v.toFixed(1) : v}%`;

export function SpeedDrawer({ node, onClose }: { node: { id: number; name: string } | null; onClose: () => void }) {
  const id = node?.id ?? 0;
  const qc = useQueryClient();
  const toast = useToast();
  const history = useSpeedTests(node?.id);
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/nodes/{id}/speedtest", { params: { path: { id } } })),
    onSuccess: (r) => {
      qc.setQueryData<SpeedTest[]>(qk.speedTests(id), (old) => [r, ...(old ?? [])].slice(0, SPEEDTESTS_LIMIT));
      if (r.error) toast.error(t("speed.broke", { error: r.error }));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <Drawer open={!!node} onOpenChange={(v) => !v && onClose()} title={t("speed.title")} meta={node?.name}>
      <div className="pt-5">
        <p className="text-[13px] text-[var(--ink-500)]">{t("speed.intro")}</p>
        <Button className="mt-4" variant="primary" loading={run.isPending} onClick={() => run.mutate()}>
          <Gauge size={16} aria-hidden /> {run.isPending ? t("speed.running") : t("speed.run")}
        </Button>
        <QueryBoundary query={history} pending={<Skeleton className="mt-5" style={{ height: 200, borderRadius: 16 }} />}>
          {(list) =>
            list.length === 0 ? (
              <p className="mt-5 text-[13px] text-[var(--ink-500)]">{t("speed.none")}</p>
            ) : (
              <>
                <Latest r={list[0]!} />
                <h3 className="mt-6 mb-2 text-sm font-semibold">{t("speed.history")}</h3>
                <div className="overflow-x-auto">
                  <table className="w-full text-[13px]">
                    <thead className="text-left text-xs text-[var(--ink-500)]">
                      <tr>
                        <th className="py-1.5 pr-3 font-normal">{t("speed.when")}</th>
                        <th className="py-1.5 pr-3 font-normal">{t("speed.ping")}</th>
                        <th className="py-1.5 pr-3 font-normal">{t("speed.loss")}</th>
                        <th className="py-1.5 pr-3 font-normal">{t("speed.down")}</th>
                        <th className="py-1.5 font-normal">{t("speed.up")}</th>
                      </tr>
                    </thead>
                    <tbody className="num">
                      {list.map((r) => (
                        <tr key={r.id} className="border-t border-[var(--hairline)]" title={r.error || undefined}>
                          <td className="py-1.5 pr-3 whitespace-nowrap">
                            {dateShort(r.at)} {time(r.at)}
                          </td>
                          <td className="py-1.5 pr-3 whitespace-nowrap">{ms(r.ping_ms)}</td>
                          <td className={`py-1.5 pr-3 ${r.loss_pct > 0 ? "text-[var(--berry-600)]" : ""}`}>{pct(r.loss_pct)}</td>
                          <td className="py-1.5 pr-3 whitespace-nowrap">{r.down_bps ? bits(r.down_bps) : "-"}</td>
                          <td className="py-1.5 whitespace-nowrap">{r.up_bps ? bits(r.up_bps) : "-"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </>
            )
          }
        </QueryBoundary>
      </div>
    </Drawer>
  );
}

function Latest({ r }: { r: SpeedTest }) {
  const stats = [
    { label: t("speed.down"), value: r.down_bps ? bits(r.down_bps) : "-" },
    { label: t("speed.up"), value: r.up_bps ? bits(r.up_bps) : "-" },
    { label: t("speed.ping"), value: ms(r.ping_ms) },
    { label: t("speed.jitter"), value: ms(r.jitter_ms) },
    { label: t("speed.loss"), value: pct(r.loss_pct) },
  ];
  return (
    <section className="panel-soft mt-5 rounded-2xl p-4">
      <div className="text-xs text-[var(--ink-500)]">{t("speed.latest", { date: `${dateShort(r.at)} ${time(r.at)}` })}</div>
      <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-3">
        {stats.map((s) => (
          <div key={s.label}>
            <dt className="text-xs text-[var(--ink-500)]">{s.label}</dt>
            <dd className="num text-[15px] font-medium">{s.value}</dd>
          </div>
        ))}
      </dl>
      {r.error ? (
        <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
          {t("speed.broke", { error: r.error })}
        </p>
      ) : null}
    </section>
  );
}
