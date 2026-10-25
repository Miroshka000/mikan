// An address to make or to change, and the secret it gets: the drawer of the form, the one that
// shows a new secret once, and the question before a secret is replaced or an address deleted.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, ExternalLink, KeyRound, RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, unwrap } from "../../../api/client";
import { Confirm, Drawer, DrawerFooter } from "../../../components/overlay";
import { Button, Field, Segmented, Skeleton } from "../../../components/ui";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { t } from "../../../i18n";
import { useCopy } from "../../../lib/copy";
import { fieldErrors } from "../../../lib/fields";
import { dateShort } from "../../../lib/format";
import { docsPage, eventLabel, groupedTypes, webhookKeys, type Webhook } from "./common";

/** A secret that was just made, shown once: the panel keeps it only to sign with. */
export type MadeSecret = { secret: string; name: string };

/** The types of events an address may be told of, as the panel lists them. */
function useEventTypes(enabled: boolean) {
  return useQuery({
    queryKey: webhookKeys.events,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/webhooks/events", { signal })),
    enabled,
    staleTime: 10 * 60_000,
  });
}

/** Makes an address, or changes one: its name, where it goes, which events, and its two switches. */
export function WebhookDrawer({ target, onClose, onSecret }: { target: Webhook | "new" | null; onClose: () => void; onSecret: (s: MadeSecret) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const open = target !== null;
  const edited = target === "new" ? null : target;
  const types = useEventTypes(open);
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [all, setAll] = useState(true);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [enabled, setEnabled] = useState(true);
  const [allowLocal, setAllowLocal] = useState(false);
  const [rotating, setRotating] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const save = useSaveWebhook(edited, onClose, onSecret);
  // The form starts from the address it was opened for, and from nothing when it is closed: what
  // was typed does not wait in a closed form, and a refusal from before is not carried over. The
  // address is the one the list had when the form was opened, so a refresh of the list does not
  // take the form from under the hand that types.
  const { reset: resetSave } = save;
  useEffect(() => {
    setName(edited?.name ?? "");
    setUrl(edited?.url ?? "");
    setAll(!edited || edited.events.length === 0);
    setPicked(new Set(edited?.events ?? []));
    setEnabled(edited?.enabled ?? true);
    setAllowLocal(edited?.allow_local ?? false);
    setRotating(false);
    setDeleting(false);
    resetSave();
  }, [edited, resetSave]);

  const errors = fieldErrors(save.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (canSave && !save.isPending) save.mutate({ name: name.trim(), url: url.trim(), events: all ? [] : [...picked], enabled, allow_local: allowLocal });
  };
  const canSave = name.trim() !== "" && url.trim() !== "" && (all || picked.size > 0);
  const toggle = (type: string) =>
    setPicked((p) => {
      const next = new Set(p);
      if (!next.delete(type)) next.add(type);
      return next;
    });

  const rotate = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/webhooks/{id}/secret", { params: { path: { id } } })),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: webhookKeys.all });
      setRotating(false);
      onClose();
      onSecret({ secret: r.secret, name: r.name });
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/webhooks/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: webhookKeys.all });
      setDeleting(false);
      onClose();
      toast.ok(t("webhooks.deleted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });

  return (
    <>
      <Drawer
        open={open}
        onOpenChange={(v) => !v && onClose()}
        title={edited ? t("webhooks.editTitle") : t("webhooks.newTitle")}
        footer={
          <DrawerFooter form="webhook-form" onCancel={onClose} loading={save.isPending} disabled={!canSave}>
            {edited ? t("common.save") : t("webhooks.create")}
          </DrawerFooter>
        }
      >
        <form id="webhook-form" onSubmit={submit} className="pt-5" noValidate>
          {save.error && Object.keys(errors).length === 0 ? (
            <div className="banner err mb-4" role="alert">
              {errorText(save.error)}
            </div>
          ) : null}
          <Field label={t("webhooks.name")} htmlFor="wh-name" hint={t("webhooks.nameHint")} error={errors.name}>
            <input id="wh-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={60} placeholder={t("webhooks.namePlaceholder")} autoComplete="off" aria-invalid={!!errors.name} />
          </Field>
          <Field label={t("webhooks.url")} htmlFor="wh-url" hint={t("webhooks.urlHint")} error={errors.url}>
            <input
              id="wh-url"
              className="input mono"
              type="url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              maxLength={2048}
              placeholder="https://example.com/mikan-webhook"
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              aria-invalid={!!errors.url}
            />
          </Field>
          <Field label={t("webhooks.eventsLabel")} hint={all ? t("webhooks.eventsAllHint") : t("webhooks.eventsChosenHint")} error={errors.events ?? (!all && picked.size === 0 ? t("webhooks.eventsNone") : undefined)}>
            <Segmented
              label={t("webhooks.eventsLabel")}
              value={all ? "all" : "chosen"}
              onChange={(v) => setAll(v === "all")}
              options={[
                { value: "all", label: t("webhooks.eventsAll") },
                { value: "chosen", label: t("webhooks.eventsChosen") },
              ]}
            />
          </Field>
          {all ? null : (
            <EventPicker types={types} picked={picked} onToggle={toggle} fromNow={!!edited} />
          )}
          <SwitchRow label={t("webhooks.enabled")} sub={t("webhooks.enabledSub")} checked={enabled} onChange={setEnabled} />
          <SwitchRow label={t("webhooks.allowLocal")} sub={t("webhooks.allowLocalSub")} warn checked={allowLocal} onChange={setAllowLocal} />
        </form>
        {edited ? (
          <div className="mt-4 flex flex-col gap-4 border-t border-[var(--hairline)] pt-4">
            <div>
              <div className="text-[13px] font-medium">{t("webhooks.secretTitle")}</div>
              <div className="panel-soft mt-2 flex flex-wrap items-center justify-between gap-3 p-3">
                <div className="min-w-0 text-[13px]">
                  <div className="mono">{t("webhooks.secretNow", { hint: edited.secret_hint, date: dateShort(edited.secret_at) })}</div>
                  <p className="mt-1 text-xs text-[var(--ink-500)]">{t("webhooks.secretNote")}</p>
                </div>
                <Button size="sm" onClick={() => setRotating(true)}>
                  <RefreshCw size={16} aria-hidden /> {t("webhooks.rotate")}
                </Button>
              </div>
            </div>
            <div>
              <Button variant="danger" size="sm" onClick={() => setDeleting(true)}>
                <Trash2 size={16} aria-hidden /> {t("webhooks.delete")}
              </Button>
            </div>
          </div>
        ) : null}
      </Drawer>
      <Confirm
        open={rotating}
        onOpenChange={setRotating}
        title={t("webhooks.rotateTitle", { name: edited?.name ?? "" })}
        text={t("webhooks.rotateText")}
        confirm={t("webhooks.rotate")}
        danger
        loading={rotate.isPending}
        onConfirm={() => edited && rotate.mutate(edited.id)}
      />
      <Confirm
        open={deleting}
        onOpenChange={setDeleting}
        title={t("webhooks.deleteTitle", { name: edited?.name ?? "" })}
        text={t("webhooks.deleteText")}
        confirm={t("webhooks.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => edited && remove.mutate(edited.id)}
      />
    </>
  );
}

type Body = { name: string; url: string; events: string[]; enabled: boolean; allow_local: boolean };

/** The save of the form: a new address is made (its secret is shown), an old one changed. */
function useSaveWebhook(edited: Webhook | null, onClose: () => void, onSecret: (s: MadeSecret) => void) {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: async (body: Body): Promise<{ secret?: string }> =>
      edited
        ? unwrap(api.PATCH("/api/v1/webhooks/{id}", { params: { path: { id: edited.id } }, body })).then(() => ({}))
        : unwrap(api.POST("/api/v1/webhooks", { body })),
    onSuccess: (r, body) => {
      void qc.invalidateQueries({ queryKey: webhookKeys.all });
      onClose();
      if (r.secret) onSecret({ secret: r.secret, name: body.name });
      else toast.ok(t("webhooks.saved"));
    },
  });
}

/** The events to choose from, by what they are about; with a type the panel gains later, its own name. */
function EventPicker({ types, picked, onToggle, fromNow }: { types: ReturnType<typeof useEventTypes>; picked: Set<string>; onToggle: (type: string) => void; fromNow: boolean }) {
  if (types.isPending) return <Skeleton className="mb-4" style={{ height: 160, borderRadius: 16 }} />;
  if (types.isError) {
    return (
      <div className="banner err mb-4" role="alert">
        <span className="min-w-0 flex-1">{errorText(types.error)}</span>
        <Button size="sm" variant="ghost" loading={types.isFetching} onClick={() => void types.refetch()}>
          {t("common.retry")}
        </Button>
      </div>
    );
  }
  return (
    <div className="mb-4 flex flex-col gap-4 rounded-2xl border border-[var(--hairline)] p-4">
      {groupedTypes(types.data.types).map((g) => (
        <fieldset key={g.group} className="flex flex-col gap-3">
          <legend className="mb-1 text-xs font-medium text-[var(--ink-500)]">{t(`webhooks.groups.${g.group}`)}</legend>
          {g.types.map((type) => (
            <label key={type} className="flex items-start gap-2 text-[13px]">
              <input type="checkbox" className="check mt-0.5 shrink-0" checked={picked.has(type)} onChange={() => onToggle(type)} />
              <span className="min-w-0">
                <span className="font-medium">{eventLabel(type)}</span> <span className="mono text-xs text-[var(--ink-500)]">{type}</span>
              </span>
            </label>
          ))}
        </fieldset>
      ))}
      {fromNow ? <p className="text-xs text-[var(--ink-500)]">{t("webhooks.eventsFromNow")}</p> : null}
    </div>
  );
}

/** A new secret, shown once: the panel keeps it only to sign with and gives back its last characters. */
export function SecretDrawer({ made, onClose }: { made: MadeSecret | null; onClose: () => void }) {
  const copyText = useCopy();
  return (
    <Drawer
      open={!!made}
      onOpenChange={(v) => !v && onClose()}
      title={t("webhooks.madeTitle")}
      meta={made?.name}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t("webhooks.madeDone")}
        </Button>
      }
    >
      <div className="pt-5">
        <div className="banner warn mb-4">
          <KeyRound size={18} className="shrink-0" aria-hidden />
          <span>{t("webhooks.madeOnce")}</span>
        </div>
        <div className="link-field">
          <span className="mono break-all">{made?.secret}</span>
          <button type="button" className="icon-btn" onClick={() => made && void copyText(made.secret, t("webhooks.secretCopied"))} aria-label={t("webhooks.copySecret")}>
            <Copy size={18} />
          </button>
        </div>
        <p className="mt-4 text-[13px] text-[var(--ink-600)]">{t("webhooks.secretNote")}</p>
        <a className="link-btn mt-2 inline-flex items-center gap-1 text-[13px]" href={docsPage()} target="_blank" rel="noopener noreferrer">
          {t("webhooks.docs")} <ExternalLink size={14} aria-hidden />
        </a>
      </div>
    </Drawer>
  );
}
