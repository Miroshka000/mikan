import { ArrowDown, ArrowUp, ImagePlus, Plus, Trash2 } from "lucide-react";
import { useId, useRef } from "react";
import { Select } from "../../../components/select";
import { useToast } from "../../../components/toast";
import { Button, Field, Segmented } from "../../../components/ui";
import { t, tMaybe } from "../../../i18n";
import type { FieldSpec } from "../../../lib/site-builder";
import { readImage } from "./images";
import { getAt } from "./paths";
import type { EditorStore } from "./store";

/** The name of a field: the editor has words for every key a block declares. */
const label = (key: string) => tMaybe(`siteBuilder.f.${key}`) ?? key;

type Props = {
  store: EditorStore;
  /** The block the fields belong to, and the data the paths are read from. */
  blockId: string;
  data: unknown;
  fields: FieldSpec[];
  /** The path of `data` inside the block: "" for the block itself, "items.2" for a row. */
  prefix?: string;
};

/**
 * The fields of a block, drawn from what its kind declares: one form for every kind, so a
 * new kind of block needs no form of its own.
 */
export function FieldsForm({ store, blockId, data, fields, prefix = "" }: Props) {
  return (
    <div className="flex flex-col gap-3">
      {fields.map((f) => (
        <FieldControl key={f.key} store={store} blockId={blockId} data={data} field={f} path={prefix ? `${prefix}.${f.key}` : f.key} />
      ))}
    </div>
  );
}

function FieldControl({ store, blockId, data, field, path }: { store: EditorStore; blockId: string; data: unknown; field: FieldSpec; path: string }) {
  const id = useId();
  const value = getAt(data, field.key);
  const set = (v: unknown) => store.setIn(blockId, path, v);
  switch (field.kind) {
    case "text":
      return (
        <Field label={label(field.key)} htmlFor={id}>
          <input id={id} className="input" value={typeof value === "string" ? value : ""} maxLength={field.max} onChange={(e) => set(e.target.value)} />
        </Field>
      );
    case "area":
      return (
        <Field label={label(field.key)} htmlFor={id}>
          <textarea id={id} className="input" rows={3} value={typeof value === "string" ? value : ""} maxLength={field.max} onChange={(e) => set(e.target.value)} />
        </Field>
      );
    case "lines":
    case "paras": {
      const list = Array.isArray(value) ? (value as string[]) : [];
      const paras = field.kind === "paras";
      return (
        <Field label={label(field.key)} htmlFor={id} hint={t(paras ? "siteBuilder.parasHint" : "siteBuilder.linesHint")}>
          <textarea
            id={id}
            className={`input${field.mono ? " mono" : ""}`}
            rows={6}
            value={list.join(paras ? "\n\n" : "\n")}
            onChange={(e) => set(paras ? e.target.value.split(/\n\s*\n/) : e.target.value.split("\n"))}
          />
        </Field>
      );
    }
    case "choice":
      return (
        <Field label={label(field.key)}>
          <Segmented label={label(field.key)} value={typeof value === "string" ? value : field.options[0]!} options={field.options.map((o) => ({ value: o, label: tMaybe(`siteBuilder.opt.${o}`) ?? o }))} onChange={(v) => set(v)} />
        </Field>
      );
    case "page":
      return (
        <Field label={label(field.key)} htmlFor={id}>
          <Select id={id} className="input" value={typeof value === "string" ? value : ""} onChange={(e) => set(e.target.value)}>
            {store.doc.pages.map((p) => (
              <option key={p.id} value={p.path}>
                {p.title}
              </option>
            ))}
          </Select>
        </Field>
      );
    case "image":
      return <ImageField store={store} name={label(field.key)} value={typeof value === "string" ? value : ""} onChange={set} />;
    case "items": {
      const rows = Array.isArray(value) ? (value as unknown[]) : [];
      const write = (next: unknown[]) => set(next);
      const empty = Object.fromEntries(field.of.map((f) => [f.key, f.kind === "lines" || f.kind === "paras" ? [] : ""]));
      return (
        <div className="flex flex-col gap-2">
          <span className="lbl">{label(field.key)}</span>
          {rows.map((row, i) => (
            <div key={i} className="sb-row">
              <div className="min-w-0 flex-1">
                <FieldsForm store={store} blockId={blockId} data={row} fields={field.of} prefix={`${path}.${i}`} />
              </div>
              <div className="flex flex-none flex-col gap-1">
                <IconButton label={t("siteBuilder.up")} disabled={i === 0} onClick={() => write(swap(rows, i, i - 1))}>
                  <ArrowUp size={16} />
                </IconButton>
                <IconButton label={t("siteBuilder.down")} disabled={i === rows.length - 1} onClick={() => write(swap(rows, i, i + 1))}>
                  <ArrowDown size={16} />
                </IconButton>
                <IconButton label={t("siteBuilder.remove")} onClick={() => write(rows.filter((_, j) => j !== i))}>
                  <Trash2 size={16} />
                </IconButton>
              </div>
            </div>
          ))}
          <Button size="sm" className="self-start" onClick={() => write([...rows, empty])}>
            <Plus size={16} aria-hidden /> {t("siteBuilder.addRow")}
          </Button>
        </div>
      );
    }
  }
}

function swap<T>(list: T[], i: number, j: number): T[] {
  const out = [...list];
  [out[i], out[j]] = [out[j]!, out[i]!];
  return out;
}

export function IconButton({ label: name, disabled, onClick, children }: { label: string; disabled?: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" className="icon-btn" aria-label={name} title={name} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  );
}

/** A picture of the site: chosen from the admin's files, shown small, or taken away. */
function ImageField({ store, name, value, onChange }: { store: EditorStore; name: string; value: string; onChange: (id: string) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const toast = useToast();
  const image = value ? store.doc.images[value] : undefined;
  const pick = async (file?: File) => {
    if (!file) return;
    try {
      onChange(store.addImage(await readImage(file)));
    } catch {
      toast.error(t("siteBuilder.imageFailed"));
    }
  };
  return (
    <Field label={name} hint={t("siteBuilder.imageHint")}>
      <div className="flex items-center gap-2">
        {image ? <img className="sb-thumb" src={`data:${image.type};base64,${image.data}`} alt="" /> : null}
        <Button size="sm" onClick={() => input.current?.click()}>
          <ImagePlus size={16} aria-hidden /> {image ? t("siteBuilder.imageChange") : t("siteBuilder.imageAdd")}
        </Button>
        {image ? (
          <IconButton label={t("siteBuilder.remove")} onClick={() => onChange("")}>
            <Trash2 size={16} />
          </IconButton>
        ) : null}
        <input ref={input} type="file" accept="image/jpeg,image/png,image/webp" className="sr-only" tabIndex={-1} aria-hidden onChange={(e) => { void pick(e.target.files?.[0]); e.target.value = ""; }} />
      </div>
    </Field>
  );
}
