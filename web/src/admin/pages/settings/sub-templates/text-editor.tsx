import { useMutation, useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useMemo, useState, type FormEvent } from "react";
import { api, errorText, unwrap } from "../../../../api/client";
import { useNodes, useReadySubTemplates } from "../../../../api/hooks";
import { CardHead, FormActions } from "../../../../components/layout";
import { Confirm } from "../../../../components/overlay";
import { useToast } from "../../../../components/toast";
import { Button, Skeleton } from "../../../../components/ui";
import { t, useLocale } from "../../../../i18n";
import { fieldErrors } from "../../../../lib/fields";
import { bytes } from "../../../../lib/format";
import { BackButton, NameFields, useLeaveGuard, useSaveTemplate } from "./editor-parts";
import { HowItWorks } from "./how";
import { LivePreview } from "./live-preview";
import { coreName, firstOfCore, readyName, type Core, type Ready, type Saved, type View } from "./model";
import { ReadyTemplates } from "./ready";
import { ClashSnippets } from "./snippets";

const ConfigEditor = lazy(() => import("../../../../components/config-editor"));

/** The start of a template of core, made of what the panel serves now: the built-in Clash
 * profile of the routing settings, mikan's working config of sing-box and Xray. */
const starterOf = (core: Core) => unwrap(api.POST("/api/v1/sub-templates/preview", { body: { core, starter: true } })).then((r) => r.profile);

/** What a text editor opens with in place of the saved template: the starter of a new one, or
 * the YAML of a simple template opened as text, with the name and the default it had in the
 * simple editor (fromSimple: a change of the kind, saved or lost). */
export type TextStart = { text: string; name?: string; def?: boolean; fromSimple?: boolean };

/**
 * A new template of text, opened with what the panel serves the core's apps now, so that the
 * admin changes a working config instead of writing one; with no server to make it of, empty.
 */
export function NewTextEditor({ v, core, onClose, onSaved }: { v: View; core: Core; onClose: () => void; onSaved: (t: Saved) => void }) {
  const start = useQuery({ queryKey: ["sub-templates", "starter", core], queryFn: () => starterOf(core), staleTime: 0, gcTime: 0, retry: false });
  if (start.isPending) return <Skeleton style={{ height: 480, borderRadius: 20 }} />;
  return <TextEditor v={v} core={core} start={{ text: start.data ?? "" }} startError={start.isError ? errorText(start.error) : undefined} onClose={onClose} onSaved={onSaved} />;
}

/**
 * A template of text, with the profile it makes beside it as it is typed: a new one of a
 * core, a saved one, or a simple one opened as text (start: its YAML, which takes the place of
 * the routing when it is saved).
 */
export function TextEditor({
  v,
  core,
  saved,
  start,
  startError,
  onClose,
  onSaved,
}: {
  v: View;
  core: Core;
  saved?: Saved;
  start?: TextStart;
  startError?: string;
  onClose: () => void;
  onSaved: (t: Saved) => void;
}) {
  useLocale();
  const initial = start?.text ?? saved?.body ?? "";
  const startName = start?.name ?? saved?.name ?? coreName(core);
  const startDef = start?.def ?? (saved ? saved.default : firstOfCore(v, core));
  const [name, setName] = useState(startName);
  const [body, setBody] = useState(initial);
  const [def, setDef] = useState(startDef);
  const hidden = !!saved?.hidden;
  // A simple template opened as text is a change until it is saved as text.
  const asText = !!start?.fromSimple;
  const dirty = asText || name !== startName || def !== startDef || (!hidden && body !== initial);
  const save = useSaveTemplate(saved, core, onSaved);
  const errors = fieldErrors(save.error);
  const guard = useLeaveGuard(dirty, save.isPending, onClose);
  const previewOf = useMemo(() => (body.trim() ? { core, body } : null), [core, body]);
  const reset = asText
    ? undefined
    : () => {
        setName(startName);
        setBody(initial);
        setDef(startDef);
      };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ name, def, content: hidden ? {} : { body } });
  };
  return (
    <div className="flex flex-col gap-4">
      <BackButton onClick={guard.back} />
      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <section className="card glass min-w-0">
          <form onSubmit={submit} noValidate className="flex flex-col gap-4">
            <CardHead title={saved ? saved.name : t("settings.subTemplates.newTitle", { core: coreName(core) })} sub={t(`settings.subTemplates.apps.${core}`)} />
            {asText ? <div className="banner info">{t("settings.subTemplates.asText.banner")}</div> : null}
            {startError ? <div className="banner info">{t("settings.subTemplates.startFailed", { error: startError })}</div> : null}
            <NameFields name={name} setName={setName} def={def} setDef={setDef} error={errors.name} defaultSub={t("settings.subTemplates.defaultSwitchSub")} />
            {hidden ? (
              <div className="banner info">{t("settings.subTemplates.hidden")}</div>
            ) : (
              <TextBody v={v} core={core} body={body} setBody={setBody} error={errors.body} onReady={(r) => !saved && name === startName && setName(readyName(r))} />
            )}
            {/* A new template opens with a working config: it may be saved as it is. */}
            <FormActions dirty={dirty} saving={save.isPending} disabled={(!!saved && !dirty) || !name.trim() || (!hidden && !body.trim())} onReset={reset} />
          </form>
        </section>
        {hidden ? null : <LivePreview of={previewOf} empty={t("settings.subTemplates.previewEmpty")} />}
      </div>
      {guard.dialog}
    </div>
  );
}

/**
 * The text of a template with what helps to write it: the start of a new one, the ready ones,
 * the Clash pieces, the size, and how the servers get in. A text of the admin's is replaced
 * only once they say so; onReady hears of a ready template loaded.
 */
function TextBody({ v, core, body, setBody, error, onReady }: { v: View; core: Core; body: string; setBody: (s: string) => void; error?: string; onReady: (r: Pick<Ready, "id" | "name">) => void }) {
  const toast = useToast();
  const nodes = useNodes();
  // What waits for the admin's word: the starter or a ready template in place of the text.
  const [replace, setReplace] = useState<(() => void) | null>(null);
  const [catalog, setCatalog] = useState(false);
  const starter = useMutation({ mutationFn: () => starterOf(core), onSuccess: setBody, onError: (e) => toast.error(errorText(e)) });
  const load = useMutation({
    mutationFn: (id: string) => unwrap(api.GET("/api/v1/sub-templates/ready/{id}", { params: { path: { id } } })),
    onSuccess: (r) => {
      setBody(r.body);
      onReady(r);
      setCatalog(false);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const unlessTyped = (action: () => void) => (body.trim() ? setReplace(() => action) : action());
  // mikan's own is what the button beside gives: the catalog is offered when it has more.
  const readyList = useReadySubTemplates();
  const readyMore = (readyList.data?.templates ?? []).some((r) => r.core === core && !r.builtin);
  const names = (nodes.data ?? []).map((n) => n.name || String(n.id));
  return (
    <>
      <div className="flex flex-wrap gap-2">
        <Button variant="ghost" size="sm" loading={starter.isPending} onClick={() => unlessTyped(() => starter.mutate())}>
          {core === "mihomo" ? t("settings.subTemplates.fromSimple") : t("settings.subTemplates.example")}
        </Button>
        {readyMore ? (
          <Button variant="ghost" size="sm" onClick={() => setCatalog(true)}>
            {t("settings.subTemplates.ready.open")}
          </Button>
        ) : null}
      </div>
      {core === "mihomo" ? <ClashSnippets names={names} onApply={setBody} current={body} /> : null}
      <div>
        <Suspense fallback={<Skeleton style={{ height: 420, borderRadius: 12 }} />}>
          <ConfigEditor kind={core === "mihomo" ? "profile" : "json"} value={body} onChange={setBody} label={t("settings.subTemplates.body")} invalid={!!error} />
        </Suspense>
        {error ? (
          <p className="mt-2 text-xs text-[var(--berry-600)]" role="alert">
            {error}
          </p>
        ) : (
          <p className="mt-2 text-xs text-[var(--ink-500)]">{t("settings.subTemplates.size", { size: bytes(new TextEncoder().encode(body).length), max: bytes(v.max_size) })}</p>
        )}
      </div>
      <HowItWorks core={core} names={names} />
      <Confirm
        open={replace !== null}
        onOpenChange={(o) => !o && setReplace(null)}
        title={t("settings.subTemplates.replaceTitle")}
        text={t("settings.subTemplates.replaceText")}
        confirm={t("settings.subTemplates.replace")}
        onConfirm={() => {
          const run = replace;
          setReplace(null);
          run?.();
        }}
      />
      <ReadyTemplates core={core} open={catalog} onOpenChange={setCatalog} loading={load.isPending ? load.variables : undefined} onPick={(id) => unlessTyped(() => load.mutate(id))} />
    </>
  );
}
