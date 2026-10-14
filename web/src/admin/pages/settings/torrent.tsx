import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { X } from "lucide-react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk, useTorrent, useTorrentHits } from "../../../api/hooks";
import { QueryBoundary } from "../../../components/query";
import { Switch } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, Pill, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { dateShort, num, time } from "../../../lib/format";

type Hit = Schemas["TorrentHitView"];

// Ban lengths offered, in minutes; 0 only drops what was caught.
const BANS = [0, 15, 60, 360, 1440, 10080];

function banLabel(m: number): string {
  if (m === 0) return t("settings.torrent.banNone");
  if (m % 1440 === 0) return t("settings.torrent.banDays", { n: m / 1440 });
  if (m % 60 === 0) return t("settings.torrent.banHours", { n: m / 60 });
  return t("settings.torrent.banMinutes", { n: m });
}

export function useSaveTorrent() {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: (body: Schemas["PatchTorrentInputBody"]) => unwrap(api.PATCH("/api/v1/torrent", { body })),
    onSuccess: (v) => {
      qc.setQueryData(qk.torrent, v);
      void qc.invalidateQueries({ queryKey: qk.users });
      toast.ok(t("settings.torrent.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
}

// The torrent blocker: the nodes catch BitTorrent, the panel bans on every node.
export function TorrentCard() {
  const q = useTorrent();
  const save = useSaveTorrent();
  return (
    <section className="card glass reveal" style={{ "--i": 3 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.torrent.title")}</h2>
          <div className="card-sub">{t("settings.torrent.sub")}</div>
        </div>
        {q.data ? <Switch checked={q.data.enabled} label={t("settings.torrent.enable")} disabled={save.isPending} onChange={(enabled) => save.mutate({ enabled })} /> : null}
      </div>
      <QueryBoundary query={q} pending={<Skeleton style={{ height: 120 }} />}>
        {(v) => (
          <>
            <p className="text-xs text-[var(--ink-500)]">{t("settings.torrent.enableHint")}</p>
            <label className="mt-4 block text-[13px] font-medium" htmlFor="torrent-ban">
              {t("settings.torrent.ban")}
            </label>
            <select
              id="torrent-ban"
              className="input mt-1.5 max-w-[280px]"
              value={v.ban_minutes}
              disabled={save.isPending}
              onChange={(e) => save.mutate({ ban_minutes: Number(e.target.value) })}
              aria-describedby="torrent-ban-hint"
            >
              {(BANS.includes(v.ban_minutes) ? BANS : [...BANS, v.ban_minutes].sort((a, b) => a - b)).map((m) => (
                <option key={m} value={m}>
                  {banLabel(m)}
                </option>
              ))}
            </select>
            <p id="torrent-ban-hint" className="mt-1.5 text-xs text-[var(--ink-500)]">
              {t("settings.torrent.banHint")}
            </p>
            <div className="mt-4 text-[13px] font-medium">{t("settings.torrent.exempt")}</div>
            {v.exempt.length ? (
              <ul className="mt-2 flex flex-wrap gap-2">
                {v.exempt.map((u) => (
                  <li key={u.id} className="chip-btn flex items-center gap-1">
                    <Link to="/users" search={{ state: "all", q: "", user: u.id }} className="link-btn">
                      {u.name || `#${u.id}`}
                    </Link>
                    <button
                      type="button"
                      className="icon-btn"
                      aria-label={t("settings.torrent.exemptRemove", { name: u.name })}
                      disabled={save.isPending}
                      onClick={() => save.mutate({ exempt: v.exempt.filter((x) => x.id !== u.id).map((x) => x.id) })}
                    >
                      <X size={14} aria-hidden />
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.torrent.exemptNone")}</p>
            )}
          </>
        )}
      </QueryBoundary>
    </section>
  );
}

export function TorrentHitsCard() {
  const list = useTorrentHits();
  const items = list.data?.pages.flat() ?? [];
  return (
    <section className="card glass reveal min-w-0" style={{ "--i": 4 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.torrent.hits")}</h2>
          <div className="card-sub">{t("settings.torrent.hitsSub")}</div>
        </div>
      </div>
      <QueryBoundary
        query={list}
        pending={
          <div className="flex flex-col gap-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} style={{ height: 48 }} />
            ))}
          </div>
        }
      >
        {() =>
          items.length === 0 ? (
            <p className="text-[13px] text-[var(--ink-500)]">{t("settings.torrent.hitsEmpty")}</p>
          ) : (
            <>
              <ul className="row-list" aria-busy={list.isFetching}>
                {items.map((h) => (
                  <HitRow key={h.id} h={h} showUser />
                ))}
              </ul>
              {list.hasNextPage ? (
                <Button className="mt-3" size="sm" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  {t("settings.torrent.more")}
                </Button>
              ) : null}
            </>
          )
        }
      </QueryBoundary>
    </section>
  );
}

export function HitRow({ h, showUser }: { h: Hit; showUser?: boolean }) {
  const banned = h.banned_until && !h.lifted_at && new Date(h.banned_until).getTime() > Date.now();
  return (
    <li className="py-2.5">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        {showUser ? (
          <Link to="/users" search={{ state: "all", q: "", user: h.user_id }} className="link-btn font-medium">
            {h.user_name || `#${h.user_id}`}
          </Link>
        ) : null}
        <span>{t(`settings.torrent.kinds.${h.kind}`)}</span>
        <span className="mono truncate text-xs text-[var(--ink-500)]">{h.dest}</span>
        {h.hits > 1 ? <span className="num text-xs text-[var(--ink-500)]">×{num(h.hits)}</span> : null}
        {h.lifted_at ? (
          <Pill tone="off">{t("settings.torrent.lifted")}</Pill>
        ) : h.banned_until ? (
          <Pill tone={banned ? "bad" : "off"}>{t("settings.torrent.bannedUntil", { date: `${dateShort(h.banned_until)} ${time(h.banned_until)}` })}</Pill>
        ) : null}
      </div>
      <div className="mt-0.5 text-xs text-[var(--ink-500)]">
        {dateShort(h.at)} {time(h.at)}
        {h.node_name ? ` · ${h.node_name}` : ""} · {h.inbound} · <span className="mono">{h.ip}</span>
      </div>
    </li>
  );
}
