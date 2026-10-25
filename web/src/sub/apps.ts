export type Platform = "ios" | "android" | "windows" | "macos" | "linux";

type App = {
  /** The id of the panel's list of known apps (apps.Catalog in internal/sub/apps): what the admin blocked is left out of the page. */
  id: string;
  name: string;
  note: "easiest" | "free" | "stable" | "openSource" | "modern" | "bestWindows" | "tun" | "oneButton" | "allProtocols" | "everything";
  link: (url: string, brand: string, happ?: string) => string;
};
export const enc = encodeURIComponent;
const clash = (url: string, brand: string) => `clash://install-config?url=${enc(url)}&name=${enc(brand)}`;
const slothClash = (url: string, brand: string) => `slothclash://install-config?url=${enc(url)}&name=${enc(brand)}`;
// Mikan (Android) and Kabosu (desktop) are mikan's own apps, each with a scheme no other
// Clash app takes: the profile lands in them, not in whichever app holds clash://.
const mikan = (url: string, brand: string) => `mikan://install-config?url=${enc(url)}&name=${enc(brand)}`;
const kabosu = (url: string, brand: string) => `kabosu://install-config?url=${enc(url)}&name=${enc(brand)}`;
const incy = (url: string) => `incy://add/${url}`;

// Happ opens the crypt link when the panel gives one: the address stays hidden.
const happ = (u: string, _brand: string, crypt?: string) => crypt || `happ://add/${u}`;

export const APPS: Record<Platform, App[]> = {
  ios: [
    { id: "happ", name: "Happ", note: "easiest", link: happ },
    { id: "incy", name: "INCY", note: "modern", link: incy },
    { id: "streisand", name: "Streisand", note: "free", link: (u, b) => `streisand://import/${u}#${enc(b)}` },
    { id: "v2raytun", name: "v2RayTun", note: "stable", link: (u) => `v2raytun://import/${u}` },
  ],
  android: [
    { id: "mikan", name: "Mikan", note: "everything", link: mikan },
    { id: "happ", name: "Happ", note: "easiest", link: happ },
    { id: "clashfest", name: "ClashFest", note: "allProtocols", link: (u, b) => `clashfest://install-config?url=${enc(u)}&name=${enc(b)}` },
    { id: "incy", name: "INCY", note: "modern", link: incy },
    { id: "v2raytun", name: "v2RayTun", note: "stable", link: (u) => `v2raytun://import/${u}` },
    { id: "hiddify", name: "Hiddify", note: "openSource", link: (u, b) => `hiddify://import/${u}#${enc(b)}` },
  ],
  windows: [
    { id: "kabosu", name: "Kabosu", note: "tun", link: kabosu },
    { id: "koala", name: "Koala Clash", note: "bestWindows", link: (u, b) => `koala-clash://install-config?url=${enc(u)}&name=${enc(b)}` },
    { id: "slothclash", name: "SlothClash", note: "tun", link: slothClash },
    { id: "hiddify", name: "Hiddify", note: "easiest", link: (u, b) => `hiddify://import/${u}#${enc(b)}` },
    { id: "verge", name: "Clash Verge Rev", note: "tun", link: clash },
  ],
  macos: [
    { id: "kabosu", name: "Kabosu", note: "tun", link: kabosu },
    { id: "verge", name: "Clash Verge Rev", note: "tun", link: clash },
    { id: "slothclash", name: "SlothClash", note: "tun", link: slothClash },
    { id: "happ", name: "Happ", note: "oneButton", link: happ },
    { id: "hiddify", name: "Hiddify", note: "openSource", link: (u, b) => `hiddify://import/${u}#${enc(b)}` },
  ],
  linux: [
    { id: "kabosu", name: "Kabosu", note: "tun", link: kabosu },
    { id: "slothclash", name: "SlothClash", note: "tun", link: slothClash },
    { id: "verge", name: "Clash Verge Rev", note: "tun", link: clash },
    { id: "hiddify", name: "Hiddify", note: "openSource", link: (u, b) => `hiddify://import/${u}#${enc(b)}` },
  ],
};

export function detect(): Platform {
  const ua = navigator.userAgent;
  if (/iPhone|iPad|iPod/.test(ua)) return "ios";
  if (/Android/.test(ua)) return "android";
  if (/Mac OS X/.test(ua)) return "macos";
  if (/Windows/.test(ua)) return "windows";
  if (/Linux|X11|CrOS/.test(ua)) return "linux"; // after Android: its browsers say Linux too
  return "android";
}
