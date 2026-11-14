import { Select } from "../../../components/select";
import { Field, Segmented } from "../../../components/ui";
import { t, tMaybe } from "../../../i18n";
import { FONT_IDS, fontOf, palettesOf, type FontId, type Layout, type Palette } from "../../../lib/site-builder";
import { useEditor, type EditorStore } from "./store";

/** A colour for the browser's picker, which takes #rrggbb only. */
function sixHex(c: string): string {
  if (/^#[0-9a-f]{6}$/i.test(c)) return c;
  const m = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(c);
  return m ? `#${m[1]}${m[1]}${m[2]}${m[2]}${m[3]}${m[3]}` : "#000000";
}

const COLORS: (keyof Palette)[] = ["accent", "bg", "surface", "ink", "muted", "line"];
const LAYOUT: { key: keyof Layout; options: string[] }[] = [
  { key: "header", options: ["bar", "centered"] },
  { key: "sections", options: ["stack", "aside"] },
  { key: "hero", options: ["plain", "band"] },
];

/** The looks of the whole site: its name and lines, its colours, its type and how a page is put together. */
export function DesignPanel({ store }: { store: EditorStore }) {
  const { doc } = useEditor(store);
  const theme = doc.theme;
  const text = (key: "brand" | "tag" | "foot", max: number) => (
    <Field label={t(`siteBuilder.${key}`)} htmlFor={`sb-${key}`}>
      <input id={`sb-${key}`} className="input" value={doc[key]} maxLength={max} onChange={(e) => store.setSite({ [key]: e.target.value }, { typing: "site:" + key })} />
    </Field>
  );
  return (
    <div className="flex flex-col gap-4">
      <section className="flex flex-col gap-3">
        <h3 className="sb-h">{t("siteBuilder.site")}</h3>
        {text("brand", 60)}
        {text("tag", 120)}
        {text("foot", 160)}
      </section>
      <section className="flex flex-col gap-3">
        <h3 className="sb-h">{t("siteBuilder.palette")}</h3>
        <div className="flex flex-wrap gap-2" role="group" aria-label={t("siteBuilder.palette")}>
          {palettesOf(doc.kind).map((p, i) => (
            <button key={i} type="button" className="sb-swatch" aria-pressed={p.bg === theme.palette.bg && p.accent === theme.palette.accent} aria-label={t("siteBuilder.paletteN", { n: i + 1 })} onClick={() => store.setTheme({ palette: { ...p } })}>
              <span style={{ background: p.bg }} />
              <span style={{ background: p.surface }} />
              <span style={{ background: p.accent }} />
              <span style={{ background: p.ink }} />
            </button>
          ))}
        </div>
        <div className="grid grid-cols-3 gap-2">
          {COLORS.map((key) => (
            <Field key={key} label={t(`siteBuilder.color.${key}`)} htmlFor={`sb-c-${key}`}>
              <input id={`sb-c-${key}`} type="color" className="sb-color" value={sixHex(theme.palette[key])} onChange={(e) => store.setTheme({ palette: { ...theme.palette, [key]: e.target.value } })} />
            </Field>
          ))}
        </div>
      </section>
      <section className="flex flex-col gap-3">
        <h3 className="sb-h">{t("siteBuilder.type")}</h3>
        {(["heading", "body"] as const).map((key) => (
          <Field key={key} label={t(`siteBuilder.font.${key}`)} htmlFor={`sb-f-${key}`}>
            <Select id={`sb-f-${key}`} className="input" value={theme[key]} onChange={(e) => store.setTheme({ [key]: e.target.value as FontId })}>
              {FONT_IDS.map((f) => (
                <option key={f} value={f}>
                  {fontOf(f).name || t(`siteBuilder.fonts.${f}`)}
                </option>
              ))}
            </Select>
          </Field>
        ))}
      </section>
      <section className="flex flex-col gap-3">
        <h3 className="sb-h">{t("siteBuilder.layout")}</h3>
        {LAYOUT.map(({ key, options }) => (
          <Field key={key} label={t(`siteBuilder.layoutOf.${key}`)}>
            <Segmented label={t(`siteBuilder.layoutOf.${key}`)} value={theme.layout[key]} options={options.map((o) => ({ value: o, label: tMaybe(`siteBuilder.opt.${o}`) ?? o }))} onChange={(v) => store.setTheme({ layout: { ...theme.layout, [key]: v } as Layout })} />
          </Field>
        ))}
        <Range id="sb-radius" label={t("siteBuilder.radius")} value={theme.radius} min={0} max={24} step={2} onChange={(radius) => store.setTheme({ radius })} />
        <Range id="sb-width" label={t("siteBuilder.width")} value={theme.width} min={640} max={1240} step={40} onChange={(width) => store.setTheme({ width })} />
      </section>
    </div>
  );
}

function Range({ id, label, value, min, max, step, onChange }: { id: string; label: string; value: number; min: number; max: number; step: number; onChange: (v: number) => void }) {
  return (
    <Field label={`${label}: ${value} px`} htmlFor={id}>
      <input id={id} type="range" className="w-full" min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} />
    </Field>
  );
}
