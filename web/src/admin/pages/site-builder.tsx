import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowDown, ArrowUp, ChevronLeft, Dices, Paintbrush, Plus, Trash2 } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { api, errorText, unwrap } from "../../api/client";
import { qk, useNodes, useSettings } from "../../api/hooks";
import { useSupportMode } from "../../api/support";
import { useToast } from "../../components/toast";
import { Select } from "../../components/select";
import { Button, ErrorState, Field, PageHeader, Segmented, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { frontPageReady } from "../../lib/front-page";
import {
  BLOCK_TYPES,
  FONTS,
  KINDS,
  emptyBlock,
  freshSeed,
  palettesOf,
  parseDoc,
  randomDoc,
  renderSite,
  type Block,
  type BlockType,
  type FontId,
  type Kind,
  type Lang,
  type ListStyle,
  type Palette,
  type SiteDoc,
} from "../../lib/site-builder/render";
import { ClosedForSupport } from "../support-closed";

const pageKey = ["site-page"] as const;
const bytes = (s: string) => new TextEncoder().encode(s).length;

/**
 * The builder of the installation's own front page. It opens on the page saved before or, the
 * first time, on a site made for this installation's seed; every change shows at once in the
 * frame beside it, which holds exactly what the panel and the nodes will serve.
 */
export function SiteBuilderPage() {
  const operator = !!useSupportMode();
  const saved = useQuery({ queryKey: pageKey, queryFn: () => unwrap(api.GET("/api/v1/settings/site-page")), enabled: !operator, staleTime: Infinity });
  return (
    <>
      <Link to="/settings" search={{ tab: "security" }} className="mb-2 inline-flex items-center gap-1 rounded-lg text-[13px] font-medium text-[var(--ink-500)] hover:text-[var(--ink-900)]">
        <ChevronLeft size={16} aria-hidden /> {t("siteBuilder.back")}
      </Link>
      <PageHeader title={t("siteBuilder.title")} sub={t("siteBuilder.sub")} />
      {operator ? (
        <ClosedForSupport />
      ) : saved.isPending ? (
        <div className="site-builder" role="status" aria-busy aria-label={t("common.loading")}>
          <Skeleton style={{ height: 560, borderRadius: 20 }} />
          <Skeleton style={{ height: 560, borderRadius: 20 }} />
        </div>
      ) : saved.isError ? (
        <ErrorState text={errorText(saved.error)} onRetry={() => void saved.refetch()} />
      ) : (
        <Builder key={saved.dataUpdatedAt} saved={saved.data} />
      )}
    </>
  );
}

function Builder({ saved }: { saved: { doc: string; size: number; max: number; seed: string } }) {
  const qc = useQueryClient();
  const toast = useToast();
  const settings = useSettings();
  const nodes = useNodes();
  const [doc, setDoc] = useState<SiteDoc>(() => parseDoc(saved.doc) ?? randomDoc(saved.seed, "ru"));
  const [kind, setKind] = useState<Kind | "">("");
  const [narrow, setNarrow] = useState(false);
  const html = useMemo(() => renderSite(doc), [doc]);
  const size = useMemo(() => bytes(html), [html]);
  const tooLarge = size > saved.max;
  const dirty = JSON.stringify(doc) !== saved.doc;

  const random = (lang: Lang, k: Kind | "") => setDoc(randomDoc(freshSeed(saved.seed), lang, k || undefined));
  const set = (patch: Partial<SiteDoc>) => setDoc((d) => ({ ...d, ...patch }));
  const setTheme = (patch: Partial<SiteDoc["theme"]>) => setDoc((d) => ({ ...d, theme: { ...d.theme, ...patch } }));
  const setColor = (key: keyof Palette, value: string) => setDoc((d) => ({ ...d, theme: { ...d.theme, palette: { ...d.theme.palette, [key]: value } } }));
  const setBlock = (id: string, next: Block) => setDoc((d) => ({ ...d, blocks: d.blocks.map((b) => (b.id === id ? next : b)) }));
  const move = (i: number, by: number) =>
    setDoc((d) => {
      const blocks = [...d.blocks];
      const [b] = blocks.splice(i, 1);
      blocks.splice(i + by, 0, b!);
      return { ...d, blocks };
    });

  // Saving keeps the page; "everywhere" also makes it the page of the panel and of every node
  // that can show it. A node that fails is named, the rest stay set.
  const save = useMutation({
    mutationFn: async (everywhere: boolean) => {
      await unwrap(api.PUT("/api/v1/settings/site-page", { body: { doc: JSON.stringify(doc), html } }));
      if (!everywhere) return [] as string[];
      const failed: string[] = [];
      if (settings.data?.site_template !== "custom") {
        await unwrap(api.PATCH("/api/v1/settings", { body: { site_template: "custom" } }));
      }
      for (const n of nodes.data ?? []) {
        if (n.front_page === "custom") continue;
        try {
          await unwrap(api.PATCH("/api/v1/nodes/{id}", { params: { path: { id: n.id } }, body: { front_page: "custom" } }));
        } catch {
          failed.push(n.name);
        }
      }
      return failed;
    },
    onSuccess: (failed, everywhere) => {
      if (failed.length) toast.error(t("siteBuilder.failedNodes", { names: failed.join(", ") }));
      else toast.ok(everywhere ? t("siteBuilder.savedEverywhere") : t("siteBuilder.saved"));
      for (const key of [pageKey, qk.settings, qk.nodes]) void qc.invalidateQueries({ queryKey: key });
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const old = (nodes.data ?? []).filter((n) => !customReady(n.version));

  return (
    <div className="site-builder">
      <div className="site-builder-side">
        <section className="card glass">
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="primary" onClick={() => random(doc.lang, kind)}>
              <Dices size={16} aria-hidden /> {t("siteBuilder.random")}
            </Button>
            <Button onClick={() => set({ seed: freshSeed(saved.seed) })} title={t("siteBuilder.relookHint")}>
              <Paintbrush size={16} aria-hidden /> {t("siteBuilder.relook")}
            </Button>
          </div>
          <p className="mt-3 text-xs text-[var(--ink-500)]">{t("siteBuilder.randomHint")}</p>
          <div className="mt-4 grid gap-3 sm:grid-cols-2">
            <Field label={t("siteBuilder.kind")} htmlFor="sb-kind">
              <Select id="sb-kind" className="input" value={kind} onChange={(e) => { const k = e.target.value as Kind | ""; setKind(k); random(doc.lang, k); }}>
                <option value="">{t("siteBuilder.kindAny")}</option>
                {KINDS.map((k) => (
                  <option key={k} value={k}>
                    {t(`siteBuilder.kinds.${k}`)}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label={t("siteBuilder.lang")}>
              <Segmented
                label={t("siteBuilder.lang")}
                value={doc.lang}
                options={[
                  { value: "ru", label: "Русский" },
                  { value: "en", label: "English" },
                ]}
                onChange={(l) => l !== doc.lang && random(l as Lang, doc.kind)}
              />
            </Field>
          </div>
          <p className="mt-2 text-xs text-[var(--ink-500)]">{t("siteBuilder.langHint")}</p>
        </section>

        <Group title={t("siteBuilder.site")}>
          <Text id="sb-brand" label={t("siteBuilder.brand")} value={doc.brand} max={60} onChange={(brand) => set({ brand })} />
          <Text id="sb-tag" label={t("siteBuilder.tag")} hint={t("siteBuilder.tagHint")} value={doc.tag} max={120} onChange={(tag) => set({ tag })} />
          <Text id="sb-foot" label={t("siteBuilder.foot")} value={doc.foot} max={160} onChange={(foot) => set({ foot })} />
        </Group>

        <Group title={t("siteBuilder.look")}>
          <Field label={t("siteBuilder.palette")}>
            <div className="flex flex-wrap gap-2" role="group" aria-label={t("siteBuilder.palette")}>
              {palettesOf(doc.kind).map((p, i) => {
                const on = p.bg === doc.theme.palette.bg && p.accent === doc.theme.palette.accent;
                return (
                  <button key={i} type="button" className="sb-swatch" aria-pressed={on} aria-label={t("siteBuilder.paletteN", { n: i + 1 })} onClick={() => setTheme({ palette: { ...p } })}>
                    <span style={{ background: p.bg }} />
                    <span style={{ background: p.surface }} />
                    <span style={{ background: p.accent }} />
                    <span style={{ background: p.ink }} />
                  </button>
                );
              })}
            </div>
          </Field>
          <div className="grid grid-cols-3 gap-3">
            {(["accent", "bg", "ink"] as const).map((key) => (
              <Field key={key} label={t(`siteBuilder.color.${key}`)} htmlFor={`sb-c-${key}`}>
                <input id={`sb-c-${key}`} type="color" className="color-input" value={sixHex(doc.theme.palette[key])} onChange={(e) => setColor(key, e.target.value)} />
              </Field>
            ))}
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            {(["heading", "body"] as const).map((key) => (
              <Field key={key} label={t(`siteBuilder.font.${key}`)} htmlFor={`sb-f-${key}`}>
                <Select id={`sb-f-${key}`} className="input" value={doc.theme[key]} onChange={(e) => setTheme({ [key]: e.target.value as FontId })}>
                  {FONTS.map((f) => (
                    <option key={f} value={f}>
                      {t(`siteBuilder.fonts.${f}`)}
                    </option>
                  ))}
                </Select>
              </Field>
            ))}
          </div>
          <Range id="sb-radius" label={t("siteBuilder.radius")} value={doc.theme.radius} min={0} max={24} step={2} unit="px" onChange={(radius) => setTheme({ radius })} />
          <Range id="sb-width" label={t("siteBuilder.width")} value={doc.theme.width} min={640} max={1240} step={40} unit="px" onChange={(width) => setTheme({ width })} />
        </Group>

        <Group title={t("siteBuilder.blocks")}>
          {doc.blocks.length === 0 ? <p className="text-[13px] text-[var(--ink-500)]">{t("siteBuilder.noBlocks")}</p> : null}
          {doc.blocks.map((b, i) => (
            <details key={b.id} className="sb-block">
              <summary>
                <span className="min-w-0 flex-1 truncate">
                  <span className="text-[var(--ink-500)]">{t(`siteBuilder.types.${b.t}`)}</span> {b.title}
                </span>
                <span className="flex flex-none gap-1" onClick={(e) => e.preventDefault()}>
                  <IconButton label={t("siteBuilder.up")} disabled={i === 0} onClick={() => move(i, -1)}>
                    <ArrowUp size={16} />
                  </IconButton>
                  <IconButton label={t("siteBuilder.down")} disabled={i === doc.blocks.length - 1} onClick={() => move(i, 1)}>
                    <ArrowDown size={16} />
                  </IconButton>
                  <IconButton label={t("siteBuilder.remove")} onClick={() => setDoc((d) => ({ ...d, blocks: d.blocks.filter((x) => x.id !== b.id) }))}>
                    <Trash2 size={16} />
                  </IconButton>
                </span>
              </summary>
              <div className="flex flex-col gap-3 pt-3">
                <BlockEditor block={b} onChange={(next) => setBlock(b.id, next)} />
              </div>
            </details>
          ))}
          <AddBlock onAdd={(type) => setDoc((d) => ({ ...d, blocks: [...d.blocks, emptyBlock(type, d.lang)] }))} />
        </Group>
      </div>

      <div className="site-builder-view">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Segmented
            label={t("siteBuilder.viewport")}
            value={narrow ? "phone" : "desktop"}
            options={[
              { value: "desktop", label: t("siteBuilder.desktop") },
              { value: "phone", label: t("siteBuilder.phone") },
            ]}
            onChange={(v) => setNarrow(v === "phone")}
          />
          <span className={`text-xs ${tooLarge ? "text-[var(--berry-600)]" : "text-[var(--ink-500)]"}`} role={tooLarge ? "alert" : undefined}>
            {tooLarge ? t("siteBuilder.tooLarge", { size: kb(size), max: kb(saved.max) }) : t("siteBuilder.size", { size: kb(size) })}
          </span>
        </div>
        <div className="site-builder-frame" data-narrow={narrow || undefined}>
          {/* No scripts, no forms, no way out: the frame shows the page as a stranger's browser will. */}
          <iframe title={t("siteBuilder.preview")} sandbox="" srcDoc={html} />
        </div>
        {old.length > 0 ? <p className="text-xs text-[var(--honey-600)]">{t("siteBuilder.oldNodes", { names: old.map((n) => n.name).join(", ") })}</p> : null}
        <div className="flex flex-wrap items-center justify-end gap-2">
          <span className="mr-auto text-xs text-[var(--ink-500)]">{dirty ? t("siteBuilder.unsaved") : saved.size ? t("siteBuilder.upToDate") : ""}</span>
          <Button disabled={tooLarge || save.isPending} loading={save.isPending && save.variables === false} onClick={() => save.mutate(false)}>
            {t("siteBuilder.save")}
          </Button>
          <Button variant="primary" disabled={tooLarge || save.isPending || !nodes.isSuccess || !settings.isSuccess} loading={save.isPending && save.variables === true} onClick={() => save.mutate(true)}>
            {t("siteBuilder.saveEverywhere")}
          </Button>
        </div>
      </div>
    </div>
  );
}

/** The first node version that shows the admin's own page: 0.5.2.1-rc.10; one that does not read as a release counts as ready. */
function customReady(version?: string): boolean {
  if (!frontPageReady(version)) return false;
  const m = /^v?(\d+)\.(\d+)\.(\d+)(?:\.(\d+))?(?:-rc\.(\d+))?$/.exec(version?.trim() ?? "");
  if (!m) return true;
  const have = [m[1], m[2], m[3], m[4] ?? "0"].map(Number).concat(m[5] != null ? Number(m[5]) : Infinity);
  const want = [0, 5, 2, 1, 10];
  for (const [i, w] of want.entries()) if ((have[i] ?? 0) !== w) return (have[i] ?? 0) > w;
  return true;
}

const kb = (n: number) => (n / 1024).toFixed(n < 10240 ? 1 : 0);
/** A colour for the browser's picker, which takes #rrggbb only. */
function sixHex(c: string): string {
  if (/^#[0-9a-f]{6}$/i.test(c)) return c;
  const m = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(c);
  return m ? `#${m[1]}${m[1]}${m[2]}${m[2]}${m[3]}${m[3]}` : "#000000";
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="card glass">
      <h2 className="mb-3 text-[15px] font-semibold">{title}</h2>
      <div className="flex flex-col gap-3">{children}</div>
    </section>
  );
}

function IconButton({ label, disabled, onClick, children }: { label: string; disabled?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" className="icon-btn" aria-label={label} title={label} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  );
}

function Text({ id, label, hint, value, max, area, mono, onChange }: { id: string; label: string; hint?: string; value: string; max?: number; area?: number; mono?: boolean; onChange: (v: string) => void }) {
  return (
    <Field label={label} htmlFor={id} hint={hint}>
      {area ? (
        <textarea id={id} className={`input${mono ? " mono" : ""}`} rows={area} value={value} maxLength={max} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <input id={id} className={`input${mono ? " mono" : ""}`} value={value} maxLength={max} onChange={(e) => onChange(e.target.value)} />
      )}
    </Field>
  );
}

function Range({ id, label, value, min, max, step, unit, onChange }: { id: string; label: string; value: number; min: number; max: number; step: number; unit: string; onChange: (v: number) => void }) {
  return (
    <Field label={`${label}: ${value} ${unit}`} htmlFor={id}>
      <input id={id} type="range" className="w-full" min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} />
    </Field>
  );
}

function AddBlock({ onAdd }: { onAdd: (t: BlockType) => void }) {
  const [type, setType] = useState<BlockType>("list");
  return (
    <div className="flex items-end gap-2">
      <div className="min-w-0 flex-1">
        <Field label={t("siteBuilder.addBlock")} htmlFor="sb-add">
          <Select id="sb-add" className="input" value={type} onChange={(e) => setType(e.target.value as BlockType)}>
            {BLOCK_TYPES.map((b) => (
              <option key={b} value={b}>
                {t(`siteBuilder.types.${b}`)}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <Button onClick={() => onAdd(type)}>
        <Plus size={16} aria-hidden /> {t("siteBuilder.add")}
      </Button>
    </div>
  );
}

/** One row of a block's list with its own "remove". */
function Entry({ children, onRemove }: { children: ReactNode; onRemove: () => void }) {
  return (
    <div className="sb-entry">
      <div className="flex min-w-0 flex-1 flex-col gap-2">{children}</div>
      <IconButton label={t("siteBuilder.remove")} onClick={onRemove}>
        <Trash2 size={16} />
      </IconButton>
    </div>
  );
}

function BlockEditor({ block: b, onChange }: { block: Block; onChange: (b: Block) => void }) {
  const id = `sb-${b.id}`;
  const title = <Text id={`${id}-t`} label={t("siteBuilder.heading")} value={b.title} max={140} onChange={(v) => onChange({ ...b, title: v })} />;
  const more = (onClick: () => void) => (
    <Button size="sm" onClick={onClick}>
      <Plus size={16} aria-hidden /> {t("siteBuilder.addRow")}
    </Button>
  );
  switch (b.t) {
    case "hero":
      return (
        <>
          {title}
          <Text id={`${id}-x`} label={t("siteBuilder.text")} area={4} value={b.text} max={600} onChange={(text) => onChange({ ...b, text })} />
        </>
      );
    case "prose":
      return (
        <>
          {title}
          {b.paras.map((p, i) => (
            <Entry key={i} onRemove={() => onChange({ ...b, paras: b.paras.filter((_, j) => j !== i) })}>
              <textarea className="input" rows={3} aria-label={t("siteBuilder.paragraph", { n: i + 1 })} value={p} maxLength={1200} onChange={(e) => onChange({ ...b, paras: b.paras.map((x, j) => (j === i ? e.target.value : x)) })} />
            </Entry>
          ))}
          {more(() => onChange({ ...b, paras: [...b.paras, ""] }))}
        </>
      );
    case "list":
      return (
        <>
          {title}
          <Field label={t("siteBuilder.listStyle")}>
            <Segmented
              label={t("siteBuilder.listStyle")}
              value={b.style}
              options={(["cards", "rows", "steps"] as const).map((s) => ({ value: s, label: t(`siteBuilder.styles.${s}`) }))}
              onChange={(style) => onChange({ ...b, style: style as ListStyle })}
            />
          </Field>
          {b.items.map((it, i) => {
            const edit = (patch: Partial<typeof it>) => onChange({ ...b, items: b.items.map((x, j) => (j === i ? { ...x, ...patch } : x)) });
            return (
              <Entry key={i} onRemove={() => onChange({ ...b, items: b.items.filter((_, j) => j !== i) })}>
                <input className="input" aria-label={t("siteBuilder.itemTitle", { n: i + 1 })} value={it.title} maxLength={140} onChange={(e) => edit({ title: e.target.value })} />
                {b.style === "steps" ? null : <input className="input" aria-label={t("siteBuilder.itemMeta", { n: i + 1 })} placeholder={t("siteBuilder.metaPlaceholder")} value={it.meta ?? ""} maxLength={60} onChange={(e) => edit({ meta: e.target.value })} />}
                <textarea className="input" rows={2} aria-label={t("siteBuilder.itemText", { n: i + 1 })} value={it.text} maxLength={600} onChange={(e) => edit({ text: e.target.value })} />
              </Entry>
            );
          })}
          {more(() => onChange({ ...b, items: [...b.items, { title: "", text: "" }] }))}
        </>
      );
    case "table":
      return (
        <>
          {title}
          {b.rows.map((row, i) => {
            const edit = (patch: Partial<typeof row>) => onChange({ ...b, rows: b.rows.map((x, j) => (j === i ? { ...x, ...patch } : x)) });
            return (
              <Entry key={i} onRemove={() => onChange({ ...b, rows: b.rows.filter((_, j) => j !== i) })}>
                <div className="grid grid-cols-[minmax(0,1fr)_96px] gap-2">
                  <input className="input" aria-label={t("siteBuilder.rowName", { n: i + 1 })} value={row.name} maxLength={120} onChange={(e) => edit({ name: e.target.value })} />
                  <input className="input" aria-label={t("siteBuilder.rowValue", { n: i + 1 })} value={row.value} maxLength={40} onChange={(e) => edit({ value: e.target.value })} />
                </div>
                <input className="input" aria-label={t("siteBuilder.rowNote", { n: i + 1 })} placeholder={t("siteBuilder.notePlaceholder")} value={row.note} maxLength={160} onChange={(e) => edit({ note: e.target.value })} />
              </Entry>
            );
          })}
          {more(() => onChange({ ...b, rows: [...b.rows, { name: "", note: "", value: "" }] }))}
        </>
      );
    case "gallery":
      return (
        <>
          {title}
          <Text id={`${id}-w`} label={t("siteBuilder.works")} hint={t("siteBuilder.worksHint")} area={6} value={b.works.join("\n")} onChange={(v) => onChange({ ...b, works: v.split("\n").slice(0, 16) })} />
        </>
      );
    case "code":
      return (
        <>
          {title}
          <Text id={`${id}-c`} label={t("siteBuilder.code")} hint={t("siteBuilder.codeHint")} area={6} mono value={b.lines.join("\n")} onChange={(v) => onChange({ ...b, lines: v.split("\n").slice(0, 40) })} />
        </>
      );
  }
}
