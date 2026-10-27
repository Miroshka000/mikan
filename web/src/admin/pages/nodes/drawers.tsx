// Adding a node, its key and the command to join it, and the form of a node's settings.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Copy, KeyRound } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, unwrap, type Schemas } from "../../../api/client";
import { qk, useSpeedTests } from "../../../api/hooks";
import { Drawer, DrawerFooter } from "../../../components/overlay";
import { Switch, SwitchRow } from "../../../components/switch";
import { MAX_TAGS, parseTags } from "../../../components/tags";
import { useToast } from "../../../components/toast";
import { Button, Field, Segmented } from "../../../components/ui";
import { t } from "../../../i18n";
import { useCopy } from "../../../lib/copy";
import { onFormError } from "../../../lib/fields";
import { nodeLabel } from "../../../lib/node-label";
import { JoinProgress, type FixActions } from "../node-check";
import type { Node } from "../nodes";
import { BalanceFields, balanceKey, useBalance } from "../nodes-balance";
import { billingBody, BillingFields, billingForm, type BillingForm } from "./billing";

export type Joined = { id: number; name: string; key: string; command: string };

const NEW_NODE = { name: "", host: "", domain: "", api_port: "" };

export function AddNodeDrawer({ open, onOpenChange, onJoined }: { open: boolean; onOpenChange: (v: boolean) => void; onJoined: (j: Joined) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [form, setForm] = useState(NEW_NODE);
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => {
    if (open) {
      setForm(NEW_NODE);
      setErrors({});
    }
  }, [open]);
  const create = useMutation({
    mutationFn: (body: Schemas["CreateNodeInputBody"]) => unwrap(api.POST("/api/v1/nodes", { body })),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: qk.nodes });
      void qc.invalidateQueries({ queryKey: qk.inbounds });
      onJoined({ id: r.node.id, name: nodeLabel(r.node), key: r.key, command: r.command });
    },
    onError: (e) => {
      onFormError(e, toast, setErrors);
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const port = form.api_port.trim();
    if (port && !/^\d{1,5}$/.test(port)) {
      setErrors({ api_port: t("errors.api.bad_port") });
      return;
    }
    create.mutate({ name: form.name.trim(), host: form.host.trim(), domain: form.domain.trim() || undefined, api_port: port ? Number(port) : undefined });
  };
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => {
    setForm((f) => ({ ...f, [k]: e.target.value }));
    setErrors(({ [k]: _, ...rest }) => rest);
  };
  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      title={t("nodes.newTitle")}
      meta={t("nodes.newMeta")}
      footer={
        <DrawerFooter form="add-node" onCancel={() => onOpenChange(false)} loading={create.isPending}>
          {t("nodes.addSubmit")}
        </DrawerFooter>
      }
    >
      <form id="add-node" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("nodes.name")} htmlFor="n-name" hint={t("nodes.nameHint")} error={errors.name}>
          <input id="n-name" className="input" value={form.name} onChange={set("name")} placeholder={t("nodes.namePlaceholderNew")} maxLength={48} autoComplete="off" aria-invalid={!!errors.name} />
        </Field>
        <Field label={t("nodes.host")} htmlFor="n-host" hint={t("nodes.hostHint")} error={errors.host}>
          <input id="n-host" className="input mono" value={form.host} onChange={set("host")} placeholder="203.0.113.10" autoComplete="off" aria-invalid={!!errors.host} />
        </Field>
        <Field label={t("nodes.domain")} htmlFor="n-domain" hint={t("nodes.domainHint")} error={errors.domain}>
          <input id="n-domain" className="input mono" value={form.domain} onChange={set("domain")} placeholder="us.example.com" autoComplete="off" aria-invalid={!!errors.domain} />
        </Field>
        <Field label={t("nodes.apiPort")} htmlFor="n-port" hint={t("nodes.apiPortHint")} error={errors.api_port}>
          <input id="n-port" className="input max-w-[160px]" inputMode="numeric" value={form.api_port} onChange={set("api_port")} placeholder={t("nodes.apiPortAuto")} aria-invalid={!!errors.api_port} />
        </Field>
      </form>
    </Drawer>
  );
}

/** The join key is shown once: the panel keeps only its fingerprint. */
export function KeyDrawer({ joined, onClose, actions }: { joined: Joined | null; onClose: () => void; actions: FixActions }) {
  const copyText = useCopy();
  const copy = () => joined && copyText(joined.command, t("nodes.commandCopied"));
  return (
    <Drawer
      open={!!joined}
      onOpenChange={(v) => !v && onClose()}
      title={t("nodes.keyTitle")}
      meta={joined?.name}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t("nodes.keyDone")}
        </Button>
      }
    >
      <div className="pt-5">
        <div className="banner warn mb-4">
          <KeyRound size={18} className="shrink-0" aria-hidden />
          <span>{t("nodes.keyOnce")}</span>
        </div>
        <ol className="mb-4 list-decimal space-y-2 pl-5 text-[13px]">
          <li>{t("nodes.keyStep1")}</li>
          <li>{t("nodes.keyStep2")}</li>
        </ol>
        <div className="link-field">
          <span className="mono break-all text-xs">{joined?.command}</span>
          <button type="button" className="icon-btn" onClick={() => void copy()} aria-label={t("nodes.copyCommand")}>
            <Copy size={18} />
          </button>
        </div>
        {joined ? <JoinProgress id={joined.id} actions={actions} /> : null}
      </div>
    </Drawer>
  );
}

export function EditNodeDrawer({ node, onClose }: { node: Node | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [form, setForm] = useState({ name: "", public_name: "", host: "", domain: "", enabled: true, fair_share: false, channel: "", group: "", capacity: "", http_redirect: false, sni_router: false, front_page: "", in_auto: true, api_lockdown: false, api_allow: "", tags: "" });
  const balance = useBalance();
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [billing, setBilling] = useState<BillingForm | null>(null);
  useEffect(() => {
    if (node) {
      setBilling(billingForm(node.billing));
      // The server as it was given, not the address for clients (its domain when it has one):
      // the field saves the server.
      setForm({ name: node.name, public_name: node.public_name, host: node.server_host, domain: node.domain, enabled: node.enabled, fair_share: node.fair_share, channel: node.channel_mbps != null ? String(node.channel_mbps) : "",
        group: node.balance_group, capacity: node.capacity != null ? String(node.capacity) : "", http_redirect: node.http_redirect, sni_router: node.sni_router, front_page: node.front_page, in_auto: node.in_auto, api_lockdown: node.api_lockdown, api_allow: node.api_allow.join(", "), tags: node.tags.join(", ") });
      setErrors({});
    }
  }, [node]);
  // The last speed test of the node, to fill the channel in with one click.
  const tests = useSpeedTests(node?.id);
  const measured = tests.data?.[0]?.down_bps ? Math.floor(tests.data[0].down_bps / 1_000_000) : 0;
  const save = useMutation({
    mutationFn: async ({ id, body, balance }: { id: number; body: Schemas["PatchNodeInputBody"]; balance?: Schemas["SetNodeBalanceInputBody"] }) => {
      const n = await unwrap(api.PATCH("/api/v1/nodes/{id}", { params: { path: { id } }, body }));
      return balance ? unwrap(api.PUT("/api/v1/nodes/{id}/balance", { params: { path: { id } }, body: balance })) : n;
    },
    onSuccess: () => {
      toast.ok(t("nodes.saved"));
      onClose();
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.nodes });
      void qc.invalidateQueries({ queryKey: qk.inbounds });
      void qc.invalidateQueries({ queryKey: balanceKey });
    },
    onError: (e) => {
      onFormError(e, toast, setErrors);
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!node) return;
    const body: Schemas["PatchNodeInputBody"] = { public_name: form.public_name.trim(), enabled: form.enabled, in_auto: form.in_auto, fair_share: form.fair_share };
    // A node without a name of its own (the panel's, shown as "Own node") keeps none: an empty
    // field is no new name, and saving the rest must not fail on it.
    if (form.name.trim() || node.name) body.name = form.name.trim();
    const tags = parseTags(form.tags);
    if (tags.length > MAX_TAGS) {
      setErrors({ tags: t("tags.many", { n: MAX_TAGS }) });
      return;
    }
    body.tags = tags;
    const channel = Number(form.channel);
    if (form.channel.trim() || form.fair_share) {
      if (!Number.isInteger(channel) || channel < 1 || channel > 100000) {
        setErrors({ channel_mbps: t("nodes.channelErr") });
        return;
      }
      body.channel_mbps = channel;
    }
    if (!node.local) {
      body.host = form.host.trim();
      body.domain = form.domain.trim();
      body.http_redirect = form.http_redirect;
      // The addresses are checked by the panel; here only how many there are.
      const allow = form.api_allow.split(/[\s,;]+/).filter(Boolean);
      if (allow.length > 16) {
        setErrors({ api_allow: t("nodes.apiAllowMany") });
        return;
      }
      body.api_lockdown = form.api_lockdown;
      body.api_allow = allow;
    }
    body.sni_router = form.sni_router;
    if (billing) {
      const b = billingBody(billing);
      if ("errors" in b) {
        setErrors(b.errors);
        return;
      }
      body.billing = b.body;
    }
    body.front_page = form.front_page as NonNullable<Schemas["PatchNodeInputBody"]["front_page"]>;
    const group = form.group.trim();
    const capacity = group && form.capacity.trim() ? Number(form.capacity) : undefined;
    if (capacity !== undefined && (!Number.isInteger(capacity) || capacity < 1)) {
      setErrors({ group: t("balance.capacityErr") });
      return;
    }
    const balanceChanged = group !== node.balance_group || (capacity ?? null) !== (node.capacity ?? null);
    save.mutate({ id: node.id, body, balance: balanceChanged ? { group, capacity } : undefined });
  };
  const set = (k: "name" | "public_name" | "host" | "domain" | "tags") => (e: React.ChangeEvent<HTMLInputElement>) => {
    setForm((f) => ({ ...f, [k]: e.target.value }));
    setErrors(({ [k]: _, ...rest }) => rest);
  };
  return (
    <Drawer
      open={!!node}
      onOpenChange={(v) => !v && onClose()}
      title={node ? nodeLabel(node) : ""}
      meta={node?.local ? t("nodes.kindLocal") : t("nodes.kindRemote")}
      footer={
        <DrawerFooter form="edit-node" onCancel={onClose} loading={save.isPending}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form id="edit-node" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("nodes.name")} htmlFor="e-name" hint={t("nodes.nameHint")} error={errors.name}>
          <input id="e-name" className="input" value={form.name} onChange={set("name")} placeholder={t("nodes.namePlaceholderEdit")} maxLength={48} autoComplete="off" aria-invalid={!!errors.name} />
        </Field>
        <Field label={t("nodes.publicName")} htmlFor="e-public-name" hint={t("nodes.publicNameHint")} error={errors.public_name}>
          <input id="e-public-name" className="input" value={form.public_name} onChange={set("public_name")} placeholder={t("nodes.publicNamePlaceholder")} maxLength={80} autoComplete="off" aria-invalid={!!errors.public_name} />
        </Field>
        <Field label={t("tags.label")} htmlFor="e-tags" hint={t("tags.nodeHint")} error={errors.tags}>
          <input id="e-tags" className="input" value={form.tags} onChange={set("tags")} placeholder={t("tags.placeholder")} autoComplete="off" aria-invalid={!!errors.tags} />
        </Field>
        {node && !node.local ? (
          <>
            <Field label={t("nodes.host")} htmlFor="e-host" hint={t("nodes.hostHint")} error={errors.host}>
              <input id="e-host" className="input mono" value={form.host} onChange={set("host")} autoComplete="off" aria-invalid={!!errors.host} />
            </Field>
            <Field label={t("nodes.domain")} htmlFor="e-domain" hint={t("nodes.domainHint")} error={errors.domain}>
              <input id="e-domain" className="input mono" value={form.domain} onChange={set("domain")} autoComplete="off" aria-invalid={!!errors.domain} />
            </Field>
          </>
        ) : (
          <p className="mb-4 text-[13px] text-[var(--ink-500)]">{t("nodes.localAddress")}</p>
        )}
        <Field label={t("nodes.serving")} hint={t("nodes.servingHint")}>
          <Switch checked={form.enabled} onChange={(v) => setForm((f) => ({ ...f, enabled: v }))} label={t("nodes.serving")} />
        </Field>
        <div className="dr-sec">
          <SwitchRow className="pt-0" label={t("nodes.inAuto")} sub={t("nodes.inAutoHint")} checked={form.in_auto} onChange={(v) => setForm((f) => ({ ...f, in_auto: v }))} />
        </div>
        <div className="dr-sec">
          <SwitchRow className="pt-0" label={t("nodes.fairShare")} sub={t("nodes.fairShareHint")} checked={form.fair_share} onChange={(v) => setForm((f) => ({ ...f, fair_share: v }))} />
          <Field label={t("nodes.channel")} htmlFor="e-channel" hint={form.fair_share ? t("nodes.channelHint") : t("nodes.channelHintOff")} error={errors.channel_mbps}>
            <div className="flex flex-wrap items-center gap-2">
              <label className="input-unit max-w-[180px] flex-1">
                <input
                  id="e-channel"
                  className="input"
                  inputMode="numeric"
                  value={form.channel}
                  onChange={(e) => {
                    setForm((f) => ({ ...f, channel: e.target.value }));
                    setErrors(({ channel_mbps: _, ...rest }) => rest);
                  }}
                  aria-invalid={!!errors.channel_mbps}
                />
                <span>{t("tariffs.mbps")}</span>
              </label>
              {measured > 0 && String(measured) !== form.channel ? (
                <Button size="sm" variant="ghost" type="button" onClick={() => setForm((f) => ({ ...f, channel: String(measured) }))}>
                  {t("nodes.channelFromTest", { n: measured })}
                </Button>
              ) : null}
            </div>
          </Field>
          {form.fair_share && !node?.fair_share ? <p className="text-xs text-[var(--honey-600)]">{t("nodes.fairShareReconnect")}</p> : null}
        </div>
        {node && !node.local ? (
          <div className="dr-sec">
            <SwitchRow className="pt-0" label={t("nodes.httpRedirect")} sub={t("nodes.httpRedirectHint")} checked={form.http_redirect} onChange={(v) => setForm((f) => ({ ...f, http_redirect: v }))} />
            {node.http_redirect && node.redirect_error ? (
              <p className="text-xs text-[var(--berry-600)]" role="alert">
                {t("nodes.httpRedirectBusy", { error: node.redirect_error })}
              </p>
            ) : null}
          </div>
        ) : null}
        <div className="dr-sec">
          <SwitchRow className="pt-0" label={t("nodes.sniRouter")} sub={t("nodes.sniRouterHint")} checked={form.sni_router} onChange={(v) => setForm((f) => ({ ...f, sni_router: v }))} />
          {node?.sni_router && node.router_error ? (
            <p className="text-xs text-[var(--berry-600)]" role="alert">
              {t("nodes.sniRouterBusy", { error: node.router_error })}
            </p>
          ) : null}
        </div>
        <div className="dr-sec">
          <Field label={t("nodes.frontPage")} hint={form.sni_router ? `${t("nodes.frontPageHint")} ${t("nodes.frontPageRouter")}` : t("nodes.frontPageHint")}>
            <Segmented
              label={t("nodes.frontPage")}
              value={form.front_page || "none"}
              onChange={(v) => setForm((f) => ({ ...f, front_page: v === "none" ? "" : v }))}
              options={[
                { value: "none", label: t("nodes.frontPageNone") },
                { value: "maintenance", label: t("nodes.frontPageMaintenance") },
                { value: "cats", label: t("nodes.frontPageCats") },
              ]}
            />
          </Field>
        </div>
        {node && !node.local ? (
          <div className="dr-sec">
            <SwitchRow className="pt-0" label={t("nodes.apiLockdown")} sub={t("nodes.apiLockdownHint")} checked={form.api_lockdown} onChange={(v) => setForm((f) => ({ ...f, api_lockdown: v }))} />
            {form.api_lockdown ? (
              <Field label={t("nodes.apiAllow")} htmlFor="e-api-allow" hint={t("nodes.apiAllowHint")} error={errors.api_allow}>
                <input
                  id="e-api-allow"
                  className="input mono"
                  value={form.api_allow}
                  onChange={(e) => {
                    setForm((f) => ({ ...f, api_allow: e.target.value }));
                    setErrors(({ api_allow: _, ...rest }) => rest);
                  }}
                  placeholder="203.0.113.5, 198.51.100.0/24"
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={!!errors.api_allow}
                />
              </Field>
            ) : null}
            {node.api_lockdown && node.api_lock_error ? (
              <p className="text-xs text-[var(--berry-600)]" role="alert">
                {t("nodes.apiLockFailed", { error: node.api_lock_error })}
              </p>
            ) : null}
          </div>
        ) : null}
        <BalanceFields
          group={form.group}
          capacity={form.capacity}
          onGroup={(group) => {
            setForm((f) => ({ ...f, group }));
            setErrors(({ group: _, ...rest }) => rest);
          }}
          onCapacity={(capacity) => setForm((f) => ({ ...f, capacity }))}
          error={errors.group}
          groups={(balance.data ?? []).map((g) => g.group)}
        />
        {billing ? (
          <BillingFields
            form={billing}
            errors={errors}
            onChange={(patch) => {
              setBilling((b) => (b ? { ...b, ...patch } : b));
              setErrors((e) => Object.fromEntries(Object.entries(e).filter(([k]) => !k.startsWith("billing."))));
            }}
          />
        ) : null}
      </form>
    </Drawer>
  );
}
