import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, apiCodeText, errorText, unwrap, type Schemas } from "../../../api/client";
import { useTariffs } from "../../../api/hooks";
import { FormActions } from "../../../components/layout";
import { useToast } from "../../../components/toast";
import { Button, Field } from "../../../components/ui";
import { t, type Key } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors, onFormError } from "../../../lib/fields";

type Kind = "marzban" | "pasarguard" | "remnawave";
const KINDS: Kind[] = ["marzban", "pasarguard", "remnawave"];
// Where each panel keeps its subscription links, for the old-links path.
const OLD_PATH: Record<Kind, string> = { marzban: "sub", pasarguard: "sub", remnawave: "api/sub" };

/** Users from another panel: a preview first, then the import onto a chosen plan; both run
 * in the background and the card follows them. */
export function ImportCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const tariffs = useTariffs();
  const [src, setSrc] = useState({ kind: "marzban" as Kind, url: "", username: "", password: "", token: "" });
  const [tariff, setTariff] = useState<number | "">("");
  const job = useQuery({
    queryKey: ["import-status"],
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/import/status", { signal })),
    refetchInterval: (q) => (busy(q.state.data?.state) ? 1000 : false),
  });
  const st = job.data;
  const running = busy(st?.state);
  // Once an import is over the credentials are not kept, and lists of users are refreshed.
  const [seen, setSeen] = useState<string | undefined>(undefined);
  if (st?.finished && st.finished !== seen) {
    setSeen(st.finished);
    if (st.mode === "import") {
      setSrc((s) => ({ ...s, password: "", token: "" }));
      void qc.invalidateQueries({ queryKey: ["users"] });
      void qc.invalidateQueries({ queryKey: ["legacy-links"] });
    }
  }
  const body = () => ({ kind: src.kind, url: src.url.trim(), username: src.username.trim(), password: src.password, token: src.token.trim() });
  const started = (s: Schemas["JobState"]) => qc.setQueryData(["import-status"], s);
  const check = useMutation({ mutationFn: () => unwrap(api.POST("/api/v1/import/preview", { body: body() })), onSuccess: started, onError: (e) => toast.error(errorText(e)) });
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/import", { body: { ...body(), tariff_id: Number(tariff) } })),
    onSuccess: (s) => {
      started(s);
      toast.ok(t("settings.import.started"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const cancel = useMutation({ mutationFn: () => unwrap(api.DELETE("/api/v1/import", {})), onSuccess: () => void job.refetch() });
  const errors = fieldErrors(run.error);
  const set = (k: keyof typeof src) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setSrc((s) => ({ ...s, [k]: e.target.value }));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    check.mutate();
  };
  const preview = st?.mode === "preview" && st.state === "done" ? st.preview : undefined;
  const report = st?.mode === "import" ? st.report : undefined;
  const usesPassword = src.kind !== "remnawave";
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.import.title")}</h2>
            <div className="card-sub">{t("settings.import.sub")}</div>
          </div>
        </div>
        <Field label={t("settings.import.kind")} htmlFor="imp-kind">
          <select id="imp-kind" className="input" value={src.kind} onChange={set("kind")} disabled={running}>
            {KINDS.map((k) => (
              <option key={k} value={k}>
                {t(`settings.import.kinds.${k}`)}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t("settings.import.url")} htmlFor="imp-url" hint={t("settings.import.urlHint")}>
          <input id="imp-url" className="input" value={src.url} onChange={set("url")} placeholder="https://panel.example.com" autoComplete="off" disabled={running} />
        </Field>
        {usesPassword ? (
          <div className="grid gap-x-3 sm:grid-cols-2">
            <Field label={t("settings.import.username")} htmlFor="imp-user">
              <input id="imp-user" className="input" value={src.username} onChange={set("username")} autoComplete="off" disabled={running} />
            </Field>
            <Field label={t("settings.import.password")} htmlFor="imp-pass">
              <input id="imp-pass" className="input" type="password" value={src.password} onChange={set("password")} autoComplete="new-password" disabled={running} />
            </Field>
          </div>
        ) : null}
        {src.kind !== "marzban" ? (
          <Field label={t(src.kind === "remnawave" ? "settings.import.token" : "settings.import.apiKey")} htmlFor="imp-token" hint={t(src.kind === "remnawave" ? "settings.import.tokenHint" : "settings.import.apiKeyHint")}>
            <input id="imp-token" className="input mono" type="password" value={src.token} onChange={set("token")} autoComplete="off" disabled={running} />
          </Field>
        ) : null}
        {running ? (
          <div className="panel-soft mb-3 flex items-center justify-between gap-3 p-3 text-[13px]" role="status">
            <span>{st?.state === "importing" ? t("settings.import.progress", { done: st.done, total: st.total }) : t("settings.import.fetching")}</span>
            <Button type="button" size="sm" variant="ghost" loading={cancel.isPending} onClick={() => cancel.mutate()}>
              {t("settings.import.cancel")}
            </Button>
          </div>
        ) : (
          <FormActions saving={check.isPending} disabled={!src.url.trim()} label={t("settings.import.check")} />
        )}
        {st?.state === "failed" ? <div className="banner err mt-3">{t("settings.import.failed", { error: apiCodeText(st.error ?? "") })}</div> : null}
        {preview ? (
          <div className="panel-soft mt-3 p-3 text-[13px]">
            <div className="font-medium">{t("settings.import.found", { total: preview.total, n: preview.new })}</div>
            <Names label="settings.import.taken" list={preview.taken} />
            {preview.on_hold ? <div className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.import.onHold", { n: preview.on_hold })}</div> : null}
            <Names label="settings.import.noLink" list={preview.no_link} />
            <Names label="settings.import.invalid" list={preview.invalid} bad />
            <div className="banner warn my-2">{t("settings.import.notCarried")}</div>
            <Field label={t("settings.import.tariff")} htmlFor="imp-tariff" hint={t("settings.import.tariffHint")} error={errors.tariff_id}>
              <select id="imp-tariff" className="input" value={tariff} onChange={(e) => setTariff(e.target.value ? Number(e.target.value) : "")}>
                <option value="">—</option>
                {(tariffs.data ?? []).map((tr) => (
                  <option key={tr.id} value={tr.id}>
                    {tr.name}
                  </option>
                ))}
              </select>
            </Field>
            <Button type="button" variant="primary" loading={run.isPending} disabled={tariff === "" || preview.new === 0} onClick={() => run.mutate()}>
              {t("settings.import.run", { n: preview.new })}
            </Button>
          </div>
        ) : null}
        {report ? (
          <div className="panel-soft mt-3 p-3 text-[13px]">
            <div className="font-medium">{t("settings.import.report", { created: report.created, links: report.links })}</div>
            {report.skipped.count ? <div className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.import.skipped", { n: report.skipped.count })}</div> : null}
            <Names label="settings.import.noLink" list={report.no_link} />
            <Names label="settings.import.failedUsers" list={report.failed} bad />
          </div>
        ) : null}
      </form>
    </section>
  );
}

const busy = (s?: string) => s === "fetching" || s === "checking" || s === "importing";

/** A long list of names: the first ones, and how many there are in all. */
function Names({ label, list, bad }: { label: Key; list: { names: string[]; count: number }; bad?: boolean }) {
  if (!list.count) return null;
  return (
    <div className={`mt-1 text-xs ${bad ? "text-[var(--berry-600)]" : "text-[var(--ink-500)]"}`}>
      {t(label, { n: list.count })}
      <ul className="mt-0.5">
        {list.names.slice(0, 10).map((x) => (
          <li key={x}>{x}</li>
        ))}
        {list.count > 10 ? <li>{t("settings.import.more", { n: list.count - 10 })}</li> : null}
      </ul>
    </div>
  );
}

/** The old panel's links: its path here, and for Marzban and PasarGuard its signing secret. */
export function LegacyLinksCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["legacy-links"], queryFn: ({ signal }) => unwrap(api.GET("/api/v1/import/legacy", { signal })) });
  const v = q.data;
  const { draft, setDraft, dirty, reset } = useDraft({ path: v?.path ?? "", kind: (v?.kind || "") as Kind | "" });
  const [secret, setSecret] = useState("");
  const save = useMutation({
    mutationFn: (body: { path?: string; kind?: Kind | ""; secret?: string }) => unwrap(api.PATCH("/api/v1/import/legacy", { body })),
    onSuccess: (r) => {
      qc.setQueryData(["legacy-links"], r);
      setSecret("");
      toast.ok(t("settings.import.legacySaved"));
    },
    onError: (e) => {
      onFormError(e, toast);
    },
  });
  const errors = fieldErrors(save.error);
  if (!v) return null;
  const signed = draft.kind === "marzban" || draft.kind === "pasarguard";
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <form
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate({ path: draft.path.trim(), kind: draft.kind, ...(signed && secret ? { secret } : {}) });
        }}
      >
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.import.legacy")}</h2>
          <div className="card-sub">{t("settings.import.legacySub")}</div>
        </div>
      </div>
      <Field label={t("settings.import.legacyKind")} htmlFor="leg-kind">
        <select id="leg-kind" className="input" value={draft.kind} onChange={(e) => setDraft((d) => ({ ...d, kind: e.target.value as Kind | "", path: d.path || (e.target.value ? OLD_PATH[e.target.value as Kind] : "") }))}>
          <option value="">—</option>
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {t(`settings.import.kinds.${k}`)}
            </option>
          ))}
        </select>
      </Field>
      <Field label={t("settings.import.legacyPath")} htmlFor="leg-path" hint={t("settings.import.legacyPathHint")} error={errors.path}>
        <input id="leg-path" className="input mono" value={draft.path} onChange={(e) => setDraft((d) => ({ ...d, path: e.target.value }))} placeholder="sub" autoComplete="off" aria-invalid={!!errors.path} />
      </Field>
      {signed ? (
        <Field label={t("settings.import.secret")} htmlFor="leg-secret" hint={v.secret_set ? t("settings.import.secretSet") : t("settings.import.secretHint")}>
          <input id="leg-secret" className="input mono" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} autoComplete="off" />
        </Field>
      ) : null}
      <FormActions dirty={dirty || !!secret} saving={save.isPending} onReset={reset} note={t("settings.import.legacyCount", { n: v.links })} />
      </form>
    </section>
  );
}
