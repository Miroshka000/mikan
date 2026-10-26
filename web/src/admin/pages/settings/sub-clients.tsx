import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { FormEvent } from "react";
import { api, unwrap, type Schemas } from "../../../api/client";
import { qk, useSubApps } from "../../../api/hooks";
import { Card, FormActions } from "../../../components/layout";
import { QueryBoundary } from "../../../components/query";
import { Switch } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Field, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors, onFormError } from "../../../lib/fields";

type App = Schemas["SubAppView"];

const FAMILIES = ["mihomo", "xray", "singbox", "stash", "other"] as const;

/**
 * The known apps, each allowed or blocked. A blocked app is given one entry in place of the
 * servers, with the admin's words, and does not take a place of the subscriber's devices;
 * the subscription page does not offer it. The list is the panel's, the words are the admin's.
 */
export function SubClients() {
  const q = useSubApps();
  return (
    <QueryBoundary query={q} pending={<Skeleton style={{ height: 320, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(v) => <ClientsCard v={v} />}
    </QueryBoundary>
  );
}

function ClientsCard({ v }: { v: Schemas["SubAppsView"] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const save = useMutation({
    mutationFn: (body: { blocked: string[]; text: string }) => unwrap(api.PUT("/api/v1/sub-apps", { body })),
    onSuccess: (next) => {
      qc.setQueryData(qk.subApps, next);
      toast.ok(t("settings.subApps.saved"));
    },
    onError: (e) => onFormError(e, toast),
  });
  const { draft: text, setDraft: setText, dirty, reset } = useDraft(v.text);
  const error = fieldErrors(save.error).text;
  // A switch saves at once with the words as they were saved: what is typed in the field waits for its own button.
  const toggle = (app: App, allowed: boolean) => {
    const blocked = v.apps.filter((a) => a.blocked && a.id !== app.id).map((a) => a.id);
    if (!allowed) blocked.push(app.id);
    save.mutate({ blocked, text: v.text });
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ blocked: v.apps.filter((a) => a.blocked).map((a) => a.id), text: text.trim() }, { onSuccess: (next) => setText(next.text) });
  };
  const blockedCount = v.apps.filter((a) => a.blocked).length;
  return (
    <div className="flex flex-col gap-4">
      <Card title={t("settings.subApps.title")} sub={t("settings.subApps.sub")}>
        {FAMILIES.map((family) => {
          const group = v.apps.filter((a) => a.family === family);
          if (group.length === 0) return null;
          return (
            <section key={family} className="mb-4 last:mb-0" aria-labelledby={`sub-apps-${family}`}>
              <h3 id={`sub-apps-${family}`} className="mb-1 text-[13px] font-medium text-[var(--ink-600)]">
                {t(`settings.subApps.family.${family}`)}
              </h3>
              <ul className="row-list">
                {group.map((a) => {
                  const name = a.generic ? t(`settings.subApps.generic.${a.family}`) : a.name;
                  return (
                    <li key={a.id} className="flex min-h-12 items-center justify-between gap-3 py-2">
                      <span className={a.blocked ? "text-[14px] text-[var(--ink-500)] line-through decoration-[var(--ink-400)]" : "text-[14px] font-medium"}>{name}</span>
                      <Switch checked={!a.blocked} label={t("settings.subApps.allow", { name })} disabled={save.isPending} onChange={(on) => toggle(a, on)} />
                    </li>
                  );
                })}
              </ul>
            </section>
          );
        })}
        <p className="mt-3 text-xs text-[var(--ink-500)]" role="status">
          {blockedCount === 0 ? t("settings.subApps.noneBlocked") : t("settings.subApps.blockedCount", { n: blockedCount })}
        </p>
      </Card>
      <Card title={t("settings.subApps.textTitle")} sub={t("settings.subApps.textSub")} i={1}>
        <form onSubmit={submit} noValidate>
          <Field label={t("settings.subApps.text")} htmlFor="s-blocked-text" hint={t("settings.subApps.textHint")} error={error}>
            <input id="s-blocked-text" className="input" value={text} onChange={(e) => setText(e.target.value)} maxLength={200} placeholder={t("settings.subApps.textPlaceholder")} aria-invalid={!!error} />
          </Field>
          <div className="panel-soft mb-3 p-3">
            <div className="text-xs text-[var(--ink-500)]">{t("settings.subApps.preview")}</div>
            <div className="mt-1 text-[13px] break-words">{v.preview}</div>
          </div>
          {/* One save carries the switches and the text: the text waits while a switch saves, or it would send the old switches back. */}
          <FormActions dirty={dirty} saving={save.isPending && save.variables?.text !== v.text} disabled={save.isPending} onReset={reset} />
        </form>
      </Card>
    </div>
  );
}
