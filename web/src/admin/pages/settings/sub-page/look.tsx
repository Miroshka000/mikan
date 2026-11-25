// The page's look: palette, colours, font, corners, the background, and the own CSS.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, ImageUp } from "lucide-react";
import { useRef, useState } from "react";
import { api, errorText, unwrap } from "../../../../api/client";
import { qk } from "../../../../api/hooks";
import { foldedList, Mock, themeNames, ThemesToggle, useTilesPerRow } from "../../../../components/theme";
import { useToast } from "../../../../components/toast";
import { Button, Field, Segmented, Skeleton } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { fieldErrors } from "../../../../lib/fields";
import { followsMode, MODE_PALETTES, THEMES } from "../../../../lib/themes";
import { Card, COLOR, imageURL, LIMITS, MAX_CSS, type Asset, type Config, type Look, type SectionProps, type Set } from "./parts";

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

/**
 * The page's look. accentShared: the page is a group's, whose brand colour is the panel's,
 * shown and not edited here.
 */
export function LookSection({ draft, setDraft, set, errors, accentMissing, background, accentShared }: SectionProps & { accentMissing: boolean; background?: Asset; accentShared?: boolean }) {
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
              // The brand's colour starts as the palette's, not as an empty field to fill; a
              // group's page takes the panel's as it is.
              if (accent === "brand" && !accentShared && !COLOR.test(draft.brand_accent.trim())) setDraft((d) => ({ ...d, brand_accent: "#F07A2E" }));
            }}
            options={[
              { value: "theme", label: t("settings.page.accentTheme") },
              { value: "brand", label: t("settings.page.accentBrand") },
            ]}
          />
        </Field>
        {look.accent === "brand" && accentShared ? (
          <Field label={t("settings.brandAccent")} hint={t("settings.page.accentShared")} error={errors["config.look.accent"]}>
            <div className="flex items-center gap-2">
              <span className="h-9 w-9 shrink-0 rounded-[var(--r-md)] border border-[var(--hairline)]" style={{ background: COLOR.test(draft.brand_accent.trim()) ? draft.brand_accent.trim() : "transparent" }} aria-hidden />
              <span className="mono text-[13px]">{draft.brand_accent.trim() || "—"}</span>
            </div>
          </Field>
        ) : look.accent === "brand" ? (
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
export function ImageField({ name, asset, label, hint }: { name: "logo" | "background"; asset?: Asset; label: string; hint: string }) {
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

export function CSSSection({ config, set, error }: { config: Config; set: Set; error?: string }) {
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
