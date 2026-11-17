// The log of an address: what was sent to it and how each delivery went, newest first.
import { useInfiniteQuery } from "@tanstack/react-query";
import { Send } from "lucide-react";
import { useEffect, useState } from "react";
import { api, unwrap } from "../../../api/client";
import { Drawer } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { Select } from "../../../components/select";
import { Button, EmptyState, Field, Pill, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { dateTime } from "../../../lib/format";
import { pagedItems } from "../../../lib/paging";
import { DELIVERY_TONE, eventLabel, failureText, webhookKeys, type Delivery, type Webhook } from "./common";

const PAGE = 50;
const STATUSES = ["delivered", "retrying", "failed"] as const;
type Filter = "" | (typeof STATUSES)[number];

export function LogDrawer({ target, onClose }: { target: Webhook | null; onClose: () => void }) {
  const [status, setStatus] = useState<Filter>("");
  const id = target?.id ?? 0;
  useEffect(() => setStatus(""), [id]);
  const log = useInfiniteQuery({
    queryKey: webhookKeys.log(id, status),
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      unwrap(api.GET("/api/v1/webhooks/{id}/deliveries", { params: { path: { id }, query: { before: pageParam || undefined, limit: PAGE, status: status || undefined } }, signal })),
    getNextPageParam: (last) => last.next || undefined,
    enabled: !!target,
    // A new status filter keeps the rows while it loads; another address never shows the
    // last one's deliveries.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[2] === id ? prev : undefined),
    // A retry or a new event shows by itself while the log is open.
    refetchInterval: 10_000,
  });
  return (
    <Drawer open={!!target} onOpenChange={(v) => !v && onClose()} title={t("webhooks.logTitle", { name: target?.name ?? "" })} meta={t("webhooks.logSub")} wide>
      <div className="pt-5" aria-busy={log.isFetching}>
        <Field label={t("webhooks.filter")} htmlFor="wh-log-status">
          <Select id="wh-log-status" className="input max-w-[240px]" value={status} onChange={(e) => setStatus(e.target.value as Filter)}>
            <option value="">{t("webhooks.filterAll")}</option>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {t(`webhooks.status.${s}`)}
              </option>
            ))}
          </Select>
        </Field>
        <QueryBoundary
          query={log}
          pending={
            <div className="flex flex-col gap-2" role="status" aria-busy aria-label={t("common.loading")}>
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} style={{ height: 52 }} />
              ))}
            </div>
          }
        >
          {(data) => {
            const items = pagedItems(data.pages);
            if (items.length === 0) {
              return status ? <p className="py-6 text-center text-[13px] text-[var(--ink-500)]">{t("webhooks.logNone")}</p> : <EmptyState icon={Send} title={t("webhooks.logEmpty")} text={t("webhooks.logEmptyText")} />;
            }
            return (
              <>
                <ul className="row-list">
                  {items.map((d) => (
                    <DeliveryRow key={d.id} d={d} />
                  ))}
                </ul>
                {log.hasNextPage ? (
                  <div className="mt-4 flex justify-center">
                    <Button loading={log.isFetchingNextPage} onClick={() => void log.fetchNextPage()}>
                      {t("webhooks.more")}
                    </Button>
                  </div>
                ) : null}
              </>
            );
          }}
        </QueryBoundary>
      </div>
    </Drawer>
  );
}

/** One event and how it went: what it was, whether the receiver took it, and why not. */
function DeliveryRow({ d }: { d: Delivery }) {
  const test = d.event_id == null;
  const answer = d.status === "delivered" ? (d.response_code > 0 ? t("webhooks.http", { code: d.response_code }) : "") : failureText(d.error, d.response_code);
  return (
    <li className="py-3">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <Pill tone={DELIVERY_TONE[d.status]}>{t(`webhooks.status.${d.status}`)}</Pill>
        <span className="text-[13px] font-medium">{test ? t("webhooks.testEvent") : eventLabel(d.type)}</span>
        {test ? null : <span className="mono text-xs text-[var(--ink-500)]">{d.type}</span>}
      </div>
      <div className="mt-1 text-xs text-[var(--ink-500)]">
        {[test ? null : t("webhooks.eventNo", { id: d.event_id! }), dateTime(d.updated_at), t("webhooks.attempts", { n: d.attempts }), answer].filter(Boolean).join(" · ")}
      </div>
    </li>
  );
}
