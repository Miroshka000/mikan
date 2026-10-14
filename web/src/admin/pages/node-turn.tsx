// The VK-call way in ("WL"): the node runs the vk-turn-proxy server there, and one of its
// inbounds is listed a second time in the subscription at the client's own address — for
// networks where a whitelist lets only calls through. The server's own settings and the
// VK call links live here; the client program next to the user's apps joins the call and
// carries the traffic to this node.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useInbounds } from "../../api/hooks";
import { Confirm, Drawer } from "../../components/overlay";
import { useToast } from "../../components/toast";
import { QueryBoundary } from "../../components/query";
import { Button, Field, Pill, Skeleton } from "../../components/ui";
import { SwitchRow } from "../../components/switch";
import { t, tMaybe } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { useDraft } from "../../lib/draft";
import { fieldErrors } from "../../lib/fields";
import { ago } from "../../lib/format";

type Turn = Schemas["TurnView"];

export function TurnDrawer({ node, onClose }: { node: { id: number; name: string } | null; onClose: () => void }) {
  const q = useQuery({
    queryKey: qk.turn(node?.id ?? 0),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/nodes/{id}/turn", { params: { path: { id: node!.id } }, signal })),
    enabled: !!node,
  });
  return (
    <Drawer open={!!node} onOpenChange={(v) => !v && onClose()} title={t("turn.title")} meta={node?.name}>
      <div className="pt-5">
        <p className="mb-4 text-[13px] text-[var(--ink-600)]">{t("turn.intro")}</p>
        <QueryBoundary query={q} pending={<Skeleton style={{ height: 260, borderRadius: 16 }} />}>
          {(v) => <Body key={node!.id} nodeId={node!.id} v={v} refetch={() => void q.refetch()} checking={q.isFetching} onClose={onClose} />}
        </QueryBoundary>
      </div>
    </Drawer>
  );
}

function Body({ nodeId, v, refetch, checking, onClose }: { nodeId: number; v: Turn; refetch: () => void; checking: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const copy = useCopy();
  const inbounds = useInbounds();
  const [remove, setRemove] = useState(false);
  // Only this node's inbounds can be served: the server hands traffic to a port of it.
  const mine = (inbounds.data ?? []).filter((i) => i.node_id === nodeId && i.enabled);
  // The status check refetches `v`: the typed fields survive it.
  const { draft, setDraft, dirty } = useDraft({
    enabled: v.enabled || !v.configured,
    inbound_id: v.inbound_id ?? 0,
    listen_port: v.listen_port || "56000",
    client_host: v.client_host || "127.0.0.1",
    client_port: v.client_port || "9000",
    streams: v.streams || 10,
    udp: v.udp ?? true,
    title: v.title ?? "",
    links: (v.links ?? []).join("\n"),
    binary_path: v.binary_path ?? "",
  });
  const set = <K extends keyof typeof draft>(key: K, value: (typeof draft)[K]) => setDraft((d) => ({ ...d, [key]: value }));
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH("/api/v1/nodes/{id}/turn", {
          params: { path: { id: nodeId } },
          body: {
            enabled: draft.enabled,
            inbound_id: draft.inbound_id || undefined,
            listen_port: draft.listen_port.trim(),
            client_host: draft.client_host.trim(),
            client_port: draft.client_port.trim(),
            streams: Number(draft.streams) || 10,
            udp: draft.udp,
            title: draft.title.trim(),
            links: draft.links.split(/\n/).map((s) => s.trim()).filter(Boolean),
            binary_path: draft.binary_path.trim(),
          },
        }),
      ),
    onSuccess: (nv) => {
      qc.setQueryData(qk.turn(nodeId), nv);
      toast.ok(t("turn.saved"));
    },
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/nodes/{id}/turn", { params: { path: { id: nodeId } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.turn(nodeId) });
      setRemove(false);
      toast.ok(t("turn.deleted"));
      onClose();
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const fields = fieldErrors(save.error);
  const s = v.status;
  const links = v.links ?? [];
  // The panel could not ask, or the node is older than the server: that says nothing
  // about the setup itself, and an old node is told to be updated.
  const blind = !s || s.error === "node_unreachable" || s.error === "node_too_old";
  return (
    <>
      <div className="panel-soft mb-4 p-3" role="status">
        <div className="flex items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2 text-[13px] font-semibold">
            {t("turn.statusTitle")}
            {!v.configured ? (
              <Pill tone="off">{t("turn.notSet")}</Pill>
            ) : !draft.enabled ? (
              <Pill tone="off">{t("turn.off")}</Pill>
            ) : !s ? (
              <Pill tone="off">{t("turn.unknown")}</Pill>
            ) : s.error === "node_too_old" ? (
              <Pill tone="off">{t("turn.unknown")}</Pill>
            ) : s.running ? (
              <Pill tone="ok">{t("turn.works")}</Pill>
            ) : blind ? (
              <Pill tone="off">{t("turn.unknown")}</Pill>
            ) : (
              <Pill tone="bad">{t("turn.down")}</Pill>
            )}
          </div>
          <Button size="sm" variant="ghost" loading={checking} onClick={refetch} aria-label={t("turn.check")}>
            <RefreshCw size={14} aria-hidden /> {t("turn.check")}
          </Button>
        </div>
        {v.configured && draft.enabled ? (
          <div className="mt-2 text-xs text-[var(--ink-500)]">
            {s?.error === "node_too_old"
              ? t("turn.nodeTooOld")
              : s && !s.error
                ? `${t("turn.runningSince", { pid: String(s.pid ?? 0) })} · ${ago(s.checked_at)}${s.restarts ? ` · ${t("turn.restarts", { n: String(s.restarts) })}` : ""}`
                : s?.error === "node_unreachable"
                  ? t("turn.nodeUnreachable")
                  : t("turn.notRunning")}
          </div>
        ) : null}
        {s?.error && !blind ? (
          <div className="mt-2">
            <p className="text-[13px] text-[var(--ink-700)]">
              {tMaybe(`turn.why.${s.error}`, {}) ?? t("turn.whyUnknown", { code: s.error })}
            </p>
            {s.detail ? <p className="mono mt-1 break-words text-xs text-[var(--ink-500)]">{s.detail}</p> : null}
          </div>
        ) : null}
        {s?.log?.length && !s.running ? (
          <pre className="mono mt-2 max-h-32 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-[var(--wash)] p-2 text-[11px] text-[var(--ink-500)]">
            {s.log.slice(-6).join("\n")}
          </pre>
        ) : null}
      </div>

      {v.configured && v.entry ? (
        <div className="panel-soft mb-4 p-3">
          <div className="text-[13px] font-semibold">{t("turn.entry")}</div>
          <div className="mt-1 text-xs text-[var(--ink-500)]">{t("turn.entryText", { name: v.entry ?? "" })}</div>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <code className="mono rounded bg-[var(--wash)] px-2 py-1 text-[11px]">
              {v.peer ?? ""} · {v.client_host}:{v.client_port} · {v.mode === "udp" ? t("turn.mode.udp") : t("turn.mode.vless")}
            </code>
            <Button size="sm" variant="ghost" onClick={() => void copy(`${v.peer ?? ""}`, t("common.linkCopied"))}>
              <Copy size={14} aria-hidden /> {t("turn.copyPeer")}
            </Button>
            {links[0] ? (
              <Button size="sm" variant="ghost" onClick={() => void copy(links[0] ?? "", t("common.linkCopied"))}>
                <Copy size={14} aria-hidden /> {t("turn.copyLink")}
              </Button>
            ) : null}
          </div>
        </div>
      ) : null}

      {save.error && !Object.keys(fields).length ? <div className="banner err mb-4">{errorText(save.error)}</div> : null}

      <SwitchRow label={t("turn.enabled")} sub={t("turn.enabledSub")} checked={draft.enabled} onChange={(x) => set("enabled", x)} />

      <Field label={t("turn.inbound")} htmlFor="turn-inbound" hint={t("turn.inboundHint")} error={fields.inbound_id}>
        {mine.length === 0 ? (
          <p className="text-xs text-[var(--ink-500)]">{t("turn.inboundNone")}</p>
        ) : (
          <select id="turn-inbound" className="input max-w-[360px]" value={draft.inbound_id} onChange={(e) => set("inbound_id", Number(e.target.value))}>
            <option value={0} disabled>
              {t("turn.inboundPick")}
            </option>
            {mine.map((i) => (
              <option key={i.id} value={i.id}>
                {i.title || i.name} · {i.type} · {i.port}
              </option>
            ))}
          </select>
        )}
      </Field>

      <div className="grid grid-cols-2 gap-3">
        <Field label={t("turn.listenPort")} htmlFor="turn-listen" error={fields.listen_port}>
          <input id="turn-listen" className="input mono" value={draft.listen_port} onChange={(e) => set("listen_port", e.target.value)} inputMode="numeric" />
        </Field>
        <Field label={t("turn.streams")} htmlFor="turn-streams" error={fields.streams}>
          <input id="turn-streams" className="input mono" value={String(draft.streams)} onChange={(e) => set("streams", Number(e.target.value))} inputMode="numeric" />
        </Field>
        <Field label={t("turn.clientHost")} htmlFor="turn-chost" error={fields.client_host}>
          <input id="turn-chost" className="input mono" value={draft.client_host} onChange={(e) => set("client_host", e.target.value)} spellCheck={false} />
        </Field>
        <Field label={t("turn.clientPort")} htmlFor="turn-cport" error={fields.client_port}>
          <input id="turn-cport" className="input mono" value={draft.client_port} onChange={(e) => set("client_port", e.target.value)} inputMode="numeric" />
        </Field>
      </div>
      <p className="-mt-1 mb-3 text-xs text-[var(--ink-500)]">{t("turn.clientHint")}</p>

      <SwitchRow label={t("turn.udp")} sub={t("turn.udpHint")} checked={draft.udp} onChange={(x) => set("udp", x)} />

      <Field label={t("turn.entryName")} htmlFor="turn-name" hint={t("turn.entryNameHint")} error={fields.title}>
        <input id="turn-name" className="input max-w-[360px]" value={draft.title} onChange={(e) => set("title", e.target.value)} maxLength={64} />
      </Field>

      <Field label={t("turn.links")} htmlFor="turn-links" hint={t("turn.linksHint")} error={fields.links}>
        <textarea
          id="turn-links"
          className="input mono min-h-[90px] py-2 text-xs"
          value={draft.links}
          onChange={(e) => set("links", e.target.value)}
          placeholder={"https://vk.com/call/join/…\nhttps://vk.com/call/join/…"}
          spellCheck={false}
          aria-invalid={!!fields.links}
        />
      </Field>

      <Field label={t("turn.binary")} htmlFor="turn-bin" hint={t("turn.binaryHint")} error={fields.binary_path}>
        <input id="turn-bin" className="input mono max-w-[360px]" value={draft.binary_path} onChange={(e) => set("binary_path", e.target.value)} placeholder="/usr/local/bin/vk-turn-proxy" spellCheck={false} />
      </Field>

      <div className="form-actions">
        <Button variant="primary" loading={save.isPending} disabled={!dirty || (!v.configured && !draft.inbound_id)} onClick={() => save.mutate()}>
          {t("common.save")}
        </Button>
        {v.configured ? (
          <Button variant="ghost" onClick={() => setRemove(true)}>
            <Trash2 size={16} aria-hidden /> {t("turn.delete")}
          </Button>
        ) : null}
      </div>

      <Confirm
        open={remove}
        onOpenChange={setRemove}
        title={t("turn.deleteTitle")}
        text={t("turn.deleteText")}
        confirm={t("turn.delete")}
        danger
        loading={del.isPending}
        onConfirm={() => del.mutate()}
      />
    </>
  );
}
