// What the forms of sellable things (tariffs, traffic packages, grants) check before they
// send. The bounds are the API's own (its validation tags); a form that says no earlier than
// the server saves a round trip, and one place to change them keeps the forms alike.

/** A price in Telegram Stars, a whole number. */
const STARS_MIN = 1;
const STARS_MAX = 10_000;

/** A price in rubles, in kopecks: from a ruble to a million. */
const RUB_KOPECKS_MIN = 100;
const RUB_KOPECKS_MAX = 100_000_000;

/** The size of a traffic package or a grant, in GiB. */
const TRAFFIC_GB_MIN = 1;
const TRAFFIC_GB_MAX = 102_400;

/** How long a package or a grant lasts when it runs for a number of days. */
const LIFETIME_DAYS_MIN = 1;
const LIFETIME_DAYS_MAX = 3650;
/** The days offered as one-tap choices beside that field. */
export const LIFETIME_DAYS_PRESETS = [7, 30, 90] as const;

/** A number as people type it: the comma of "12,5" is a decimal point too. NaN when it is not one. */
export function parseDecimal(text: string): number {
  return Number(text.replace(",", "."));
}

/** Rubles as typed ("199,50") to kopecks. */
export function rubKopecks(text: string): number {
  return Math.round(parseDecimal(text) * 100);
}

export const isStars = (n: number) => Number.isInteger(n) && n >= STARS_MIN && n <= STARS_MAX;
export const isKopecks = (n: number) => Number.isFinite(n) && n >= RUB_KOPECKS_MIN && n <= RUB_KOPECKS_MAX;
export const isTrafficGB = (gb: number) => Number.isFinite(gb) && gb >= TRAFFIC_GB_MIN && gb <= TRAFFIC_GB_MAX;
export const isLifetimeDays = (n: number) => Number.isInteger(n) && n >= LIFETIME_DAYS_MIN && n <= LIFETIME_DAYS_MAX;
