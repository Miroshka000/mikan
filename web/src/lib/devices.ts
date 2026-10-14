import { appName } from "./format";

/** Operating systems that get a laptop icon; the rest get a phone. */
export const desktopOS = /windows|mac|linux|darwin/i;

/** A bound device: `name` is its own (the admin's or the subscriber's), "" when none. */
type Described = { name?: string; os: string; os_version: string; model: string; app: string };

/** The longest own name of a device, as the API takes it. */
export const DEVICE_NAME_MAX = 40;

/** What the app reported a device to be: its model, else its system, else the app. */
export function reportedName(d: Described): string {
  return d.model || [d.os, d.os_version].filter(Boolean).join(" ") || appName(d.app);
}

/** What to call a device: its own name, else what its app reported, else `fallback`. */
export function deviceLabel(d: Described, fallback: string): string {
  return d.name || reportedName(d) || fallback;
}

/** The line under a device's name: what the app reported that the name does not say. */
export function deviceDetails(d: Described, named: boolean): string {
  const system = [d.os, d.os_version].filter(Boolean).join(" ");
  const parts = d.name ? [d.model, system] : named && d.model ? [system] : [];
  return [...parts, appName(d.app)].filter(Boolean).join(" · ");
}
