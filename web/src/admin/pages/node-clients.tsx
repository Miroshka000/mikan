// Who goes through one node: each client's last time there, its traffic on the node and
// what of its keys is open now, the latest seen first, a page at a time. The panel counts it
// by user and node from the version that learned to; what was carried before is not there.
import { Link } from "@tanstack/react-router";
import { Search, UsersRound } from "lucide-react";
import { useState } from "react";
import { useNodeClients } from "../../api/hooks";
import { useSupportMode } from "../../api/support";
import { Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Button, EmptyState, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useDebounced } from "../../lib/debounce";
import { ago, bytes, maskIP } from "../../lib/format";

export function NodeClientsDrawer({ node, onClose }: { node: { id: number; name: string } | null; onClose: () => void }) {
  return (
    <Drawer open={!!node} onOpenChange={(v) => !v && onClose()} title={t("nodeClients.title")} meta={node?.name} wide>
      {/* A new node starts with an empty search. */}
      {node ? <ClientsBody key={node.id} id={node.id} /> : null}
    </Drawer>
  );
}

function ClientsBody({ id }: { id: number }) {
  const operator = useSupportMode();
  // An operator the admin did not let see the subscribers may not search them (the API says 403).
  const canSearch = !operator || operator.show_people;
  const [text, setText] = useState("");
  const q = useDebounced(text.trim());
  const list = useNodeClients(id, canSearch ? q : "");
  const first = list.data?.pages[0];
  return (
    <div className="pt-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        {canSearch ? (
          <label className="search-field min-w-[min(100%,240px)] flex-1">
            <Search size={16} aria-hidden />
            <input type="search" placeholder={t("nodeClients.search")} value={text} onChange={(e) => setText(e.target.value)} aria-label={t("nodeClients.search")} />
          </label>
        ) : null}
        {first ? (
          <span className="num text-[13px] text-[var(--ink-600)]">
            {t("nodeClients.summary", { total: first.total, online: first.online })}
          </span>
        ) : null}
      </div>
      <div className="mt-4" aria-busy={list.isFetching}>
        <QueryBoundary
          query={list}
          pending={
            <div className="flex flex-col gap-2" role="status" aria-busy aria-label={t("common.loading")}>
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} style={{ height: 52, borderRadius: 16 }} />
              ))}
            </div>
          }
        >
          {(data) => {
            const items = [...new Map(data.pages.flatMap((p) => p.items).map((c) => [c.user_id, c])).values()];
            if (items.length === 0) {
              return <EmptyState icon={UsersRound} search={!!q} title={q ? t("nodeClients.noneFound") : t("nodeClients.empty")} text={t("nodeClients.note")} />;
            }
            return (
              <>
                <ul className="flex flex-col gap-2" aria-label={t("nodeClients.title")}>
                  {items.map((c) => {
                    const ips = c.online.flatMap((o) => o.online_ips);
                    const devices = c.online.length;
                    return (
                      <li key={c.user_id} className="panel-soft grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 p-3">
                        <div className="min-w-0">
                          <Link to="/users" search={{ state: "all", q: "", user: c.user_id }} className="link-btn truncate text-[13px] font-medium">
                            {c.user_name || `#${c.user_id}`}
                          </Link>
                          <div className="truncate text-xs text-[var(--ink-500)]">
                            {devices > 0 ? (
                              <>
                                <span className="text-[var(--leaf-700)]">{t("userDrawer.liveTitle")}</span>
                                {" · "}
                                {t("nodeClients.keys", { n: devices })}
                                {ips.length > 0 ? (
                                  <>
                                    {" · "}
                                    <span className="mono">{[...new Set(ips)].map(maskIP).join(", ")}</span>
                                  </>
                                ) : null}
                              </>
                            ) : (
                              t("nodeClients.seen", { when: ago(c.last_seen) })
                            )}
                          </div>
                        </div>
                        <div className="num text-right text-[13px]">
                          {bytes(c.up + c.down)}
                          <div className="text-xs text-[var(--ink-500)]">
                            ↓ {bytes(c.down)} · ↑ {bytes(c.up)}
                          </div>
                        </div>
                      </li>
                    );
                  })}
                </ul>
                {list.hasNextPage ? (
                  <Button className="mt-3" size="sm" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                    {t("payments.more")}
                  </Button>
                ) : null}
                <p className="mt-3 text-xs text-[var(--ink-500)]">{t("nodeClients.note")}</p>
              </>
            );
          }}
        </QueryBoundary>
      </div>
    </div>
  );
}
