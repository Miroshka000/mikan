// Connecting the bot: its token, and how the panel reaches Telegram (directly, through a node or a proxy).
import { Link } from "@tanstack/react-router";
import { Globe, Link2, Network, Shield, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { ApiError, errorText, type Schemas } from "../../../api/client";
import { useNodes } from "../../../api/hooks";
import { CardHead } from "../../../components/layout";
import { Confirm } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, Field, Segmented, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { rise, usePatchTelegram, type View } from "./shared";
import { Select } from "../../../components/select";

export function ConnectCard({ v }: { v: View }) {
  const patch = usePatchTelegram();
  const toast = useToast();
  const [token, setToken] = useState("");
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [error, setError] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError("");
    patch.mutate(
      { token: token.trim(), enabled: true },
      {
        onSuccess: (r) => {
          setToken("");
          setEditing(false);
          toast.ok(t("telegram.connected", { name: r.bot?.username ?? "" }));
        },
        onError: (err) => {
          if (err instanceof ApiError && Object.keys(err.fields).length) setError(Object.values(err.fields)[0] ?? "");
          else setError(errorText(err));
        },
      },
    );
  };
  const showForm = !v.token_set || editing;
  return (
    <section {...rise(0)}>
      <CardHead title={t("telegram.connect")} sub={v.token_set && v.bot ? v.bot.name : t("telegram.connectSub")} />
      {showForm ? (
        <form onSubmit={submit} noValidate>
          {!v.token_set ? (
            <ol className="mb-4 flex list-decimal flex-col gap-1 pl-5 text-[13px] text-[var(--ink-600)]">
              <li>{t("telegram.step1")}</li>
              <li>{t("telegram.step2")}</li>
            </ol>
          ) : null}
          <Field label={t("telegram.token")} htmlFor="tg-token" hint={t("telegram.tokenHint")} error={error}>
            <input id="tg-token" className="input mono" type="password" autoComplete="off" spellCheck={false} value={token} onChange={(e) => setToken(e.target.value)} placeholder="123456789:AAH…" aria-invalid={!!error} />
          </Field>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" type="submit" loading={patch.isPending} disabled={!token.trim()}>
              <Link2 size={16} aria-hidden /> {t("telegram.connectButton")}
            </Button>
            {editing ? (
              <Button variant="ghost" onClick={() => setEditing(false)}>
                {t("common.cancel")}
              </Button>
            ) : null}
          </div>
        </form>
      ) : (
        <div className="mt-4 flex flex-wrap gap-2">
          <Button size="sm" onClick={() => setEditing(true)}>
            {t("telegram.changeToken")}
          </Button>
          <Button size="sm" variant="danger" onClick={() => setRemoving(true)}>
            <Trash2 size={16} aria-hidden /> {t("telegram.removeToken")}
          </Button>
        </div>
      )}
      <Confirm
        open={removing}
        onOpenChange={setRemoving}
        title={t("telegram.removeTitle")}
        text={t("telegram.removeText")}
        confirm={t("telegram.removeToken")}
        danger
        loading={patch.isPending}
        onConfirm={() => patch.mutate({ token: "" }, { onSuccess: () => setRemoving(false), onError: (e) => toast.error(errorText(e)) })}
      />
    </section>
  );
}

type RouteMode = Schemas["TelegramRoute"]["mode"];

const ROUTE_ICON = { direct: Globe, node: Network, proxy: Shield } as const;

// A node opens the tunnel to Telegram since 0.4.2; dev builds and nodes not heard from yet
// are given the benefit of the doubt (the check on saving tells).
function tunnels(version?: string): boolean {
  const m = /^(\d+)\.(\d+)\.(\d+)/.exec(version ?? "");
  if (!m) return true;
  const [a, b, c] = [Number(m[1]), Number(m[2]), Number(m[3])];
  return a > 0 || b > 4 || (b === 4 && c >= 2);
}

// How the bot reaches Telegram: straight, through a node of the panel or a proxy — for a
// server where Telegram is blocked.
export function RouteCard({ v }: { v: View }) {
  const patch = usePatchTelegram();
  const toast = useToast();
  const nodes = useNodes();
  const saved = v.route;
  const {
    draft: { mode, nodeId },
    setDraft: setRoute,
  } = useDraft<{ mode: RouteMode; nodeId: number }>({ mode: saved.mode, nodeId: saved.node_id ?? 0 });
  const setMode = (m: RouteMode) => setRoute((d) => ({ ...d, mode: m }));
  const setNodeId = (n: number) => setRoute((d) => ({ ...d, nodeId: n }));
  const [proxy, setProxy] = useState("");
  const [error, setError] = useState("");
  const remote = (nodes.data ?? []).filter((n) => !n.local);
  const nodeName = (id?: number) => remote.find((n) => n.id === id)?.name ?? `#${id}`;
  // Only a node of the list can be chosen. A saved route may still name a node deleted
  // since: React would show the first option for it while the form sent the old id.
  const picked = remote.some((n) => n.id === nodeId && tunnels(n.version)) ? nodeId : 0;
  const now =
    saved.mode === "node"
      ? t("telegram.routeNowNode", { name: nodeName(saved.node_id) })
      : saved.mode === "proxy"
        ? t("telegram.routeNowProxy", { proxy: saved.proxy ?? "" })
        : t("telegram.routeNowDirect");
  const changed = mode !== saved.mode || (mode === "node" && picked !== (saved.node_id ?? 0)) || (mode === "proxy" && proxy.trim() !== "");
  const ready = mode === "direct" || (mode === "node" && picked > 0) || (mode === "proxy" && (proxy.trim() !== "" || !!saved.proxy));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError("");
    const route: Schemas["PatchTelegramInputBody"]["route"] =
      mode === "node" ? { mode, node_id: picked } : mode === "proxy" ? { mode, ...(proxy.trim() ? { proxy: proxy.trim() } : {}) } : { mode };
    patch.mutate(
      { route },
      {
        onSuccess: (r) => {
          setProxy("");
          const how = r.route.mode === "node" ? nodeName(r.route.node_id) : r.route.mode === "proxy" ? (r.route.proxy ?? "") : t("telegram.routeDirectShort");
          toast.ok(t("telegram.routeSaved", { how }));
        },
        onError: (err) => setError(err instanceof ApiError && Object.keys(err.fields).length ? (Object.values(err.fields)[0] ?? "") : errorText(err)),
      },
    );
  };
  const Icon = ROUTE_ICON[saved.mode];
  return (
    <section {...rise(1)}>
      <CardHead title={t("telegram.routeTitle")} sub={t("telegram.routeSub")} />
      <div className="panel-soft mb-4 flex items-center gap-3 p-3">
        <span className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-[var(--hover)] text-[var(--ink-700)]" aria-hidden>
          <Icon size={20} />
        </span>
        <div className="min-w-0">
          <div className="text-xs text-[var(--ink-500)]">{t("telegram.routeNow")}</div>
          <div className="truncate text-[13px] font-semibold">{now}</div>
        </div>
      </div>
      <form onSubmit={submit} noValidate>
        <div className="mb-4">
          <Segmented
            value={mode}
            onChange={(m) => {
              setMode(m);
              setError("");
            }}
            label={t("telegram.routeTitle")}
            options={[
              { value: "direct", label: t("telegram.routeDirect") },
              { value: "node", label: t("telegram.routeNode") },
              { value: "proxy", label: t("telegram.routeProxy") },
            ]}
          />
        </div>
        {mode === "direct" ? <p className="mb-4 text-xs text-[var(--ink-500)]">{t("telegram.routeDirectHint")}</p> : null}
        {mode === "node" ? (
          nodes.isPending ? (
            <Skeleton style={{ height: 40, borderRadius: 12, maxWidth: 320 }} />
          ) : remote.length === 0 ? (
            <div className="banner warn mb-4 flex-wrap" role="status">
              <span className="min-w-0 flex-1">{t("telegram.routeNoNodes")}</span>
              <Link to="/nodes" className="btn btn-glass btn-sm">
                {t("telegram.routeOpenNodes")}
              </Link>
            </div>
          ) : (
            <Field label={t("telegram.routeNodeLabel")} htmlFor="tg-route-node" hint={t("telegram.routeNodeHint")} error={error}>
              <Select id="tg-route-node" className="input max-w-[320px]" value={picked} onChange={(e) => setNodeId(Number(e.target.value))} aria-invalid={!!error}>
                <option value={0} disabled>
                  {t("telegram.routeNodePick")}
                </option>
                {remote.map((n) => (
                  <option key={n.id} value={n.id} disabled={!tunnels(n.version)}>
                    {tunnels(n.version) ? n.name : t("telegram.routeNodeOld", { name: n.name })}
                  </option>
                ))}
              </Select>
            </Field>
          )
        ) : null}
        {mode === "proxy" ? (
          <Field label={t("telegram.routeProxyLabel")} htmlFor="tg-route-proxy" hint={saved.proxy ? t("telegram.routeProxyKeep", { proxy: saved.proxy }) : t("telegram.routeProxyHint")} error={error}>
            <input
              id="tg-route-proxy"
              className="input mono"
              type="text"
              autoComplete="off"
              spellCheck={false}
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
              placeholder="socks5://user:pass@203.0.113.5:1080"
              maxLength={512}
              aria-invalid={!!error}
            />
          </Field>
        ) : null}
        {error && mode === "direct" ? (
          <p className="mb-3 text-xs text-[var(--berry-600)]" role="alert">
            {error}
          </p>
        ) : null}
        {mode === "node" && !nodes.isPending && remote.length === 0 ? null : (
          <div className="form-actions">
            <Button variant="primary" type="submit" loading={patch.isPending} disabled={!changed || !ready}>
              {mode === "direct" ? t("common.save") : t("telegram.routeSave")}
            </Button>
          </div>
        )}
      </form>
    </section>
  );
}
