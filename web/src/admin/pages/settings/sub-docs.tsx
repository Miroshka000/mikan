// The subscription page's instructions (sub_docs): pages of Markdown the page lists and
// opens, in the Mini App too. Each change is saved at once, apart from the page's draft.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, FileText, Pencil, Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk } from "../../../api/hooks";
import { Confirm, Drawer, DrawerFooter } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { Switch } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, Field, Pill, Segmented, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { same } from "../../../lib/draft";
import { fieldErrors } from "../../../lib/fields";
import { Markdown } from "../../../lib/markdown";
import { PLATFORMS } from "../../../sub/page";

type SubDoc = Schemas["SubDocView"];
type Body = Schemas["SubDocBody"];

export const PLATFORM_NAMES: Record<string, string> = { ios: "iPhone", android: "Android", windows: "Windows", macos: "Mac", linux: "Linux" };
const MAX_DOCS = 50;

export function useSubDocs() {
  return useQuery({ queryKey: qk.subDocs, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/sub-docs", { signal })).then((d) => d.items) });
}

export function DocsSection({ announce }: { announce: (text: string) => void }) {
  const docs = useSubDocs();
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<SubDoc | "new" | null>(null);
  const [removing, setRemoving] = useState<SubDoc | null>(null);
  const order = useMutation({
    mutationFn: (ids: number[]) => unwrap(api.PUT("/api/v1/sub-docs/order", { body: { ids } })).then((d) => d.items),
    onSuccess: (items) => qc.setQueryData(qk.subDocs, items),
    onError: (e) => {
      toast.error(errorText(e));
      void qc.invalidateQueries({ queryKey: qk.subDocs });
    },
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/sub-docs/{id}", { params: { path: { id } } })),
    onSuccess: (_, id) => {
      qc.setQueryData<SubDoc[]>(qk.subDocs, (list) => list?.filter((d) => d.id !== id));
      setRemoving(null);
      toast.ok(t("settings.page.docDeleted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const items = docs.data ?? [];
  const move = (i: number, step: -1 | 1) => {
    const next = [...items];
    const [d] = next.splice(i, 1);
    next.splice(i + step, 0, d!);
    qc.setQueryData(qk.subDocs, next);
    order.mutate(next.map((x) => x.id));
    announce(t("settings.page.moved", { name: d!.title, n: i + step + 1, total: next.length }));
    // The button pressed may be gone at the list's end: the other one of the pair keeps the focus.
    window.requestAnimationFrame(() => {
      const want = document.getElementById(`doc-${step < 0 ? "up" : "down"}-${d!.id}`) as HTMLButtonElement | null;
      (want && !want.disabled ? want : document.getElementById(`doc-${step < 0 ? "down" : "up"}-${d!.id}`))?.focus();
    });
  };

  return (
    <section className="card glass">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.page.docs")}</h2>
          <div className="card-sub">{t("settings.page.docsSub")}</div>
        </div>
        {items.length ? (
          <Button size="sm" variant="primary" onClick={() => setEditing("new")} disabled={items.length >= MAX_DOCS}>
            <Plus size={16} aria-hidden /> {t("settings.page.docNew")}
          </Button>
        ) : null}
      </div>
      <QueryBoundary
        query={docs}
        pending={
          <div className="flex flex-col gap-2" aria-busy>
            <Skeleton style={{ height: 56, borderRadius: 16 }} />
            <Skeleton style={{ height: 56, borderRadius: 16 }} />
          </div>
        }
      >
        {() =>
          items.length === 0 ? (
            <div className="panel-soft flex flex-col items-center gap-3 px-6 py-8 text-center">
              <FileText size={22} className="text-[var(--ink-400)]" aria-hidden />
              <div>
                <div className="text-[13px] font-semibold">{t("settings.page.docsEmpty")}</div>
                <p className="mt-1 max-w-[360px] text-xs text-[var(--ink-500)]">{t("settings.page.docsEmptyText")}</p>
              </div>
              <Button size="sm" variant="primary" onClick={() => setEditing("new")}>
                <Plus size={16} aria-hidden /> {t("settings.page.docNew")}
              </Button>
            </div>
          ) : (
            <ul className="flex flex-col gap-2">
              {items.map((d, i) => (
                <li key={d.id} className="panel-soft flex items-center gap-3 p-3">
                  <span className="grid h-9 w-9 shrink-0 place-items-center rounded-[10px] bg-[var(--hover)] text-base leading-none" aria-hidden>
                    {d.emoji || <FileText size={16} className="text-[var(--ink-500)]" />}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[13px] font-semibold">{d.title}</div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-[var(--ink-500)]">
                      <Pill tone={d.published ? "ok" : "off"}>{d.published ? t("settings.page.docPublished") : t("settings.page.docDraft")}</Pill>
                      <span>{d.platform ? PLATFORM_NAMES[d.platform] : t("settings.page.docAll")}</span>
                    </div>
                  </div>
                  <button type="button" id={`doc-up-${d.id}`} className="icon-btn" disabled={i === 0 || order.isPending} onClick={() => move(i, -1)} aria-label={t("settings.page.moveUp", { name: d.title })}>
                    <ArrowUp size={16} />
                  </button>
                  <button type="button" id={`doc-down-${d.id}`} className="icon-btn" disabled={i === items.length - 1 || order.isPending} onClick={() => move(i, 1)} aria-label={t("settings.page.moveDown", { name: d.title })}>
                    <ArrowDown size={16} />
                  </button>
                  <button type="button" className="icon-btn" onClick={() => setEditing(d)} aria-label={t("settings.page.docEdit", { name: d.title })}>
                    <Pencil size={16} />
                  </button>
                  <button type="button" className="icon-btn" onClick={() => setRemoving(d)} aria-label={t("settings.page.docDelete", { name: d.title })}>
                    <Trash2 size={16} />
                  </button>
                </li>
              ))}
            </ul>
          )
        }
      </QueryBoundary>
      {items.length >= MAX_DOCS ? <p className="mt-3 text-xs text-[var(--ink-500)]">{t("errors.api.too_many_docs", { value: MAX_DOCS })}</p> : null}
      {editing !== null ? <DocEditor key={editing === "new" ? "new" : editing.id} doc={editing === "new" ? null : editing} onClose={() => setEditing(null)} /> : null}
      <Confirm
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
        title={t("settings.page.docDeleteTitle", { name: removing?.title ?? "" })}
        text={t("settings.page.docDeleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </section>
  );
}

const EMPTY: Body = { title: "", emoji: "", body: "", platform: "", published: true };

function DocEditor({ doc, onClose }: { doc: SubDoc | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const start: Body = doc ? { title: doc.title, emoji: doc.emoji, body: doc.body, platform: doc.platform as Body["platform"], published: doc.published } : EMPTY;
  const [form, setForm] = useState<Body>(start);
  const [view, setView] = useState<"write" | "preview">("write");
  const [leaving, setLeaving] = useState(false);
  const save = useMutation({
    mutationFn: (body: Body) =>
      doc ? unwrap(api.PUT("/api/v1/sub-docs/{id}", { params: { path: { id: doc.id } }, body })) : unwrap(api.POST("/api/v1/sub-docs", { body })),
    onSuccess: (saved) => {
      qc.setQueryData<SubDoc[]>(qk.subDocs, (list) => (doc ? list?.map((d) => (d.id === saved.id ? saved : d)) : [...(list ?? []), saved]));
      toast.ok(doc ? t("settings.page.docSaved") : t("settings.page.docCreated"));
      onClose();
    },
    onError: (e) => {
      if (!Object.keys(fieldErrors(e)).length) toast.error(errorText(e));
    },
  });
  const errors = fieldErrors(save.error);
  const dirty = !same(form, start);
  const close = () => (dirty ? setLeaving(true) : onClose());
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ ...form, title: form.title.trim(), emoji: form.emoji.trim() });
  };
  const missing = form.title.trim() === "";
  return (
    <>
      <Drawer
        open
        onOpenChange={(open) => !open && close()}
        title={doc ? t("settings.page.docEditTitle") : t("settings.page.docNew")}
        footer={
          <DrawerFooter form="sub-doc-form" onCancel={close} loading={save.isPending} disabled={missing}>
            {t("common.save")}
          </DrawerFooter>
        }
      >
        <form id="sub-doc-form" onSubmit={submit} noValidate className="pt-4">
          <div className="grid gap-x-3 sm:grid-cols-[minmax(0,1fr)_96px]">
            <Field label={t("settings.page.docTitle")} htmlFor="doc-title" error={errors.title}>
              <input id="doc-title" className="input" value={form.title} maxLength={80} onChange={(e) => setForm({ ...form, title: e.target.value })} aria-invalid={!!errors.title} autoFocus={!doc} />
            </Field>
            <Field label={t("settings.page.emoji")} htmlFor="doc-emoji" error={errors.emoji}>
              <input id="doc-emoji" className="input text-center" value={form.emoji} maxLength={16} onChange={(e) => setForm({ ...form, emoji: e.target.value })} aria-invalid={!!errors.emoji} autoComplete="off" />
            </Field>
          </div>
          <Field label={t("settings.page.docPlatform")} htmlFor="doc-platform" hint={t("settings.page.docPlatformHint")} error={errors.platform}>
            <select id="doc-platform" className="input" value={form.platform} onChange={(e) => setForm({ ...form, platform: e.target.value as Body["platform"] })}>
              <option value="">{t("settings.page.docAll")}</option>
              {PLATFORMS.map((p) => (
                <option key={p} value={p}>
                  {PLATFORM_NAMES[p]}
                </option>
              ))}
            </select>
          </Field>
          <div className="mb-4 flex items-start justify-between gap-4">
            <div className="min-w-0">
              <div className="text-[13px] font-medium">{t("settings.page.docPublish")}</div>
              <div className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.page.docPublishHint")}</div>
            </div>
            <Switch checked={form.published} label={t("settings.page.docPublish")} onChange={(published) => setForm({ ...form, published })} />
          </div>
          <div className="field">
            <div className="flex items-center justify-between gap-2">
              <label htmlFor="doc-body" className="text-[13px] font-medium text-[var(--ink-700)]">
                {t("settings.page.docBody")}
              </label>
              <Segmented
                label={t("settings.page.docBody")}
                value={view}
                onChange={setView}
                options={[
                  { value: "write", label: t("settings.page.write") },
                  { value: "preview", label: t("settings.page.read") },
                ]}
              />
            </div>
            {view === "write" ? (
              <textarea
                id="doc-body"
                className="input mono"
                rows={16}
                value={form.body}
                maxLength={20000}
                onChange={(e) => setForm({ ...form, body: e.target.value })}
                aria-invalid={!!errors.body}
                aria-describedby="doc-body-note"
                placeholder={t("settings.page.docBodyPlaceholder")}
              />
            ) : (
              <div className="panel-soft min-h-[200px] p-4">{form.body.trim() ? <Markdown text={form.body} /> : <p className="text-[13px] text-[var(--ink-500)]">{t("settings.page.docPreviewEmpty")}</p>}</div>
            )}
            {errors.body ? (
              <span className="err" role="alert" id="doc-body-note">
                {errors.body}
              </span>
            ) : (
              <span className="hint" id="doc-body-note">
                {t("settings.page.markdownHint")}
              </span>
            )}
          </div>
        </form>
      </Drawer>
      <Confirm
        open={leaving}
        onOpenChange={setLeaving}
        title={t("settings.page.docLeaveTitle")}
        text={t("settings.page.docLeaveText")}
        confirm={t("settings.page.discard")}
        danger
        onConfirm={() => {
          setLeaving(false);
          onClose();
        }}
      />
    </>
  );
}
