// The admin's journal (GET /api/v1/audit, kept 180 days): the sign-ins, what support did on
// a grant, and every change, newest first. A support visit whose Telegram notice never
// came is still here.
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ScrollText } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api, unwrap, type Schemas } from "../../../api/client";
import { Drawer } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { Button, EmptyState, Skeleton } from "../../../components/ui";
import { t, tMaybe } from "../../../i18n";
import { pagedItems } from "../../../lib/paging";
import { dateShort, time } from "../../../lib/format";
import { CardHead } from "../../../components/layout";

type Entry = Schemas["AuditEntry"];

const PAGE = 50;

/** The card holds the newest few; the whole log opens beside it, so the page keeps its length. */
const PREVIEW = 5;

/** The action in words: its own text, else its kind's ("Clients"), else the code. */
function actionText(action: string): string {
  return tMaybe(`audit.action.${action}`) ?? tMaybe(`audit.group.${action.split(".")[0]}`) ?? action;
}

/** The lines the admin looks for first: a wrong password, a helper let in. */
const watched = (action: string) => action === "auth.login_failed" || action.startsWith("support.");

/** Who acted (audit.Actor): a support operator on a grant, an addon, the subscriber, an API
 * key, the admin, or the system and the console, which leave both empty. */
function actorText(e: Entry): string {
  const d = e.details && typeof e.details === "object" ? (e.details as Record<string, unknown>) : {};
  const actor = typeof d.actor === "string" ? d.actor : "";
  if (actor.startsWith("support:")) return t("audit.actor.support", { id: actor.slice("support:".length) });
  if (actor.startsWith("addon:")) return t("audit.actor.addon", { id: actor.slice("addon:".length) });
  if (actor === "subscriber") return t("audit.actor.subscriber");
  if (typeof d.api_key === "string") return t("audit.actor.apiKey", { prefix: d.api_key });
  return e.admin_id != null ? t("audit.actor.admin") : t("audit.actor.system");
}

/** The lines after which the client they are about is gone: no card to open. */
const deletesUser = (action: string) => action === "user.delete" || action === "user.remove_inactive";

/** What the line is about: a client opens in their card (unless the line deleted them). */
function target(e: Entry): ReactNode {
  if (!e.target_type) return null;
  if (e.target_type === "user" && /^\d+$/.test(e.target_id) && !deletesUser(e.action)) {
    return (
      <Link to="/users" search={{ state: "all", q: "", user: Number(e.target_id) }} className="link-btn">
        {t("audit.target.user", { id: e.target_id })}
      </Link>
    );
  }
  return tMaybe(`audit.target.${e.target_type}`, { id: e.target_id }) ?? [e.target_type, e.target_id].filter(Boolean).join(" ");
}

export function AuditCard() {
  const [all, setAll] = useState(false);
  const list = useInfiniteQuery({
    queryKey: ["audit"],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) => unwrap(api.GET("/api/v1/audit", { params: { query: { before: pageParam || undefined, limit: PAGE } }, signal })),
    getNextPageParam: (last) => last.next || undefined,
    placeholderData: keepPreviousData,
  });
  return (
    <section className="card glass reveal" aria-labelledby="audit-title" aria-busy={list.isFetching}>
      <CardHead title={t("audit.title")} sub={t("audit.sub")} id="audit-title" />
      <QueryBoundary
        query={list}
        pending={
          <div className="flex flex-col gap-2" role="status" aria-busy aria-label={t("common.loading")}>
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} style={{ height: 44 }} />
            ))}
          </div>
        }
      >
        {(data) => {
          const items = pagedItems(data.pages);
          return items.length === 0 ? (
            <EmptyState icon={ScrollText} title={t("audit.empty")} text={t("audit.emptyText")} />
          ) : (
            <>
              <ul className="row-list">
                {items.slice(0, PREVIEW).map((e) => (
                  <AuditRow key={e.id} e={e} />
                ))}
              </ul>
              {items.length > PREVIEW || list.hasNextPage ? (
                <Button className="mt-3" size="sm" onClick={() => setAll(true)}>
                  <ScrollText size={16} aria-hidden /> {t("audit.all")}
                </Button>
              ) : null}
              <Drawer open={all} onOpenChange={setAll} title={t("audit.title")} meta={t("audit.meta")} wide>
                <ul className="row-list">
                  {items.map((e) => (
                    <AuditRow key={e.id} e={e} />
                  ))}
                </ul>
                {list.hasNextPage ? (
                  <Button className="mt-3" size="sm" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                    {t("payments.more")}
                  </Button>
                ) : null}
              </Drawer>
            </>
          );
        }}
      </QueryBoundary>
    </section>
  );
}

function AuditRow({ e }: { e: Entry }) {
  const about = target(e);
  const parts: ReactNode[] = [actorText(e), about, e.ip ? <span className="mono">{e.ip}</span> : null].filter((x) => x != null && x !== "");
  return (
    <li className="py-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <div className="min-w-0 text-[13px]">
          <span className={watched(e.action) ? "font-semibold text-[var(--honey-600)]" : "font-semibold"}>{actionText(e.action)}</span>{" "}
          <span className="mono text-xs text-[var(--ink-500)]">{e.action}</span>
        </div>
        <time className="num shrink-0 text-xs text-[var(--ink-500)]" dateTime={e.at}>
          {dateShort(e.at)} {time(e.at)}
        </time>
      </div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-xs text-[var(--ink-600)]">
        {parts.map((p, i) => (
          <span key={i} className="inline-flex items-center gap-x-1.5">
            {i ? <span aria-hidden>·</span> : null}
            {p}
          </span>
        ))}
      </div>
      {e.details != null ? (
        <details className="mt-1 text-xs">
          <summary className="cursor-pointer text-[var(--ink-500)]">{t("audit.details")}</summary>
          <pre className="panel-soft mono mt-1 max-h-64 overflow-auto p-2 text-[12px] leading-[1.45] whitespace-pre-wrap break-all">{JSON.stringify(e.details, null, 2)}</pre>
        </details>
      ) : null}
    </li>
  );
}
