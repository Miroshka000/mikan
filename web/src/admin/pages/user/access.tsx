import { Copy, ExternalLink, RefreshCw, Send } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorText, unwrap, type Inbound, type User } from "../../../api/client";
import { qk, useInbounds, useNodes, userActions, useTariffs, useUserMutation } from "../../../api/hooks";
import { Confirm } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, ErrorState, QR, Skeleton } from "../../../components/ui";
import { Switch } from "../../../components/switch";
import { t } from "../../../i18n";
import { useCopy } from "../../../lib/copy";
import { useDraft } from "../../../lib/draft";
import { nodeLabel } from "../../../lib/node-label";
import { safeHref } from "../../../lib/url";
import { serversHas } from "../sale-rules";
import { byNode } from "../servers-tree";
import { Section } from "./section";

export function SubscriptionSection({ u, onReissue }: { u: User; onReissue: () => void }) {
  const copyText = useCopy();
  const copy = () => copyText(u.sub_url, t("common.linkCopied"));
  if (!u.sub_url) {
    return (
      <Section title={t("userDrawer.subscription")}>
        <p className="text-[13px] text-[var(--ink-500)]">{t("userDrawer.noHost")}</p>
        {u.legacy ? <LegacyLink legacy={u.legacy} /> : null}
      </Section>
    );
  }
  return (
    <Section title={t("userDrawer.subscription")}>
      <div className="link-field">
        <span className="mono" title={u.sub_url}>
          {u.sub_url}
        </span>
        <button type="button" className="icon-btn" onClick={copy} aria-label={t("common.copyLink")}>
          <Copy size={18} />
        </button>
      </div>
      <div className="mt-4 grid grid-cols-1 items-start gap-4 sm:grid-cols-[136px_minmax(0,1fr)]">
        <QR value={u.sub_url} />
        <div className="flex flex-col items-start gap-2">
          <p className="mb-1 text-[13px] text-[var(--ink-500)]">{t("userDrawer.sendHint")}</p>
          <a className="btn btn-glass btn-sm" href={safeHref(u.sub_url)} target="_blank" rel="noreferrer noopener">
            {t("userDrawer.subPage")} <ExternalLink size={14} aria-hidden />
          </a>
          <Button size="sm" variant="danger" onClick={onReissue}>
            <RefreshCw size={16} aria-hidden /> {t("userDrawer.reissue")}
          </Button>
        </div>
      </div>
      <HappLink id={u.id} token={u.sub_url} />
      {u.legacy ? <LegacyLink legacy={u.legacy} /> : null}
    </Section>
  );
}

/** Happ's crypt link, when the panel makes them (Settings → Subscription → Happ): what to
 * send instead of the address when the address must stay hidden. Nothing while it loads
 * or when the crypt link is off; token refetches it after a reissue. */
function HappLink({ id, token }: { id: number; token: string }) {
  const copyText = useCopy();
  const q = useQuery({
    queryKey: ["happ-link", id, token],
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/happ-link", { params: { path: { id } }, signal })),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const link = q.data?.link;
  if (!link) return null;
  return (
    <div className="mt-4">
      <div className="mb-1 text-[13px] font-medium">{t("settings.happLink")}</div>
      <div className="link-field">
        <span className="mono" title={link}>
          {link}
        </span>
        <button type="button" className="icon-btn" onClick={() => copyText(link, t("common.linkCopied"))} aria-label={t("common.copyLink")}>
          <Copy size={18} />
        </button>
      </div>
      <p className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.happLinkHint")}</p>
    </div>
  );
}

type Legacy = NonNullable<User["legacy"]>;

const SOURCES = { marzban: "Marzban", pasarguard: "PasarGuard", remnawave: "Remnawave", "3xui": "3x-ui" } as Record<string, string>;

/** The link the user had in the panel they came from. Only Remnawave's and 3x-ui's can be shown whole:
 * Marzban and PasarGuard sign a new token for every request, so the panel keeps none. */
function LegacyLink({ legacy }: { legacy: Legacy }) {
  const copyText = useCopy();
  const source = SOURCES[legacy.source] ?? legacy.source;
  const hint = !legacy.active
    ? t("userDrawer.legacyOff")
    : legacy.url
      ? t("userDrawer.legacyUrl")
      : legacy.source === "remnawave" || legacy.source === "3xui"
        ? t("userDrawer.legacyNoHost")
        : t("userDrawer.legacySigned");
  return (
    <div className="mt-5 border-t border-[var(--hairline)] pt-4">
      <div className="mb-2 text-[13px] font-medium text-[var(--ink-700)]">{t("userDrawer.legacyTitle", { source })}</div>
      {legacy.url ? (
        <div className="link-field mb-2">
          <span className="mono" title={legacy.url}>
            {legacy.url}
          </span>
          <button type="button" className="icon-btn" onClick={() => copyText(legacy.url!, t("common.linkCopied"))} aria-label={t("common.copyLink")}>
            <Copy size={18} />
          </button>
        </div>
      ) : null}
      <p className="text-xs text-[var(--ink-500)]">{hint}</p>
    </div>
  );
}

export function TelegramSection({ u }: { u: User }) {
  const toast = useToast();
  const unlink = useUserMutation((id: number) => unwrap(api.DELETE("/api/v1/users/{id}/telegram", { params: { path: { id } } })));
  const [confirm, setConfirm] = useState(false);
  const tg = u.telegram;
  return (
    <Section title={t("userDrawer.telegram")}>
      {tg ? (
        <div className="panel-soft flex items-center gap-3 p-3">
          <span className="grid h-9 w-9 place-items-center rounded-[10px] bg-[var(--hover)] text-[var(--ink-600)]" aria-hidden>
            <Send size={18} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13px] font-medium">{tg.username ? `@${tg.username}` : tg.name || tg.id}</div>
            {tg.username && tg.name ? <div className="truncate text-xs text-[var(--ink-500)]">{tg.name}</div> : null}
          </div>
          <Button size="sm" variant="danger" onClick={() => setConfirm(true)}>
            {t("userDrawer.telegramUnlink")}
          </Button>
        </div>
      ) : (
        <p className="text-[13px] text-[var(--ink-500)]">{t("userDrawer.telegramNone")}</p>
      )}
      <Confirm
        open={confirm}
        onOpenChange={setConfirm}
        title={t("userDrawer.telegramUnlinkTitle")}
        text={t("userDrawer.telegramUnlinkText")}
        confirm={t("userDrawer.telegramUnlink")}
        danger
        loading={unlink.isPending}
        onConfirm={() =>
          unlink.mutate(u.id, {
            onSuccess: () => {
              setConfirm(false);
              toast.ok(t("userDrawer.telegramUnlinked"));
            },
            onError: (e) => toast.error(errorText(e)),
          })
        }
      />
    </Section>
  );
}

/** The protocols the client may use: the tariff's servers, or a list of the card's own
 * that stays over them until it is reset. A protocol of a server group the client has
 * closed ("not included") is off whatever the list says: the group's mode decides. */
export function ProtocolsSection({ u }: { u: User }) {
  const inbounds = useInbounds();
  const tariffs = useTariffs();
  const nodes = useNodes();
  // The same query as the card's server groups: it is read once for both.
  const groups = useQuery({ queryKey: qk.userPools(u.id), queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/pools", { params: { path: { id: u.id } }, signal })) });
  const update = useUserMutation(userActions.update);
  const toast = useToast();
  const all = (inbounds.data ?? []).filter((i) => i.enabled);
  const tariff = tariffs.data?.find((tr) => tr.id === u.tariff_id);
  const base = new Set(all.filter((i) => !tariff || serversHas(tariff.servers, i)).map((i) => i.id));
  const closed = new Map((groups.data ?? []).filter((g) => g.mode === "closed").map((g) => [g.pool_id, g.name]));
  const closedBy = (i: Inbound) => (i.pool_id != null ? closed.get(i.pool_id) : undefined);
  const shut = new Set(all.filter((i) => closedBy(i) !== undefined).map((i) => i.id));
  const own = u.inbounds.length > 0;
  const allowed = own ? new Set(u.inbounds) : base;
  const send = (ids: number[]) => update.mutate({ id: u.id, body: { inbounds: ids } }, { onError: (e) => toast.error(errorText(e)) });
  const toggle = (id: number, on: boolean) => {
    const next = new Set(allowed);
    if (on) next.add(id);
    else next.delete(id);
    if (![...next].some((x) => !shut.has(x))) {
      toast.error(t("userDrawer.needOneProtocol"));
      return;
    }
    // A list that comes back to the tariff's is no list of its own: the tariff's servers apply again.
    const same = next.size === base.size && [...next].every((x) => base.has(x));
    send(same ? [] : [...next]);
  };
  const tree = byNode(all);
  // Every list counts: without the tariff the base is every protocol, without the groups
  // none is closed, and a switch flipped then would send a list of the wrong ones.
  const failed = [inbounds, tariffs, groups].find((q) => q.data === undefined && q.isError);
  const loading = inbounds.data === undefined || tariffs.data === undefined || groups.data === undefined;
  if (failed || loading) {
    return (
      <Section title={t("userDrawer.protocols")}>
        {failed ? (
          <ErrorState text={errorText(failed.error)} onRetry={() => void failed.refetch()} />
        ) : (
          [0, 1].map((n) => (
            <div key={n} className="flex items-center justify-between gap-3 py-2" aria-hidden>
              <div className="min-w-0 flex-1">
                <Skeleton style={{ width: 140 }} />
                <Skeleton className="mt-1.5" style={{ width: 200 }} />
              </div>
              <Skeleton style={{ width: 36, height: 20, borderRadius: 999 }} />
            </div>
          ))
        )}
      </Section>
    );
  }
  return (
    <Section
      title={t("userDrawer.protocols")}
      aside={
        own ? (
          <button type="button" className="link-btn text-xs" onClick={() => send([])} disabled={update.isPending}>
            {t("servers.asTariff")}
          </button>
        ) : undefined
      }
    >
      {own ? <p className="mb-1 text-xs text-[var(--honey-600)]">{t("servers.notAsTariff")}</p> : null}
      {tree.length === 0 ? <p className="text-[13px] text-[var(--ink-500)]">{t("userDrawer.noProtocols")}</p> : null}
      {tree.map(([nodeId, list]) => {
        const node = nodes.data?.find((n) => n.id === nodeId);
        // With more than one node the switches of one are a group named by it: two nodes
        // of the same preset have switches of the same name.
        const head = tree.length > 1 ? `proto-node-${u.id}-${nodeId}` : undefined;
        return (
          <div key={nodeId} role={head ? "group" : undefined} aria-labelledby={head}>
            {head ? (
              <div id={head} className="pt-2 text-xs font-semibold text-[var(--ink-500)]">
                {node ? nodeLabel(node) : `#${nodeId}`}
              </div>
            ) : null}
            {list.map((i) => {
              const group = closedBy(i);
              return (
                <div key={i.id} className="flex items-center justify-between gap-3 py-2">
                  <div className="min-w-0">
                    <div className="truncate text-[13px] font-medium">{i.sub_name}</div>
                    <div className="text-xs text-[var(--ink-500)]">
                      {i.title} · {i.port}/{i.network}
                      {group !== undefined ? ` · ${t("servers.groupClosed", { name: group })}` : own || base.has(i.id) ? "" : ` · ${t("servers.notInTariff")}`}
                    </div>
                  </div>
                  <Switch
                    checked={allowed.has(i.id) && group === undefined}
                    onChange={(v) => toggle(i.id, v)}
                    label={i.sub_name}
                    disabled={update.isPending || group !== undefined}
                  />
                </div>
              );
            })}
          </div>
        );
      })}
    </Section>
  );
}

export function NoteSection({ u }: { u: User }) {
  // The drawer polls the user every few seconds: a note being typed is not replaced.
  const { draft: note, setDraft: setNote, dirty } = useDraft(u.note);
  const update = useUserMutation(userActions.update);
  const toast = useToast();
  return (
    <Section title={t("userDrawer.note")} aside={update.isPending ? t("userDrawer.saving") : undefined}>
      <textarea
        className="input"
        aria-label={t("userDrawer.noteLabel")}
        placeholder={t("userDrawer.notePlaceholder")}
        maxLength={2000}
        value={note}
        onChange={(e) => setNote(e.target.value)}
        onBlur={() => {
          if (dirty) update.mutate({ id: u.id, body: { note } }, { onSuccess: () => toast.ok(t("userDrawer.noteSaved")), onError: (e) => toast.error(errorText(e)) });
        }}
      />
    </Section>
  );
}
