import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { ArrowLeft, ExternalLink, FileCode2, Plus, Trash2 } from "lucide-react";
import { lazy, Suspense, useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk, useNodes, useReadySubTemplates, useSubTemplate, useSubTemplates } from "../../../api/hooks";
import { Card, FormActions } from "../../../components/layout";
import { Confirm, Drawer } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, EmptyState, ErrorState, Field, Pill, Segmented, Skeleton } from "../../../components/ui";
import { t, tMaybe, useLocale } from "../../../i18n";
import { useDebounced } from "../../../lib/debounce";
import { fieldErrors, onFormError } from "../../../lib/fields";
import { bytes, dateShort } from "../../../lib/format";

const ConfigEditor = lazy(() => import("../../../components/config-editor"));

type Core = Schemas["SubTemplateHead"]["core"];
type Head = Schemas["SubTemplateHead"];
type View = Schemas["SubTemplatesView"];
type Ready = Schemas["ReadySubTemplate"];

/** A ready template's name and summary in the admin's language; the API's are Russian. */
const readyName = (r: Pick<Ready, "id" | "name">) => tMaybe(`settings.subTemplates.readyNames.${r.id}`) ?? r.name;
const readySummary = (r: Ready) => tMaybe(`settings.subTemplates.readyItems.${r.id}`) ?? r.summary;

export const CORES: Core[] = ["mihomo", "singbox", "xray"];

/** The core's name as the admin knows it. */
export const coreName = (c: Core) => t(`settings.subTemplates.cores.${c}`);

/** A template being edited: a new one of a core, or a saved one. */
type Open = { core: Core; id?: number };

/**
 * The subscription templates: the admin's own config of each core, which the panel puts the
 * user's servers into. A list per core with its default, and an editor with the profile it
 * makes beside it, as it is typed.
 */
export function SubTemplates() {
  const q = useSubTemplates();
  const [core, setCore] = useState<Core>("mihomo");
  const [open, setOpen] = useState<Open | null>(null);
  return (
    <QueryBoundary query={q} pending={<Skeleton style={{ height: 320, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(v) =>
        open ? (
          <TemplateEditor v={v} open={open} onClose={() => setOpen(null)} onCreated={(id) => setOpen({ core: open.core, id })} />
        ) : (
          <TemplateList v={v} core={core} setCore={setCore} onOpen={setOpen} />
        )
      }
    </QueryBoundary>
  );
}

function TemplateList({ v, core, setCore, onOpen }: { v: View; core: Core; setCore: (c: Core) => void; onOpen: (o: Open) => void }) {
  useLocale();
  const qc = useQueryClient();
  const toast = useToast();
  const [removing, setRemoving] = useState<Head | null>(null);
  const mine = v.templates.filter((x) => x.core === core);
  const full = mine.length >= v.max;
  const clashDefault = v.templates.some((x) => x.core === "mihomo" && x.default);
  const setDefault = useMutation({
    mutationFn: ({ id, on }: { id: number; on: boolean }) => unwrap(api.PATCH("/api/v1/sub-templates/{id}", { params: { path: { id } }, body: { default: on } })),
    onSuccess: (next) => {
      qc.setQueryData(qk.subTemplate(next.id), next);
      void qc.invalidateQueries({ queryKey: qk.subTemplates });
      toast.ok(next.default ? t("settings.subTemplates.madeDefault") : t("settings.subTemplates.unsetDone"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/sub-templates/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      setRemoving(null);
      void qc.invalidateQueries({ queryKey: qk.subTemplates });
      toast.ok(t("settings.subTemplates.removed"));
    },
    onError: (e) => {
      setRemoving(null);
      toast.error(errorText(e));
    },
  });
  const create = (
    <Button variant="primary" size="sm" disabled={full} onClick={() => onOpen({ core })}>
      <Plus size={16} aria-hidden /> {t("settings.subTemplates.new")}
    </Button>
  );
  return (
    <Card title={t("settings.subTemplates.title")} sub={t("settings.subTemplates.lead")} aside={mine.length > 0 ? create : undefined}>
      <Segmented
        label={t("settings.subTemplates.core")}
        value={core}
        onChange={setCore}
        options={CORES.map((c) => {
          const n = v.templates.filter((x) => x.core === c).length;
          return { value: c, label: n > 0 ? `${coreName(c)} · ${n}` : coreName(c) };
        })}
      />
      <p className="mt-2 text-xs text-[var(--ink-500)]">{t(`settings.subTemplates.apps.${core}`)}</p>
      {core !== "mihomo" && clashDefault && !mine.some((x) => x.default) ? <div className="banner info mt-3">{t("settings.subTemplates.clashFallback", { core: coreName(core) })}</div> : null}
      {mine.length === 0 ? (
        <EmptyState icon={FileCode2} title={t("settings.subTemplates.emptyTitle", { core: coreName(core) })} text={t("settings.subTemplates.emptyText")}>
          {create}
        </EmptyState>
      ) : (
        <ul className="mt-4 flex flex-col gap-2">
          {mine.map((x) => (
            <li key={x.id} className="panel-soft flex flex-wrap items-center gap-3 p-3">
              <button type="button" className="min-w-0 flex-1 basis-48 text-left" onClick={() => onOpen({ core, id: x.id })}>
                <span className="flex min-w-0 items-center gap-2">
                  <span className="truncate text-[14px] font-medium">{x.name}</span>
                  {x.default ? <Pill tone="ok">{t("settings.subTemplates.default")}</Pill> : null}
                </span>
                <span className="block text-xs text-[var(--ink-500)]">{t("settings.subTemplates.meta", { size: bytes(x.size), date: dateShort(x.updated_at) })}</span>
                {x.rules.length > 0 ? <span className="block text-xs break-words text-[var(--ink-500)]">{t("settings.subTemplates.usedBy", { rules: x.rules.join(", ") })}</span> : null}
              </button>
              <div className="flex flex-wrap items-center gap-1">
                <Button
                  size="sm"
                  variant="ghost"
                  loading={setDefault.isPending && setDefault.variables?.id === x.id}
                  disabled={setDefault.isPending && setDefault.variables?.id !== x.id}
                  onClick={() => setDefault.mutate({ id: x.id, on: !x.default })}
                >
                  {x.default ? t("settings.subTemplates.unsetDefault") : t("settings.subTemplates.makeDefault")}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => onOpen({ core, id: x.id })}>
                  {t("settings.subTemplates.edit")}
                </Button>
                <Button size="sm" variant="danger" aria-label={t("settings.subTemplates.remove", { name: x.name })} title={t("settings.subTemplates.remove", { name: x.name })} onClick={() => setRemoving(x)}>
                  <Trash2 size={16} aria-hidden />
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      {full ? <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.subTemplates.limit", { max: v.max })}</p> : null}
      <Confirm
        open={removing !== null}
        onOpenChange={(o) => !o && setRemoving(null)}
        title={t("settings.subTemplates.removeTitle")}
        text={
          removing
            ? `${t("settings.subTemplates.removeText", { name: removing.name })} ${removing.default ? t("settings.subTemplates.removeDefault") : ""}`.trim()
            : ""
        }
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </Card>
  );
}

function TemplateEditor({ v, open, onClose, onCreated }: { v: View; open: Open; onClose: () => void; onCreated: (id: number) => void }) {
  const one = useSubTemplate(open.id);
  if (open.id !== undefined && one.isPending) return <Skeleton style={{ height: 480, borderRadius: 20 }} />;
  if (open.id !== undefined && one.isError) {
    return (
      <section className="card glass">
        <ErrorState text={errorText(one.error)} onRetry={() => void one.refetch()} />
      </section>
    );
  }
  return <EditorForm key={open.id ?? "new"} v={v} core={open.core} saved={open.id !== undefined ? one.data : undefined} onClose={onClose} onCreated={onCreated} />;
}

function EditorForm({ v, core, saved, onClose, onCreated }: { v: View; core: Core; saved?: Schemas["SubTemplate"]; onClose: () => void; onCreated: (id: number) => void }) {
  useLocale();
  const qc = useQueryClient();
  const toast = useToast();
  const nodes = useNodes();
  // The first template of a core is the one its apps get.
  const [name, setName] = useState(saved?.name ?? coreName(core));
  const [body, setBody] = useState(saved?.body ?? "");
  const [def, setDef] = useState(saved ? saved.default : !v.templates.some((x) => x.core === core && x.default));
  // What would replace the text in the editor: the starter, or a ready template by its id.
  const [replace, setReplace] = useState<string | null>(null);
  const [catalog, setCatalog] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const hidden = !!saved?.hidden;
  const dirty = saved ? name !== saved.name || def !== saved.default || (!hidden && body !== saved.body) : body.trim() !== "";
  const save = useMutation({
    mutationFn: () =>
      saved
        ? unwrap(api.PATCH("/api/v1/sub-templates/{id}", { params: { path: { id: saved.id } }, body: { name, default: def, ...(hidden ? {} : { body }) } }))
        : unwrap(api.POST("/api/v1/sub-templates", { body: { core, name, body, default: def } })),
    onSuccess: (next) => {
      qc.setQueryData(qk.subTemplate(next.id), next);
      void qc.invalidateQueries({ queryKey: qk.subTemplates });
      toast.ok(saved ? t("settings.subTemplates.saved") : t("settings.subTemplates.created"));
      if (!saved) onCreated(next.id);
    },
    onError: (e) => onFormError(e, toast),
  });
  const errors = fieldErrors(save.error);
  const starter = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/sub-templates/preview", { body: { core, starter: true } })).then((r) => r.profile),
    onSuccess: (text) => setBody(text),
    onError: (e) => toast.error(errorText(e)),
  });
  const start = () => (body.trim() ? setReplace("starter") : starter.mutate());
  const load = useMutation({
    mutationFn: (id: string) => unwrap(api.GET("/api/v1/sub-templates/ready/{id}", { params: { path: { id } } })),
    onSuccess: (r) => {
      setBody(r.body);
      // A new template is named after the one it starts from, unless the admin named it.
      if (!saved && name === coreName(core)) setName(readyName(r));
      setCatalog(false);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const pick = (id: string) => (body.trim() ? setReplace(id) : load.mutate(id));
  const leave = useBlocker({ shouldBlockFn: () => dirty && !save.isPending, enableBeforeUnload: () => dirty, withResolver: true });
  const back = () => (dirty ? setLeaving(true) : onClose());
  const reset = saved
    ? () => {
        setName(saved.name);
        setBody(saved.body);
        setDef(saved.default);
      }
    : undefined;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  const names = (nodes.data ?? []).map((n) => n.name || String(n.id));
  return (
    <div className="flex flex-col gap-4">
      <div>
        <Button size="sm" variant="ghost" onClick={back}>
          <ArrowLeft size={16} aria-hidden /> {t("settings.subTemplates.back")}
        </Button>
      </div>
      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <section className="card glass min-w-0">
          <form onSubmit={submit} noValidate className="flex flex-col gap-4">
            <div className="card-head">
              <div>
                <h2 className="card-title">{saved ? saved.name : t("settings.subTemplates.newTitle", { core: coreName(core) })}</h2>
                <div className="card-sub">{t(`settings.subTemplates.apps.${core}`)}</div>
              </div>
            </div>
            <Field label={t("settings.subTemplates.name")} htmlFor="sub-template-name" hint={t("settings.subTemplates.nameHint")} error={errors.name}>
              <input id="sub-template-name" className="input" value={name} maxLength={80} aria-invalid={!!errors.name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <SwitchRow label={t("settings.subTemplates.defaultSwitch")} sub={t("settings.subTemplates.defaultSwitchSub")} checked={def} onChange={setDef} />
            {hidden ? (
              <div className="banner info">{t("settings.subTemplates.hidden")}</div>
            ) : (
              <>
                <div className="flex flex-wrap gap-2">
                  <Button variant="ghost" size="sm" loading={starter.isPending} onClick={start}>
                    {core === "mihomo" ? t("settings.subTemplates.fromSimple") : t("settings.subTemplates.example")}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setCatalog(true)}>
                    {t("settings.subTemplates.ready.open")}
                  </Button>
                </div>
                {core === "mihomo" ? <ClashSnippets names={names} onApply={(f) => setBody(f)} current={body} /> : null}
                <div>
                  <Suspense fallback={<Skeleton style={{ height: 420, borderRadius: 12 }} />}>
                    <ConfigEditor kind={core === "mihomo" ? "profile" : "json"} value={body} onChange={setBody} label={t("settings.subTemplates.body")} invalid={!!errors.body} />
                  </Suspense>
                  {errors.body ? (
                    <p className="mt-2 text-xs text-[var(--berry-600)]" role="alert">
                      {errors.body}
                    </p>
                  ) : (
                    <p className="mt-2 text-xs text-[var(--ink-500)]">{t("settings.subTemplates.size", { size: bytes(new TextEncoder().encode(body).length), max: bytes(v.max_size) })}</p>
                  )}
                </div>
                <HowItWorks core={core} names={names} />
              </>
            )}
            <FormActions dirty={dirty} saving={save.isPending} disabled={!dirty || !name.trim() || (!hidden && !body.trim())} onReset={reset} />
          </form>
        </section>
        {hidden ? null : <LivePreview core={core} body={body} />}
      </div>
      <Confirm
        open={replace !== null}
        onOpenChange={(o) => !o && setReplace(null)}
        title={t("settings.subTemplates.replaceTitle")}
        text={t("settings.subTemplates.replaceText")}
        confirm={t("settings.subTemplates.replace")}
        onConfirm={() => {
          const what = replace;
          setReplace(null);
          if (what === "starter") starter.mutate();
          else if (what) load.mutate(what);
        }}
      />
      <ReadyTemplates core={core} open={catalog} onOpenChange={setCatalog} loading={load.isPending ? load.variables : undefined} onPick={pick} />
      <Confirm
        open={leaving || leave.status === "blocked"}
        onOpenChange={(o) => {
          if (o) return;
          setLeaving(false);
          leave.reset?.();
        }}
        title={t("settings.page.leaveTitle")}
        text={t("settings.subTemplates.leaveText")}
        confirm={t("settings.page.leaveConfirm")}
        danger
        onConfirm={() => {
          if (leave.status === "blocked") leave.proceed?.();
          else {
            setLeaving(false);
            onClose();
          }
        }}
      />
    </div>
  );
}

/** The ready templates of a core, each with its author, license and source: picking one puts
 * its text in the editor and saves nothing. mikan's own is made of the panel's settings. */
function ReadyTemplates({
  core,
  open,
  onOpenChange,
  loading,
  onPick,
}: {
  core: Core;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  loading?: string;
  onPick: (id: string) => void;
}) {
  useLocale();
  const q = useReadySubTemplates();
  return (
    <Drawer open={open} onOpenChange={onOpenChange} title={t("settings.subTemplates.ready.title", { core: coreName(core) })}>
      <p className="text-sm text-[var(--ink-500)]">{t("settings.subTemplates.ready.lead")}</p>
      <QueryBoundary query={q} pending={<Skeleton style={{ height: 160, borderRadius: 12 }} />}>
        {(v) => (
          <ul className="mt-4 flex flex-col gap-2">
            {v.templates
              .filter((r) => r.core === core)
              .map((r) => (
                <li key={r.id} className="panel-soft flex flex-col gap-2 p-3">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[14px] font-medium">{readyName(r)}</span>
                    {r.builtin ? <Pill tone="ok">{t("settings.subTemplates.ready.builtin")}</Pill> : null}
                  </div>
                  <p className="text-xs text-[var(--ink-500)]">{readySummary(r)}</p>
                  <p className="text-xs break-words text-[var(--ink-500)]">
                    {t("settings.subTemplates.ready.meta", { author: r.author, license: r.license })}{" "}
                    <a className="link-btn inline-flex items-center gap-1" href={r.source} target="_blank" rel="noreferrer noopener">
                      {t("settings.subTemplates.ready.source")} <ExternalLink size={12} aria-hidden />
                    </a>
                  </p>
                  <div>
                    <Button size="sm" variant="ghost" loading={loading === r.id} disabled={loading !== undefined && loading !== r.id} onClick={() => onPick(r.id)}>
                      {t("settings.subTemplates.ready.load")}
                    </Button>
                  </div>
                </li>
              ))}
          </ul>
        )}
      </QueryBoundary>
    </Drawer>
  );
}

/** The profile a user with every connection gets of the text being typed: asked again once
 * the text stands still, the last one kept in sight meanwhile. */
function LivePreview({ core, body }: { core: Core; body: string }) {
  useLocale();
  const settled = useDebounced(body, 600);
  const preview = useQuery({
    queryKey: ["sub-templates", "preview", core, settled],
    queryFn: ({ signal }) => unwrap(api.POST("/api/v1/sub-templates/preview", { body: { core, body: settled }, signal })),
    enabled: settled.trim() !== "",
    placeholderData: keepPreviousData,
    retry: false,
    // Each pause in the typing is a query keyed by the whole text: the ones left behind go
    // soon, not after the default five minutes.
    staleTime: 30_000,
    gcTime: 30_000,
  });
  const waiting = settled !== body || preview.isFetching;
  return (
    <section className="card glass min-w-0 xl:sticky xl:top-4 xl:self-start" aria-labelledby="sub-template-preview">
      <div className="card-head">
        <div>
          <h2 className="card-title" id="sub-template-preview">
            {t("settings.subTemplates.preview")}
          </h2>
          <div className="card-sub">{t("settings.subTemplates.previewSub")}</div>
        </div>
        {body.trim() && waiting ? <Pill tone="off">{t("settings.subTemplates.previewUpdating")}</Pill> : null}
      </div>
      <div aria-live="polite">
        {!body.trim() ? (
          <p className="text-[13px] text-[var(--ink-500)]">{t("settings.subTemplates.previewEmpty")}</p>
        ) : preview.isError && !waiting ? (
          <div className="banner err" role="alert">
            {errorText(preview.error)}
          </div>
        ) : preview.data ? (
          <>
            {preview.data.notes.length > 0 ? (
              <ul className="mb-3 flex flex-col gap-2">
                {preview.data.notes.map((n) => (
                  <li key={`${n.core}/${n.group}/${n.code}`} className="panel-soft flex flex-wrap gap-x-2 gap-y-1 p-3 text-[13px]">
                    <span className="font-medium">{coreName(n.core)}</span>
                    <span className="min-w-0 break-words text-[var(--ink-700)]">{t(`settings.routesNotes.${n.code}`, { group: n.group })}</span>
                  </li>
                ))}
              </ul>
            ) : null}
            <pre className="panel-soft mono max-h-[70vh] overflow-auto p-3 text-[12px] leading-[1.45]" style={{ opacity: waiting ? 0.6 : 1 }}>
              {preview.data.profile}
            </pre>
          </>
        ) : (
          <Skeleton style={{ height: 320, borderRadius: 12 }} />
        )}
      </div>
    </section>
  );
}

/** How the user's servers get into the groups of a template of the core, and the nodes to name. */
function HowItWorks({ core, names }: { core: Core; names: string[] }) {
  const node = names[0] ?? "1";
  const country = t("settings.subTemplates.sample.country");
  const auto = t("settings.subTemplates.sample.auto");
  const example =
    core === "mihomo"
      ? `- name: ${country}
  type: url-test
  include-all-proxies: true
  filter: "🇩🇪"

- name: ${t("settings.subTemplates.sample.fast")}
  type: url-test
  mikan:
    nodes: [${names.slice(0, 2).map((n) => `"${n}"`).join(", ") || '"1"'}]
    types: [vless, hysteria2]`
      : core === "singbox"
        ? `{"type": "urltest", "tag": "${auto}", "mikan": {}},
{"type": "selector", "tag": "🔒 VLESS",
 "mikan": {"nodes": ["${node}"], "types": ["vless"]}}`
        : `"balancers": [
  {"tag": "${auto}", "mikan": {"choice": true},
   "strategy": {"type": "leastPing"}},
  {"tag": "${country}", "mikan": {"filter": "🇩🇪"},
   "strategy": {"type": "random"}}
]`;
  return (
    <details className="text-xs text-[var(--ink-500)]">
      <summary className="cursor-pointer font-medium text-[var(--ink-700)]">{t("settings.subTemplates.how")}</summary>
      <p className="mt-2">{t(`settings.subTemplates.howServers.${core}`)}</p>
      <pre className="panel-soft mono mt-2 overflow-auto p-2 text-[12px]">{example}</pre>
      <p className="mt-2">{t("settings.subTemplates.howNodes")}</p>
      <div className="mt-1 flex flex-wrap gap-1.5">
        {names.map((n) => (
          <code key={n} className="tag mono">
            {n}
          </code>
        ))}
      </div>
      <p className="mt-2">{t(`settings.subTemplates.howRest.${core}`)}</p>
    </details>
  );
}

/** text with the top-level section key replaced by block, or block added at the end. */
function putSection(text: string, key: string, block: string): string {
  const lines = text.replace(/\s*$/, "").split("\n");
  const start = lines.findIndex((l) => l.startsWith(`${key}:`));
  if (start < 0) return (text.trim() ? text.replace(/\s*$/, "\n\n") : "") + block.trim() + "\n";
  let end = start + 1;
  while (end < lines.length && (lines[end] === "" || /^[\s#]/.test(lines[end]!))) end++;
  return [...lines.slice(0, start), ...block.trim().split("\n"), ...lines.slice(end)].join("\n") + "\n";
}

/** text with item lines added at the end (or, first, at the start) of the top-level list key. */
function addToList(text: string, key: string, item: string, first = false): string {
  const lines = text.replace(/\s*$/, "").split("\n");
  const start = lines.findIndex((l) => l.startsWith(`${key}:`));
  if (start < 0) return (text.trim() ? text.replace(/\s*$/, "\n\n") : "") + `${key}:\n` + item.replace(/\s*$/, "") + "\n";
  let end = start + 1;
  while (end < lines.length && (lines[end] === "" || /^[\s#]/.test(lines[end]!))) end++;
  while (end > start + 1 && lines[end - 1] === "") end--;
  const at = first ? start + 1 : end;
  return [...lines.slice(0, at), ...item.replace(/\s*$/, "").split("\n"), ...lines.slice(at)].join("\n") + "\n";
}

const DNS_FAKE_IP = `dns:
  enable: true
  ipv6: false
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  fake-ip-filter:
    - "*.lan"
    - "+.local"
    - "+.ntp.org"
    - "+.msftconnecttest.com"
  default-nameserver:
    - 1.1.1.1
    - 77.88.8.8
  nameserver:
    - https://1.1.1.1/dns-query#PROXY
    - https://8.8.8.8/dns-query#PROXY
  proxy-server-nameserver:
    - 1.1.1.1
    - 77.88.8.8
  nameserver-policy:
    "+.ru": [https://77.88.8.8/dns-query]
    "+.su": [https://77.88.8.8/dns-query]
    "+.xn--p1ai": [https://77.88.8.8/dns-query]`;

const TUN = `tun:
  enable: true
  stack: mixed
  auto-route: true
  auto-detect-interface: true
  strict-route: true
  dns-hijack:
    - any:53`;

const SNIFFER = `sniffer:
  enable: true
  parse-pure-ip: true
  override-destination: true
  sniff:
    HTTP:
      ports: [80, 8080-8880]
    TLS:
      ports: [443, 8443]
    QUIC:
      ports: [443, 8443]`;

/** Ready pieces of a Clash profile, one tap each: DNS, TUN, sniffing, a QUIC block, a group of a country or of a node. */
function ClashSnippets({ names, current, onApply }: { names: string[]; current: string; onApply: (text: string) => void }) {
  const snippets = [
    { id: "dns", apply: (v: string) => putSection(v, "dns", DNS_FAKE_IP) },
    { id: "tun", apply: (v: string) => putSection(v, "tun", TUN) },
    { id: "sniffer", apply: (v: string) => putSection(v, "sniffer", SNIFFER) },
    { id: "quic", apply: (v: string) => addToList(v, "rules", "  - AND,((NETWORK,UDP),(DST-PORT,443)),REJECT", true) },
    {
      id: "country",
      apply: (v: string) =>
        addToList(
          v,
          "proxy-groups",
          `  - name: ${t("settings.subTemplates.sample.country")}\n    type: url-test\n    include-all-proxies: true\n    filter: "🇩🇪"\n    url: https://www.gstatic.com/generate_204\n    interval: 300`,
        ),
    },
    { id: "node", apply: (v: string) => addToList(v, "proxy-groups", `  - name: 📺 YouTube\n    type: select\n    mikan:\n      nodes: ["${names[0] ?? "1"}"]`) },
  ] as const;
  return (
    <div className="flex flex-wrap gap-2" role="group" aria-label={t("settings.subTemplates.snippets")}>
      {snippets.map((x) => (
        <button key={x.id} type="button" className="chip-btn" onClick={() => onApply(x.apply(current))}>
          + {t(`settings.subTemplates.snippet.${x.id}`)}
        </button>
      ))}
    </div>
  );
}
