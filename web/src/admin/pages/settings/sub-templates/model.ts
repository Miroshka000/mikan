import type { Schemas } from "../../../../api/client";
import { t, tMaybe } from "../../../../i18n";

export type Core = Schemas["SubTemplateHead"]["core"];
export type Head = Schemas["SubTemplateHead"];
export type View = Schemas["SubTemplatesView"];
export type Saved = Schemas["SubTemplate"];
export type Ready = Schemas["ReadySubTemplate"];
export type TemplateRouting = Schemas["TemplateRouting"];

export const CORES: Core[] = ["mihomo", "singbox", "xray"];

/** The core's name as the admin knows it. */
export const coreName = (c: Core) => t(`settings.subTemplates.cores.${c}`);

/** A ready template's name and summary in the admin's language; the API's are Russian. */
export const readyName = (r: Pick<Ready, "id" | "name">) => tMaybe(`settings.subTemplates.readyNames.${r.id}`) ?? r.name;
export const readySummary = (r: Ready) => tMaybe(`settings.subTemplates.readyItems.${r.id}`) ?? r.summary;

/** What a template is written as: switches (a simple one), YAML (Clash) or JSON (sing-box, Xray). */
export const kindName = (x: Pick<Head, "kind" | "core">) => (x.kind === "simple" ? t("settings.subTemplates.kinds.simple") : x.core === "mihomo" ? "YAML" : "JSON");

/** A template's name among choices (a response rule's, a group's), with the kind of a simple one:
 * it goes to the apps of every core. */
export const choiceName = (x: Pick<Head, "name" | "kind">) => (x.kind === "simple" ? `${x.name} · ${t("settings.subTemplates.kinds.simple").toLowerCase()}` : x.name);

/** What the apps of a core get when no response rule and no group picks another: their core's
 * default, the Clash default read their way (sing-box and Xray), or the built-in profile of the
 * routing settings. */
export type Served = { by: "own" | "clash"; template: Head } | { by: "builtin" };

export function servedOf(templates: Head[], core: Core): Served {
  const own = templates.find((x) => x.core === core && x.default);
  if (own) return { by: "own", template: own };
  const clash = core === "mihomo" ? undefined : templates.find((x) => x.core === "mihomo" && x.default);
  return clash ? { by: "clash", template: clash } : { by: "builtin" };
}

type GroupLook = { name: string; appearance: { templates: Record<Core, number | null> } };

/** The names of the groups of clients that pick each template for their clients, by its id. */
export function groupsPicking(groups: GroupLook[]): Map<number, string[]> {
  const out = new Map<number, string[]>();
  for (const g of groups) {
    for (const core of CORES) {
      const id = g.appearance.templates[core];
      if (id != null) out.set(id, [...(out.get(id) ?? []), g.name]);
    }
  }
  return out;
}

/** Whether a new template of core is its default: the first one of a core is what its apps get. */
export const firstOfCore = (v: View, core: Core) => !v.templates.some((x) => x.core === core && x.default);

/** Whether a core has room for one more template; a simple one is a Clash template. */
export const hasRoom = (v: View, core: Core) => v.templates.filter((x) => x.core === core).length < v.max;
