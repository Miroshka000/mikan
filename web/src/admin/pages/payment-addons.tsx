import { useIsMutating, useMutation, useQueryClient } from "@tanstack/react-query";
import { Copy } from "lucide-react";
import { useId, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk } from "../../api/hooks";
import { Drawer, DrawerFooter } from "../../components/overlay";
import { useToast } from "../../components/toast";
import { ManifestForm, manifestInitial, manifestPayload, type ManifestValues } from "../../components/manifest-form";
import { SwitchRow } from "../../components/switch";
import { t } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { useDraft } from "../../lib/draft";
import { fieldErrors } from "../../lib/fields";
import { localized } from "../../lib/localized";

// The payment methods of the marketplace (protocol 1 adapters): their data, their names and
// the settings of one, opened from its row on the addons page.

type Addons = Schemas["AddonsView"];
type Addon = Schemas["AddonView"];

/** "addon:yookassa" as the admin knows it: the adapter's own name. */
export function addonName(id: string, data: Addons | undefined): string {
  const a = data?.installed.find((x) => x.id === id) ?? data?.catalog.find((x) => x.id === id);
  return localized(a?.name, KNOWN_NAMES[id] ?? id);
}

// The two that were built in until 0.4.3, named even while the catalog is out of reach.
const KNOWN_NAMES: Record<string, string> = { yookassa: "ЮKassa", cryptobot: "CryptoBot" };

/** A payment method's settings: on or off, its keys from the provider's dashboard, its webhook. */
export function PaymentDrawer({ a, onClose }: { a: Addon | null; onClose: () => void }) {
  const formId = useId();
  const name = a ? localized(a.name, a.id) : "";
  const blocked = !a || !!a.info_error;
  return (
    <Drawer
      open={!!a}
      onOpenChange={(v) => !v && onClose()}
      title={name}
      meta={a ? t("addons.versionN", { version: a.version }) : undefined}
      footer={a && !blocked ? <PaymentFooter a={a} form={formId} onCancel={onClose} /> : undefined}
    >
      {a ? <PaymentForm key={a.id} a={a} id={formId} /> : null}
    </Drawer>
  );
}

// The footer sits outside the form; it submits it by id and shows the form's save in progress.
function PaymentFooter({ a, form, onCancel }: { a: Addon; form: string; onCancel: () => void }) {
  const saving = useIsMutating({ mutationKey: saveKey(a.id) }) > 0;
  return (
    <DrawerFooter form={form} onCancel={onCancel} loading={saving}>
      {t("common.save")}
    </DrawerFooter>
  );
}

const saveKey = (id: string) => ["addon-save", id] as const;

function useSavePayment(a: Addon) {
  const qc = useQueryClient();
  const toast = useToast();
  const name = localized(a.name, a.id);
  return useMutation({
    mutationKey: saveKey(a.id),
    mutationFn: (body: Schemas["PatchAddonInputBody"]) => unwrap(api.PATCH("/api/v1/addons/{id}", { params: { path: { id: a.id } }, body })),
    onSuccess: (v) => {
      qc.setQueryData(qk.addons, v);
      void qc.invalidateQueries({ queryKey: qk.paymentSettings });
      toast.ok(t("addons.saved", { name }));
    },
  });
}

function PaymentForm({ a, id }: { a: Addon; id: string }) {
  // The secrets in the draft start empty (the API never sends them) and are cleared after a
  // save, so a saved form is not dirty.
  const { draft, setDraft } = useDraft({ enabled: a.enabled, values: manifestInitial(a.settings) });
  const { enabled, values } = draft;
  const setValues = (fn: (v: ManifestValues) => ManifestValues) => setDraft((d) => ({ ...d, values: fn(d.values) }));
  const save = useSavePayment(a);
  const name = localized(a.name, a.id);
  const errors = fieldErrors(save.error);
  const general = save.error && !Object.keys(errors).some((k) => k.startsWith("settings.")) ? errors.settings || errorText(save.error) : "";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (save.isPending) return;
    save.mutate(
      { enabled, settings: manifestPayload(a.settings, values) },
      {
        onSuccess: (v) => {
          const now = v.installed.find((x) => x.id === a.id);
          if (now) setDraft({ enabled: now.enabled, values: manifestInitial(now.settings) });
        },
      },
    );
  };
  if (a.info_error) {
    return (
      <div className="banner warn" role="status">
        <span className="min-w-0 flex-1">{t("addons.unreachable")}</span>
      </div>
    );
  }
  return (
    <form id={id} className="flex flex-col gap-4" onSubmit={submit} noValidate aria-label={name}>
      {general ? (
        <div className="banner err" role="alert">
          {general}
        </div>
      ) : null}
      <SwitchRow label={t("addons.enabled")} sub={enabled ? t("addons.enabledSub") : t("addons.disabledSub")} checked={enabled} onChange={(on) => setDraft((d) => ({ ...d, enabled: on }))} disabled={save.isPending} />
      {enabled ? (
        <>
          {localized(a.help, "") ? <p className="text-xs text-[var(--ink-500)]">{localized(a.help, "")}</p> : null}
          <ManifestForm idPrefix={`addon-${a.id}`} fields={a.settings} values={values} onChange={(key, v) => setValues((cur) => ({ ...cur, [key]: v }))} errors={errors} disabled={save.isPending} />
          <Webhook url={a.webhook_url} />
        </>
      ) : null}
    </form>
  );
}

function Webhook({ url }: { url: string }) {
  const copy = useCopy();
  if (!url) return <p className="text-xs text-[var(--ink-500)]">{t("payments.webhookNoHost")}</p>;
  return (
    <div>
      <div className="mb-1 text-xs text-[var(--ink-500)]">{t("addons.webhook")}</div>
      <div className="link-field">
        <span className="mono">{url}</span>
        <button type="button" className="icon-btn" onClick={() => void copy(url, t("payments.webhookCopied"))} aria-label={t("payments.copyWebhook")}>
          <Copy size={18} aria-hidden />
        </button>
      </div>
    </div>
  );
}
