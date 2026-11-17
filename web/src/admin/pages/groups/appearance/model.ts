// How a group's subscriptions look (#290), as the group's form edits it: what is set goes over
// the panel's settings for the group's clients, the rest stays the panel's and follows it.
import type { Schemas } from "../../../../api/client";

export type Appearance = Schemas["GroupAppearanceView"];
export type AppearanceBody = Schemas["GroupAppearanceBody"];
export type GroupAddress = Schemas["GroupAddressView"];
export type Core = "mihomo" | "singbox" | "xray";

/** The cores a group may pick a template of, in the order the form shows them. */
export const CORES: Core[] = ["mihomo", "singbox", "xray"];

/** "" is the panel's setting; on and off are the group's own. */
export type Tristate = "" | "on" | "off";

/**
 * The appearance as the form holds it. A text left empty is the panel's, but the announcement
 * may be turned off for the group alone (noAnnounce); updateHours is the field's text.
 */
export type AppearanceDraft = {
  templates: Record<Core, number | null>;
  title: string;
  announce: string;
  noAnnounce: boolean;
  announceUrl: string;
  supportUrl: string;
  updateHours: string;
  headers: Schemas["Header"][];
  requireHwid: Tristate;
  unbindOwn: boolean;
  unbind: Schemas["UnbindRules"];
  addresses: GroupAddress[];
};

export function appearanceDraft(a: Appearance): AppearanceDraft {
  return {
    templates: { mihomo: a.templates.mihomo, singbox: a.templates.singbox, xray: a.templates.xray },
    title: a.title ?? "",
    announce: a.announce ?? "",
    noAnnounce: a.announce === "",
    announceUrl: a.announce_url ?? "",
    supportUrl: a.support_url ?? "",
    updateHours: a.update_hours == null ? "" : String(a.update_hours),
    headers: a.headers.map((h) => ({ ...h })),
    requireHwid: a.require_hwid == null ? "" : a.require_hwid ? "on" : "off",
    unbindOwn: a.unbind_own,
    unbind: { ...a.unbind },
    addresses: a.addresses.map((x) => ({ ...x })),
  };
}

/** The hours the field holds, when it is a number the API takes (1 to 168); null otherwise. */
export function updateHours(text: string): number | null {
  const s = text.trim();
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return n >= 1 && n <= 168 ? n : null;
}

/** Whether the form can be sent: the interval is empty or a number the API takes. */
export function draftValid(d: AppearanceDraft): boolean {
  return d.updateHours.trim() === "" || updateHours(d.updateHours) !== null;
}

/** What the form sends: everything left as the panel's is left out. */
export function appearanceBody(d: AppearanceDraft): AppearanceBody {
  const text = (s: string) => (s.trim() === "" ? undefined : s.trim());
  const templates: AppearanceBody["templates"] = {};
  for (const core of CORES) {
    const id = d.templates[core];
    if (id != null) templates[core] = id;
  }
  const body: AppearanceBody = { templates };
  body.title = text(d.title);
  body.announce = d.noAnnounce ? "" : text(d.announce);
  body.announce_url = d.noAnnounce ? undefined : text(d.announceUrl);
  body.support_url = text(d.supportUrl);
  body.update_hours = updateHours(d.updateHours) ?? undefined;
  const headers = d.headers.filter((h) => h.name.trim() !== "").map((h) => ({ name: h.name.trim(), value: h.value }));
  if (headers.length > 0) body.headers = headers;
  if (d.requireHwid !== "") body.require_hwid = d.requireHwid === "on";
  if (d.unbindOwn) body.unbind = d.unbind;
  if (d.addresses.length > 0) body.addresses = d.addresses;
  return body;
}

/** A group's address in place of an inbound's default one, blank: it changes nothing yet. */
export function blankAddress(inbound: number): GroupAddress {
  return { inbound_id: inbound, address: "", port: 0, sni: "", host: "", name: "", cdn: false };
}

/** How many of the appearance's parts are the group's own: what the list of groups tells. */
export function ownParts(a: Appearance): number {
  const parts = [
    a.templates.mihomo != null || a.templates.singbox != null || a.templates.xray != null,
    a.title != null || a.announce != null || a.announce_url != null || a.support_url != null || a.update_hours != null || a.headers.length > 0,
    a.require_hwid != null || a.unbind_own,
    a.page,
    a.addresses.length > 0,
  ];
  return parts.filter(Boolean).length;
}
