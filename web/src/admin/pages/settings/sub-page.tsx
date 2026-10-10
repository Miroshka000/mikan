// Settings → Subscription page: how the page and the Mini App look, what they show and in
// what order, with the page itself next to the form. One draft for the page's settings,
// saved together; images and instructions are saved as they are made.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useBlocker } from "@tanstack/react-router";
import { ArrowDown, ArrowUp, Check, ChevronDown, ImageUp, Link2, Monitor, Moon, Plus, RotateCcw, Smartphone, Sun, Trash2, Type } from "lucide-react";
import { useMemo, useRef, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { api, ApiError, basePath, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk, useNodes, useSettings } from "../../../api/hooks";
import { Confirm } from "../../../components/overlay";
import { foldedList, Mock, themeNames, ThemesToggle, useTilesPerRow } from "../../../components/theme";
import { QueryBoundary } from "../../../components/query";
import { Switch, SwitchRow } from "../../../components/switch";
import { SaveBar, WithPreview } from "../../../components/layout";
import { useToast } from "../../../components/toast";
import { Button, Field, Segmented, Skeleton } from "../../../components/ui";
import { t, useLocale, type Key } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors } from "../../../lib/fields";
import { followsMode, MODE_PALETTES, THEMES } from "../../../lib/themes";
import { safeHref } from "../../../lib/url";
import { APPS, type Platform } from "../../../sub/apps";
import { CUSTOM_TYPES, MINI_APP_ONLY, orderApps, PLATFORMS, type BlockType } from "../../../sub/page";
import type { ShopData } from "../../../sub/shop";
import type { Info } from "../../../sub/types";
import { firstPlatform } from "../../../sub/view";
import type { SETTINGS_PARTS } from "../../search";
import { DocsSection, PLATFORM_NAMES, useSubDocs } from "./sub-docs";
import { PagePreview, type PreviewDevice } from "./sub-page-preview";

type View = Schemas["SubPageView"];
type Config = Schemas["Page"];
type Block = Schemas["PageBlock"];
type Look = Schemas["PageLook"];
type Asset = Schemas["PageAsset"];
type Form = { config: Config; brand_accent: string };
type Set = (f: (c: Config) => Config) => void;

/** The editor's parts, listed under "Subscription page" in the settings' section list. */
export type SubPagePart = (typeof SETTINGS_PARTS.page)[number];
type Section = SubPagePart;

const COLOR = /^#[0-9A-Fa-f]{6}$/;
const LIMITS = { logo: 512, background: 2048 } as const;
const MAX_CUSTOM = 12;
const MAX_CSS = 20480;

/** A block's usual heading on the page, for the placeholder of its own. */
const USUAL: Partial<Record<BlockType, Key>> = {
  promo: "sub.promoTitle",
  shop: "sub.shop",
  devices: "sub.devicesTitle",
  apps: "sub.connect",
  guide: "sub.howTo",
  instructions: "sub.docs",
  link: "sub.link",
  locations: "sub.locations",
  telegram: "sub.openTelegram",
  support: "sub.support",
};

export function useSubPage() {
  return useQuery({ queryKey: qk.subPage, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/sub-page", { signal })) });
}

const imageURL = (a?: Asset) => (a ? `${basePath}/api/v1/sub-page/images/${a.name}?v=${a.hash}` : undefined);

/** The editor of the subscription page; the open part is kept by the settings page (in the URL). */
export function SubPageSettings({ section, onSection }: { section: SubPagePart; onSection: (s: SubPagePart) => void }) {
  const page = useSubPage();
  return (
    <QueryBoundary query={page} pending={<Skeleton style={{ height: 520, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(v) => <Editor v={v} section={section} setSection={onSection} />}
    </QueryBoundary>
  );
}

function Editor({ v, section, setSection }: { v: View; section: Section; setSection: (s: Section) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const { draft, setDraft, dirty, reset } = useDraft<Form>({ config: v.config, brand_accent: v.brand_accent });
  // The platform the apps section edits: the preview opens the same tab.
  const [appsTab, setAppsTab] = useState<Platform>("ios");
  const [live, setLive] = useState("");
  const [resetting, setResetting] = useState(false);
  const set: Set = (f) => setDraft((d) => ({ ...d, config: f(d.config) }));
  const save = useMutation({
    mutationFn: (f: Form) => {
      const body: Schemas["PutSubPageInputBody"] = { config: f.config };
      if (f.brand_accent !== v.brand_accent) body.brand_accent = f.brand_accent.trim().toUpperCase();
      return unwrap(api.PUT("/api/v1/sub-page", { body }));
    },
    onSuccess: (data) => {
      qc.setQueryData(qk.subPage, data);
      void qc.invalidateQueries({ queryKey: qk.settings });
      toast.ok(t("settings.page.saved"));
    },
    onError: (e) => {
      toast.error(errorText(e));
      // The first field the server refused: its section opens.
      const field = e instanceof ApiError ? Object.keys(e.fields)[0] : undefined;
      if (field) setSection(sectionOf(field));
    },
  });
  const errors = fieldErrors(save.error);
  const accentMissing = draft.config.look.accent === "brand" && !COLOR.test(draft.brand_accent.trim());
  const submit = () => {
    if (accentMissing) {
      setSection("look");
      toast.error(t("errors.api.color_required"));
      return;
    }
    save.mutate(draft);
  };
  // Leaving the page or this section drops the draft: ask first.
  const leave = useBlocker({
    shouldBlockFn: ({ current, next }) => dirty && (current.pathname !== next.pathname || (next.search as { tab?: string }).tab !== "page"),
    enableBeforeUnload: () => dirty,
    withResolver: true,
  });

  return (
    <>
      <WithPreview title={t("settings.page.preview")} preview={(bare) => <Preview v={v} draft={draft} platform={section === "apps" ? appsTab : undefined} bare={bare} />}>
            {section === "look" ? (
              <LookSection draft={draft} setDraft={setDraft} set={set} errors={errors} accentMissing={accentMissing} background={v.background} />
            ) : section === "brand" ? (
              <BrandSection draft={draft} setDraft={setDraft} set={set} errors={errors} logo={v.logo} brandName={v.brand} />
            ) : section === "blocks" ? (
              <BlocksSection config={draft.config} set={set} errors={errors} announce={setLive} />
            ) : section === "apps" ? (
              <AppsSection config={draft.config} set={set} announce={setLive} platform={appsTab} setPlatform={setAppsTab} />
            ) : section === "docs" ? (
              <DocsSection announce={setLive} />
            ) : (
              <CSSSection config={draft.config} set={set} error={errors["config.css"]} />
            )}
          <div className="flex flex-wrap items-center gap-2 px-1">
            <Button variant="ghost" size="sm" onClick={() => setResetting(true)}>
              <RotateCcw size={16} aria-hidden /> {t("settings.page.reset")}
            </Button>
            <span className="text-xs text-[var(--ink-500)]">{t("settings.page.resetHint")}</span>
          </div>
      </WithPreview>
      <p className="sr-only" role="status" aria-live="polite">
        {live}
      </p>
      <SaveBar dirty={dirty} saving={save.isPending} onSave={submit} onReset={reset} />
      <Confirm
        open={resetting}
        onOpenChange={setResetting}
        title={t("settings.page.resetTitle")}
        text={t("settings.page.resetText")}
        confirm={t("settings.page.resetConfirm")}
        danger
        onConfirm={() => {
          setDraft((d) => ({ ...d, config: structuredClone(v.defaults) }));
          setResetting(false);
        }}
      />
      <Confirm
        open={leave.status === "blocked"}
        onOpenChange={(open) => !open && leave.reset?.()}
        title={t("settings.page.leaveTitle")}
        text={t("settings.page.leaveText")}
        confirm={t("settings.page.leaveConfirm")}
        danger
        onConfirm={() => leave.proceed?.()}
      />
    </>
  );
}

/** The section of the editor a field the API refused is in. */
function sectionOf(field: string): Section {
  if (field.startsWith("config.blocks")) return "blocks";
  if (field.startsWith("config.apps")) return "apps";
  if (field.startsWith("config.css")) return "css";
  if (field.startsWith("config.look") || field === "brand_accent") return "look";
  return "brand";
}

type SectionProps = { draft: Form; setDraft: Dispatch<SetStateAction<Form>>; set: Set; errors: Record<string, string> };

function Card({ title, sub, children }: { title: string; sub?: string; children: ReactNode }) {
  return (
    <section className="card glass">
      <div className="card-head">
        <div>
          <h2 className="card-title">{title}</h2>
          {sub ? <div className="card-sub">{sub}</div> : null}
        </div>
      </div>
      {children}
    </section>
  );
}

function ColorField({ id, label, value, onChange, error, hint }: { id: string; label: string; value: string; onChange: (v: string) => void; error?: string; hint?: string }) {
  const valid = COLOR.test(value.trim());
  return (
    <Field label={label} htmlFor={id} error={error ?? (value.trim() && !valid ? t("settings.page.colorInvalid") : undefined)} hint={hint}>
      <div className="flex items-center gap-2">
        <input
          type="color"
          className="h-11 w-11 shrink-0 cursor-pointer rounded-[var(--r-md)] border border-[var(--hairline)] bg-[var(--surface)] p-1"
          value={valid ? value.trim().toLowerCase() : "#f07a2e"}
          onChange={(e) => onChange(e.target.value.toUpperCase())}
          aria-label={t("settings.page.colorPick", { name: label })}
        />
        <input id={id} className="input mono max-w-[140px]" value={value} maxLength={7} placeholder="#F07A2E" autoComplete="off" spellCheck={false} onChange={(e) => onChange(e.target.value)} aria-invalid={!!error || (!!value.trim() && !valid)} />
      </div>
    </Field>
  );
}

function LookSection({ draft, setDraft, set, errors, accentMissing, background }: SectionProps & { accentMissing: boolean; background?: Asset }) {
  const look = draft.config.look;
  const setLook = (patch: Partial<Look>) => set((c) => ({ ...c, look: { ...c.look, ...patch } }));
  const bg = look.background;
  const setBg = (patch: Partial<Look["background"]>) => setLook({ background: { ...bg, ...patch } });
  const names = themeNames();
  const [themesOpen, setThemesOpen] = useState(false);
  const [themeRows, perRow] = useTilesPerRow();
  // The four that follow the page's light/dark mode, then every other panel theme as it is.
  const moded = MODE_PALETTES.map((id) => ({ id, label: names[id], theme: id === "mikan" && look.mode === "dark" ? ("midnight" as const) : id }));
  const fixed = THEMES.filter((id) => !followsMode(id) && id !== "midnight").map((id) => ({ id, label: names[id], theme: id }));
  const fonts: { id: Look["font"]; family: string }[] = [
    { id: "default", family: '"Unbounded Variable", "Onest Variable", system-ui, sans-serif' },
    { id: "onest", family: '"Onest Variable", system-ui, sans-serif' },
    { id: "system", family: 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif' },
    { id: "rounded", family: 'ui-rounded, "SF Pro Rounded", "Nunito", system-ui, sans-serif' },
  ];
  return (
    <>
      <Card title={t("settings.page.theme")} sub={t("settings.page.themeSub")}>
        <div ref={themeRows} role="radiogroup" aria-label={t("settings.page.theme")} className="mb-4 flex flex-col gap-4">
          {[
            { label: t("settings.page.themesModed"), list: moded },
            { label: t("settings.page.themesFixed"), list: fixed },
          ].map((g) => (
            <div key={g.label} role="group" aria-label={g.label}>
              <div className="theme-group-label">{g.label}</div>
              <div className="theme-tiles">
                {foldedList(g.list, look.palette, themesOpen, perRow).map((p) => (
                  <button
                    key={p.id}
                    type="button"
                    role="radio"
                    aria-checked={look.palette === p.id}
                    aria-label={p.theme === "midnight" ? names.midnight : p.label}
                    className="theme-tile"
                    onClick={() => setLook({ palette: p.id as Look["palette"] })}
                  >
                    <Mock theme={p.theme} />
                    <span className="theme-tile-name">
                      <span>{p.theme === "midnight" ? names.midnight : p.label}</span>
                      {look.palette === p.id ? <Check size={14} aria-hidden /> : null}
                    </span>
                  </button>
                ))}
              </div>
            </div>
          ))}
          {themesOpen || fixed.length > perRow || moded.length > perRow ? (
            <ThemesToggle open={themesOpen} total={moded.length + fixed.length} onToggle={() => setThemesOpen((v) => !v)} />
          ) : null}
        </div>
        {followsMode(look.palette) ? (
        <Field label={t("settings.page.mode")} hint={look.mode === "system" ? t("settings.page.modeSystemHint") : undefined}>
          <Segmented
            label={t("settings.page.mode")}
            value={look.mode}
            onChange={(mode) => setLook({ mode })}
            options={[
              { value: "light", label: t("settings.page.modeLight") },
              { value: "dark", label: t("settings.page.modeDark") },
              { value: "system", label: t("settings.page.modeSystem") },
            ]}
          />
        </Field>
        ) : null}
        <Field label={t("settings.page.accent")} hint={t("settings.page.accentHint")}>
          <Segmented
            label={t("settings.page.accent")}
            value={look.accent}
            onChange={(accent) => {
              setLook({ accent });
              // The brand's colour starts as the palette's, not as an empty field to fill.
              if (accent === "brand" && !COLOR.test(draft.brand_accent.trim())) setDraft((d) => ({ ...d, brand_accent: "#F07A2E" }));
            }}
            options={[
              { value: "theme", label: t("settings.page.accentTheme") },
              { value: "brand", label: t("settings.page.accentBrand") },
            ]}
          />
        </Field>
        {look.accent === "brand" ? (
          <ColorField
            id="sp-accent"
            label={t("settings.brandAccent")}
            value={draft.brand_accent}
            onChange={(brand_accent) => setDraft((d) => ({ ...d, brand_accent }))}
            error={errors.brand_accent ?? (accentMissing && draft.brand_accent.trim() === "" ? t("errors.api.color_required") : undefined)}
            hint={t("settings.page.brandAccentHint")}
          />
        ) : null}
      </Card>

      <Card title={t("settings.page.background")} sub={t("settings.page.backgroundSub")}>
        <Field label={t("settings.page.backgroundKind")} error={errors["config.look.background.kind"]}>
          <Segmented
            label={t("settings.page.backgroundKind")}
            value={bg.kind}
            onChange={(kind) => {
              // A first colour or gradient starts from the theme's own background.
              const dark = look.mode === "dark";
              setBg({ kind, from: bg.from || (kind === "image" ? "" : dark ? "#0D1118" : "#FBF1E8"), to: bg.to || (kind === "gradient" ? (dark ? "#3A2418" : "#FFCBA6") : "") });
            }}
            options={[
              { value: "theme", label: t("settings.page.bgTheme") },
              { value: "solid", label: t("settings.page.bgSolid") },
              { value: "gradient", label: t("settings.page.bgGradient") },
              { value: "image", label: t("settings.page.bgImage") },
            ]}
          />
        </Field>
        {bg.kind === "solid" || bg.kind === "gradient" ? (
          <div className="grid gap-x-3 sm:grid-cols-2">
            <ColorField id="sp-bg-from" label={bg.kind === "solid" ? t("settings.page.bgColor") : t("settings.page.bgFrom")} value={bg.from} onChange={(from) => setBg({ from })} error={errors["config.look.background.from"]} />
            {bg.kind === "gradient" ? <ColorField id="sp-bg-to" label={t("settings.page.bgTo")} value={bg.to} onChange={(to) => setBg({ to })} error={errors["config.look.background.to"]} /> : null}
          </div>
        ) : null}
        {bg.kind === "gradient" ? (
          <Field label={t("settings.page.bgAngle", { n: bg.angle })} htmlFor="sp-bg-angle">
            <input id="sp-bg-angle" type="range" min={0} max={359} step={1} value={bg.angle} onChange={(e) => setBg({ angle: Number(e.target.value) })} className="w-full accent-[var(--mikan-500)]" />
          </Field>
        ) : null}
        {bg.kind === "image" ? (
          <>
            <ImageField name="background" asset={background} label={t("settings.page.bgImage")} hint={t("settings.page.bgImageHint")} />
            <Field label={t("settings.page.bgDim", { n: bg.dim })} htmlFor="sp-bg-dim" hint={t("settings.page.bgDimHint")}>
              <input id="sp-bg-dim" type="range" min={0} max={80} step={5} value={bg.dim} onChange={(e) => setBg({ dim: Number(e.target.value) })} className="w-full accent-[var(--mikan-500)]" />
            </Field>
          </>
        ) : null}
      </Card>

      <Card title={t("settings.page.type")} sub={t("settings.page.typeSub")}>
        <Field label={t("settings.page.font")}>
          <div className="grid gap-2 sm:grid-cols-2" role="radiogroup" aria-label={t("settings.page.font")}>
            {fonts.map((f) => (
              <button key={f.id} type="button" role="radio" aria-checked={look.font === f.id} className="opt" onClick={() => setLook({ font: f.id })}>
                <span className="text-[17px] leading-6 font-medium" style={{ fontFamily: f.family }}>
                  {t("settings.page.fontSample")}
                </span>
                <span className="text-xs text-[var(--ink-500)]">{t(`settings.page.fonts.${f.id}`)}</span>
              </button>
            ))}
          </div>
        </Field>
        <div className="grid gap-x-6 sm:grid-cols-2">
          <Field label={t("settings.page.radius")}>
            <Segmented
              label={t("settings.page.radius")}
              value={look.radius}
              onChange={(radius) => setLook({ radius })}
              options={[
                { value: "small", label: t("settings.page.radiusSmall") },
                { value: "medium", label: t("settings.page.radiusMedium") },
                { value: "large", label: t("settings.page.radiusLarge") },
              ]}
            />
          </Field>
          <Field label={t("settings.page.cards")} hint={look.cards === "solid" ? t("settings.page.cardsSolidHint") : t("settings.page.cardsGlassHint")}>
            <Segmented
              label={t("settings.page.cards")}
              value={look.cards}
              onChange={(cards) => setLook({ cards })}
              options={[
                { value: "glass", label: t("settings.page.cardsGlass") },
                { value: "solid", label: t("settings.page.cardsSolid") },
              ]}
            />
          </Field>
        </div>
      </Card>
    </>
  );
}

/** An image of the page (the logo, the background): uploaded at once, sniffed by the server. */
function ImageField({ name, asset, label, hint }: { name: "logo" | "background"; asset?: Asset; label: string; hint: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const input = useRef<HTMLInputElement>(null);
  const [local, setLocal] = useState("");
  const upload = useMutation({
    mutationFn: async (file: File) => {
      const data = await new Promise<string>((resolve, reject) => {
        const r = new FileReader();
        r.onload = () => resolve(String(r.result).replace(/^data:[^,]*,/, ""));
        r.onerror = () => reject(new Error("read"));
        r.readAsDataURL(file);
      });
      return unwrap(api.PUT("/api/v1/sub-page/images/{name}", { params: { path: { name } }, body: { data } }));
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.subPage });
      toast.ok(t("settings.page.imageUploaded"));
    },
  });
  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/sub-page/images/{name}", { params: { path: { name } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.subPage }),
    onError: (e) => toast.error(errorText(e)),
  });
  const pick = (file?: File) => {
    setLocal("");
    upload.reset();
    if (!file) return;
    if (!["image/png", "image/jpeg", "image/webp"].includes(file.type)) setLocal(t("errors.api.image_type"));
    else if (file.size > LIMITS[name] * 1024) setLocal(t("errors.api.image_too_big", { value: LIMITS[name] }));
    else upload.mutate(file);
  };
  const error = local || (upload.error ? (fieldErrors(upload.error).data ?? errorText(upload.error)) : "");
  const src = imageURL(asset);
  return (
    <div className="field">
      <span className="lbl">{label}</span>
      <div className="flex flex-wrap items-center gap-3">
        <span className="grid h-16 w-16 shrink-0 place-items-center overflow-hidden rounded-[14px] border border-dashed border-[var(--ink-300)] bg-[var(--surface-soft)]">
          {upload.isPending ? <Skeleton style={{ width: 64, height: 64, borderRadius: 0 }} /> : src ? <img src={src} alt="" className="h-full w-full object-cover" /> : <ImageUp size={20} className="text-[var(--ink-400)]" aria-hidden />}
        </span>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" loading={upload.isPending} onClick={() => input.current?.click()}>
            {asset ? t("settings.page.imageReplace") : t("settings.page.imageUpload")}
          </Button>
          {asset ? (
            <Button size="sm" variant="ghost" loading={remove.isPending} onClick={() => remove.mutate()}>
              {t("settings.page.imageRemove")}
            </Button>
          ) : null}
        </div>
        <input
          ref={input}
          type="file"
          accept="image/png,image/jpeg,image/webp"
          className="sr-only"
          tabIndex={-1}
          aria-label={label}
          onChange={(e) => {
            pick(e.target.files?.[0]);
            e.target.value = "";
          }}
        />
      </div>
      {error ? (
        <span className="err" role="alert">
          {error}
        </span>
      ) : (
        <span className="hint">{hint}</span>
      )}
    </div>
  );
}

function BrandSection({ draft, set, errors, logo, brandName }: SectionProps & { logo?: Asset; brandName: string }) {
  const b = draft.config.brand;
  const og = draft.config.og;
  const setBrand = (patch: Partial<Config["brand"]>) => set((c) => ({ ...c, brand: { ...c.brand, ...patch } }));
  const setOG = (patch: Partial<Config["og"]>) => set((c) => ({ ...c, og: { ...c.og, ...patch } }));
  const name = brandName.trim() || "VPN";
  return (
    <>
      <Card title={t("settings.page.brand")} sub={t("settings.page.brandSub")}>
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("settings.brand")} hint={t("settings.page.brandNameHint")}>
            <div className="flex min-h-11 items-center justify-between gap-2 rounded-[var(--r-md)] border border-[var(--hairline)] bg-[var(--surface-soft)] py-1 pr-1 pl-3">
              <span className="truncate font-medium">{brandName.trim() || "VPN"}</span>
              <Link to="/settings" search={{ tab: "general" }} className="btn btn-ghost btn-sm">
                {t("settings.page.brandNameEdit")}
              </Link>
            </div>
          </Field>
          <Field label={t("settings.page.subtitle")} htmlFor="sp-subtitle" hint={t("settings.page.subtitleHint")} error={errors["config.brand.subtitle"]}>
            <input id="sp-subtitle" className="input" value={b.subtitle} maxLength={80} onChange={(e) => setBrand({ subtitle: e.target.value })} aria-invalid={!!errors["config.brand.subtitle"]} />
          </Field>
        </div>
        <Field label={t("settings.page.logo")} hint={t("settings.page.logoHint")} error={errors["config.brand.logo"]}>
          <Segmented
            label={t("settings.page.logo")}
            value={b.logo}
            onChange={(logo) => setBrand({ logo })}
            options={[
              { value: "letter", label: t("settings.page.logoLetter") },
              { value: "image", label: t("settings.page.logoImage") },
              { value: "emoji", label: t("settings.page.logoEmoji") },
              { value: "none", label: t("settings.page.logoNone") },
            ]}
          />
        </Field>
        {b.logo === "image" ? <ImageField name="logo" asset={logo} label={t("settings.page.logoFile")} hint={t("settings.page.logoFileHint")} /> : null}
        {b.logo === "emoji" ? (
          <Field label={t("settings.page.emoji")} htmlFor="sp-emoji" hint={t("settings.page.emojiHint")} error={errors["config.brand.emoji"]}>
            <input id="sp-emoji" className="input max-w-[120px] text-center text-lg" value={b.emoji} maxLength={16} autoComplete="off" onChange={(e) => setBrand({ emoji: e.target.value.trim() })} aria-invalid={!!errors["config.brand.emoji"]} />
          </Field>
        ) : null}
      </Card>

      <Card title={t("settings.page.og")} sub={t("settings.page.ogSub")}>
        <Field label={t("settings.page.ogTitle")} htmlFor="sp-og-title" error={errors["config.og.title"]}>
          <input id="sp-og-title" className="input" value={og.title} maxLength={80} placeholder={name} onChange={(e) => setOG({ title: e.target.value })} aria-invalid={!!errors["config.og.title"]} />
        </Field>
        <Field label={t("settings.page.ogDescription")} htmlFor="sp-og-desc" error={errors["config.og.description"]}>
          <textarea id="sp-og-desc" className="input" rows={2} value={og.description} maxLength={200} onChange={(e) => setOG({ description: e.target.value.replace(/\n/g, " ") })} aria-invalid={!!errors["config.og.description"]} />
        </Field>
        <SwitchRow label={t("settings.page.ogImage")} sub={logo ? t("settings.page.ogImageSub") : t("settings.page.ogImageNoLogo")} checked={og.image} disabled={!logo && !og.image} onChange={(image) => setOG({ image })} />
        {errors["config.og.image"] ? (
          <p className="mb-3 text-xs text-[var(--berry-600)]" role="alert">
            {errors["config.og.image"]}
          </p>
        ) : null}
        <div className="text-xs text-[var(--ink-500)]">{t("settings.page.ogLook")}</div>
        {og.title || og.description || (og.image && logo) ? (
          <div className="panel-soft mt-2 flex items-start gap-3 p-3" aria-hidden>
            <span className="w-0.5 self-stretch rounded-full bg-[var(--mikan-500)]" />
            <div className="min-w-0 flex-1">
              <div className="text-xs font-semibold text-[var(--mikan-700)]">{name}</div>
              <div className="truncate text-[13px] font-semibold">{og.title || name}</div>
              {og.description ? <div className="line-clamp-2 text-xs text-[var(--ink-600)]">{og.description}</div> : null}
            </div>
            {og.image && logo ? <img src={imageURL(logo)} alt="" className="h-12 w-12 shrink-0 rounded-[10px] object-cover" /> : null}
          </div>
        ) : (
          <p className="panel-soft mt-2 p-3 text-xs text-[var(--ink-500)]">{t("settings.page.ogNone")}</p>
        )}
      </Card>
    </>
  );
}

const newId = (type: "text" | "links") => `${type}-${Math.random().toString(36).slice(2, 10) || "a"}`;

function blockName(b: Block): string {
  if (b.type === "text") return b.title || firstLine(b.text ?? "") || t("settings.page.blockNames.text");
  if (b.type === "links") return b.title || t("settings.page.blockNames.links");
  return t(`settings.page.blockNames.${b.type}`);
}

function firstLine(md: string): string {
  const line = md.split("\n").find((l) => l.trim()) ?? "";
  return line.replace(/^[#>*\-\s]+/, "").slice(0, 60);
}

function BlocksSection({ config, set, errors, announce }: { config: Config; set: Set; errors: Record<string, string>; announce: (text: string) => void }) {
  const [open, setOpen] = useState<string | null>(null);
  const blocks = config.blocks;
  const custom = blocks.filter((b) => CUSTOM_TYPES.includes(b.type)).length;
  const setBlock = (i: number, patch: Partial<Block>) => set((c) => ({ ...c, blocks: c.blocks.map((b, j) => (j === i ? { ...b, ...patch } : b)) }));
  const move = (i: number, step: -1 | 1) => {
    const b = blocks[i]!;
    set((c) => {
      const next = [...c.blocks];
      next.splice(i, 1);
      next.splice(i + step, 0, b);
      return { ...c, blocks: next };
    });
    announce(t("settings.page.moved", { name: blockName(b), n: i + step + 1, total: blocks.length }));
    window.requestAnimationFrame(() => {
      const want = document.getElementById(`blk-${step < 0 ? "up" : "down"}-${b.id}`) as HTMLButtonElement | null;
      (want && !want.disabled ? want : document.getElementById(`blk-${step < 0 ? "down" : "up"}-${b.id}`))?.focus();
    });
  };
  const add = (type: "text" | "links") => {
    const b: Block = type === "text" ? { id: newId(type), type, on: true, title: "", text: "" } : { id: newId(type), type, on: true, title: "", links: [{ label: "", url: "https://", emoji: "" }] };
    set((c) => ({ ...c, blocks: [...c.blocks, b] }));
    setOpen(b.id);
  };
  // Errors of a block's fields by its index, at the block (open it so they show).
  const blockErrors = (i: number) => Object.entries(errors).filter(([k]) => k.startsWith(`config.blocks[${i}]`));
  return (
    <Card title={t("settings.page.blocks")} sub={t("settings.page.blocksSub")}>
      <ul className="flex flex-col gap-2">
        {blocks.map((b, i) => {
          const expanded = open === b.id || blockErrors(i).length > 0;
          const name = blockName(b);
          const panel = `blk-panel-${b.id}`;
          return (
            <li key={b.id} className="panel-soft p-3">
              <div className="flex items-center gap-3">
                <Switch checked={b.on} label={t("settings.page.showBlock", { name })} onChange={(on) => setBlock(i, { on })} />
                <div className={`min-w-0 flex-1 ${b.on ? "" : "opacity-60"}`}>
                  <div className="flex items-center gap-2">
                    <span className="truncate text-[13px] font-semibold">{name}</span>
                    {MINI_APP_ONLY.includes(b.type) ? <span className="tag shrink-0">Mini App</span> : null}
                  </div>
                  <div className="truncate text-xs text-[var(--ink-500)]">{t(`settings.page.blockDesc.${b.type}`)}</div>
                </div>
                <button type="button" id={`blk-up-${b.id}`} className="icon-btn" disabled={i === 0} onClick={() => move(i, -1)} aria-label={t("settings.page.moveUp", { name })}>
                  <ArrowUp size={16} />
                </button>
                <button type="button" id={`blk-down-${b.id}`} className="icon-btn" disabled={i === blocks.length - 1} onClick={() => move(i, 1)} aria-label={t("settings.page.moveDown", { name })}>
                  <ArrowDown size={16} />
                </button>
                {b.type !== "status" ? (
                  <button type="button" className="icon-btn" aria-expanded={expanded} aria-controls={panel} onClick={() => setOpen(open === b.id ? null : b.id)} aria-label={t("settings.page.blockSetup", { name })}>
                    <ChevronDown size={16} className={`transition-transform ${expanded ? "rotate-180" : ""}`} />
                  </button>
                ) : (
                  <span className="w-9" aria-hidden />
                )}
              </div>
              {expanded && b.type !== "status" ? (
                <div id={panel} className="mt-3 border-t border-[var(--hairline)] pt-3">
                  <BlockSetup block={b} index={i} setBlock={(patch) => setBlock(i, patch)} errors={errors} />
                  {CUSTOM_TYPES.includes(b.type) ? (
                    <Button
                      size="sm"
                      variant="danger"
                      onClick={() => {
                        set((c) => ({ ...c, blocks: c.blocks.filter((x) => x.id !== b.id) }));
                        announce(t("settings.page.blockRemoved", { name }));
                      }}
                    >
                      <Trash2 size={14} aria-hidden /> {t("settings.page.blockRemove")}
                    </Button>
                  ) : null}
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button type="button" className="chip-btn inline-flex items-center gap-1" onClick={() => add("text")} disabled={custom >= MAX_CUSTOM}>
          <Type size={14} aria-hidden /> {t("settings.page.addText")}
        </button>
        <button type="button" className="chip-btn inline-flex items-center gap-1" onClick={() => add("links")} disabled={custom >= MAX_CUSTOM}>
          <Link2 size={14} aria-hidden /> {t("settings.page.addLinks")}
        </button>
        <span className="text-xs text-[var(--ink-500)]">{custom >= MAX_CUSTOM ? t("errors.api.too_many_blocks", { value: MAX_CUSTOM }) : t("settings.page.customHint")}</span>
      </div>
    </Card>
  );
}

function BlockSetup({ block: b, index: i, setBlock, errors }: { block: Block; index: number; setBlock: (patch: Partial<Block>) => void; errors: Record<string, string> }) {
  const at = (field: string) => errors[`config.blocks[${i}].${field}`];
  const usual = USUAL[b.type];
  const titleLabel = b.type === "telegram" || b.type === "support" ? t("settings.page.buttonText") : t("settings.page.blockTitle");
  const links = b.links ?? [];
  const setLink = (j: number, patch: Partial<Schemas["PageLink"]>) => setBlock({ links: links.map((l, k) => (k === j ? { ...l, ...patch } : l)) });
  return (
    <>
      <Field label={titleLabel} htmlFor={`blk-title-${b.id}`} hint={usual ? t("settings.page.blockTitleHint") : t("settings.page.blockTitleNone")} error={at("title")}>
        <input id={`blk-title-${b.id}`} className="input" value={b.title} maxLength={60} placeholder={usual ? t(usual) : ""} onChange={(e) => setBlock({ title: e.target.value })} aria-invalid={!!at("title")} />
      </Field>
      {b.type === "text" ? (
        <Field label={t("settings.page.text")} htmlFor={`blk-text-${b.id}`} hint={t("settings.page.markdownHint")} error={at("text")}>
          <textarea id={`blk-text-${b.id}`} className="input mono" rows={6} value={b.text ?? ""} maxLength={8000} onChange={(e) => setBlock({ text: e.target.value })} aria-invalid={!!at("text")} />
        </Field>
      ) : null}
      {b.type === "links" ? (
        <div className="field">
          <span className="lbl">{t("settings.page.links")}</span>
          <ul className="flex flex-col gap-2">
            {links.map((l, j) => {
              const urlErr = at(`links[${j}].url`) ?? (l.url && l.url !== "https://" && !safeHref(l.url, { tg: true }) ? t("errors.api.link_invalid") : undefined);
              const labelErr = at(`links[${j}].label`);
              return (
                <li key={j} className="grid grid-cols-[56px_minmax(0,1fr)_auto] gap-2">
                  <input className="input text-center" value={l.emoji ?? ""} maxLength={16} autoComplete="off" onChange={(e) => setLink(j, { emoji: e.target.value.trim() })} aria-label={t("settings.page.emoji")} />
                  <input className="input" value={l.label} maxLength={40} placeholder={t("settings.page.linkLabel")} onChange={(e) => setLink(j, { label: e.target.value })} aria-label={t("settings.page.linkLabel")} aria-invalid={!!labelErr} />
                  <button type="button" className="icon-btn self-center" onClick={() => setBlock({ links: links.filter((_, k) => k !== j) })} aria-label={t("settings.page.linkRemove", { name: l.label || String(j + 1) })}>
                    <Trash2 size={16} />
                  </button>
                  <input className="input mono col-span-2 col-start-2" value={l.url} maxLength={500} placeholder="https://… / tg://…" onChange={(e) => setLink(j, { url: e.target.value.trim() })} aria-label={t("settings.page.linkURL")} aria-invalid={!!urlErr} spellCheck={false} />
                  {urlErr || labelErr ? (
                    <span className="col-span-2 col-start-2 text-xs text-[var(--berry-600)]" role="alert">
                      {labelErr ?? urlErr}
                    </span>
                  ) : null}
                </li>
              );
            })}
          </ul>
          <div>
            <button type="button" className="chip-btn inline-flex items-center gap-1" disabled={links.length >= 8} onClick={() => setBlock({ links: [...links, { label: "", url: "https://", emoji: "" }] })}>
              <Plus size={14} aria-hidden /> {t("settings.page.addLink")}
            </button>
          </div>
        </div>
      ) : null}
    </>
  );
}

function AppsSection({ config, set, announce, platform, setPlatform }: { config: Config; set: Set; announce: (text: string) => void; platform: Platform; setPlatform: (p: Platform) => void }) {
  const apps = config.apps;
  const list = apps[platform];
  // Every app of the catalogue, hidden ones too, in the order the page shows them.
  const all = orderApps(APPS[platform], { order: list.order, hidden: [] });
  const setList = (next: Partial<typeof list>) => set((c) => ({ ...c, apps: { ...c.apps, [platform]: { ...c.apps[platform], ...next } } }));
  const move = (i: number, step: -1 | 1) => {
    const names = all.map((a) => a.name);
    const [name] = names.splice(i, 1);
    names.splice(i + step, 0, name!);
    setList({ order: names });
    announce(t("settings.page.moved", { name: name!, n: i + step + 1, total: names.length }));
    window.requestAnimationFrame(() => {
      const want = document.getElementById(`app-${step < 0 ? "up" : "down"}-${name}`) as HTMLButtonElement | null;
      (want && !want.disabled ? want : document.getElementById(`app-${step < 0 ? "down" : "up"}-${name}`))?.focus();
    });
  };
  const shownCount = all.filter((a) => !list.hidden.includes(a.name)).length;
  return (
    <Card title={t("settings.page.apps")} sub={t("settings.page.appsSub")}>
      <div className="grid gap-x-3 sm:grid-cols-2">
        <Field label={t("settings.page.appsDefault")} htmlFor="sp-platform" hint={t("settings.page.appsDefaultHint")}>
          <select id="sp-platform" className="input" value={apps.platform} onChange={(e) => set((c) => ({ ...c, apps: { ...c.apps, platform: e.target.value as Config["apps"]["platform"] } }))}>
            <option value="auto">{t("settings.page.appsAuto")}</option>
            {PLATFORMS.map((p) => (
              <option key={p} value={p}>
                {PLATFORM_NAMES[p]}
              </option>
            ))}
          </select>
        </Field>
        <SwitchRow label={t("settings.page.qr")} sub={t("settings.page.qrSub")} checked={apps.qr} onChange={(qr) => set((c) => ({ ...c, apps: { ...c.apps, qr } }))} />
      </div>
      <div className="mb-3 flex gap-1 overflow-x-auto" role="group" aria-label={t("sub.platform")}>
        <Segmented label={t("sub.platform")} value={platform} onChange={setPlatform} options={PLATFORMS.map((p) => ({ value: p, label: PLATFORM_NAMES[p]! }))} />
      </div>
      <ul className="flex flex-col gap-2">
        {all.map((a, i) => {
          const hidden = list.hidden.includes(a.name);
          return (
            <li key={a.name} className="panel-soft flex items-center gap-3 p-3">
              <Switch
                checked={!hidden}
                label={t("settings.page.showApp", { name: a.name })}
                onChange={(on) => setList({ hidden: on ? list.hidden.filter((n) => n !== a.name) : [...list.hidden, a.name] })}
              />
              <div className={`min-w-0 flex-1 ${hidden ? "opacity-60" : ""}`}>
                <div className="text-[13px] font-semibold">{a.name}</div>
                <div className="text-xs text-[var(--ink-500)]">{t(`sub.notes.${a.note}`)}</div>
              </div>
              <button type="button" id={`app-up-${a.name}`} className="icon-btn" disabled={i === 0} onClick={() => move(i, -1)} aria-label={t("settings.page.moveUp", { name: a.name })}>
                <ArrowUp size={16} />
              </button>
              <button type="button" id={`app-down-${a.name}`} className="icon-btn" disabled={i === all.length - 1} onClick={() => move(i, 1)} aria-label={t("settings.page.moveDown", { name: a.name })}>
                <ArrowDown size={16} />
              </button>
            </li>
          );
        })}
      </ul>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{shownCount === 0 ? t("settings.page.appsNone") : t("settings.page.appsNote")}</p>
    </Card>
  );
}

function CSSSection({ config, set, error }: { config: Config; set: Set; error?: string }) {
  const size = new Blob([config.css]).size;
  return (
    <Card title={t("settings.page.css")} sub={t("settings.page.cssSub")}>
      <Field label={t("settings.page.cssField")} htmlFor="sp-css" hint={t("settings.page.cssHint", { size: size.toLocaleString(), max: MAX_CSS.toLocaleString() })} error={error ?? (size > MAX_CSS ? t("errors.api.too_long", { value: MAX_CSS }) : undefined)}>
        <textarea
          id="sp-css"
          className="input mono"
          rows={16}
          value={config.css}
          spellCheck={false}
          placeholder={".sub-look .glass {\n  border-width: 2px;\n}"}
          onChange={(e) => set((c) => ({ ...c, css: e.target.value }))}
          aria-invalid={!!error || size > MAX_CSS}
        />
      </Field>
      <p className="text-xs text-[var(--ink-500)]">{t("settings.page.cssNote")}</p>
    </Card>
  );
}

const SAMPLE_GB = 1024 ** 3;

function Preview({ v, draft, platform: editing, bare }: { v: View; draft: Form; platform?: Platform; bare?: boolean }) {
  const settings = useSettings();
  const nodes = useNodes();
  const docs = useSubDocs();
  const locale = useLocale();
  const [device, setDevice] = useState<PreviewDevice>("phone");
  const [scheme, setScheme] = useState<"light" | "dark">(() => (window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light"));
  const [tg, setTg] = useState(false);
  const look = draft.config.look;
  const s = settings.data;
  const brand = v.brand.trim() || "VPN";
  const info = useMemo<Info>(() => {
    const now = Date.now();
    const fill = (text: string) => text.replace(/\{(\w+)\}/g, (m, k: string) => ({ brand, name: t("settings.page.sample.name"), days: "23" })[k] ?? m);
    const names = (nodes.data ?? []).map((n) => n.name).filter(Boolean);
    return {
      name: t("settings.page.sample.name"),
      brand,
      support_url: s?.support_url || "https://t.me/",
      state: "active",
      used_up: 1.6 * SAMPLE_GB,
      used_down: 10.8 * SAMPLE_GB,
      limit: 50 * SAMPLE_GB,
      expires_at: new Date(now + 23 * 86_400_000).toISOString(),
      device_limit: 3,
      protocols: [],
      binding: !!s?.device_binding,
      devices: s?.device_binding
        ? [{ id: 1, name: "", os: "iOS", os_version: "18.4", model: "iPhone 15", app: "Happ", shared: false, created_at: new Date(now - 9 * 86_400_000).toISOString(), last_seen: new Date(now - 600_000).toISOString() }]
        : [],
      telegram: "https://t.me/",
      locations: names.length ? names : [t("settings.page.sample.location1"), t("settings.page.sample.location2")],
      announce: fill(s?.sub_announce || t("settings.page.sample.announce")),
      announce_url: s?.sub_announce_url || undefined,
    };
    // The sample texts are translated: they change with the language.
  }, [brand, s, nodes.data, locale]);
  const shop = useMemo<ShopData>(
    () => ({
      allow_new: true,
      providers: { stars: true },
      offers: [
        { id: 1, name: t("settings.page.sample.month"), description: t("settings.page.sample.monthText"), rub: 19900, stars: 130 },
        { id: 2, name: t("settings.page.sample.year"), description: t("settings.page.sample.yearText"), rub: 179000, stars: 1150 },
      ],
    }),
    [locale],
  );
  const accent = look.accent === "brand" && COLOR.test(draft.brand_accent.trim()) ? draft.brand_accent.trim().toUpperCase() : undefined;
  const published = (docs.data ?? []).filter((d) => d.published).map((d) => ({ id: d.id, title: d.title, emoji: d.emoji, platform: d.platform, body: d.body }));
  const platform = editing ?? firstPlatform(draft.config, device === "phone" ? "ios" : "windows");
  const shownScheme = look.mode === "system" ? scheme : look.mode;
  return (
    <section className={bare ? "flex flex-col gap-3" : "card glass flex flex-col gap-3"} aria-label={t("settings.page.preview")}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        {bare ? <span /> : <h2 className="card-title">{t("settings.page.preview")}</h2>}
        <div className="flex flex-wrap items-center gap-2">
          <div className="seg" role="group" aria-label={t("settings.page.previewDevice")}>
            <button type="button" aria-pressed={device === "phone"} onClick={() => setDevice("phone")} aria-label={t("settings.page.phone")} title={t("settings.page.phone")}>
              <Smartphone size={14} aria-hidden />
            </button>
            <button type="button" aria-pressed={device === "desktop"} onClick={() => setDevice("desktop")} aria-label={t("settings.page.desktop")} title={t("settings.page.desktop")}>
              <Monitor size={14} aria-hidden />
            </button>
          </div>
          {look.mode === "system" ? (
            <div className="seg" role="group" aria-label={t("settings.page.previewScheme")}>
              <button type="button" aria-pressed={scheme === "light"} onClick={() => setScheme("light")} aria-label={t("settings.page.modeLight")} title={t("settings.page.modeLight")}>
                <Sun size={14} aria-hidden />
              </button>
              <button type="button" aria-pressed={scheme === "dark"} onClick={() => setScheme("dark")} aria-label={t("settings.page.modeDark")} title={t("settings.page.modeDark")}>
                <Moon size={14} aria-hidden />
              </button>
            </div>
          ) : null}
          <Segmented
            label={t("settings.page.previewWhere")}
            value={tg ? "tg" : "web"}
            onChange={(w) => setTg(w === "tg")}
            options={[
              { value: "web", label: t("settings.page.previewWeb") },
              { value: "tg", label: "Mini App" },
            ]}
          />
        </div>
      </div>
      <PagePreview
        config={draft.config}
        brand={brand}
        accent={accent}
        logo={imageURL(v.logo)}
        background={look.background.kind === "image" ? imageURL(v.background) : undefined}
        docs={published}
        info={info}
        subURL="https://vpn.example.com/sub/k3J9fQ2mXa7d"
        device={device}
        scheme={shownScheme}
        tg={tg}
        platform={platform}
        shop={shop}
      />
      <p className="text-xs text-[var(--ink-500)]">{t("settings.page.previewNote")}</p>
    </section>
  );
}
