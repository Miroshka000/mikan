/**
 * Prices in the panel are all a tariff's: Stars a whole number, rubles typed as rubles and
 * sent as kopecks. The bounds are the API's (domain.MinPriceStars and friends), and the
 * tariff, package and trial forms check their price fields through here.
 */
export const minPriceStars = 1;
export const maxPriceStars = 10_000;
export const minPriceRub = 100; // kopecks: a ruble
export const maxPriceRub = 100_000_000;

/** kopecks: a price typed in rubles, with a comma or a dot, in kopecks. */
export function kopecks(typed: string): number {
  return Math.round(Number(typed.replace(",", ".")) * 100);
}

/** badStars: what is typed is not a price in Stars; an empty field is no price at all. */
export function badStars(typed: string): boolean {
  if (!typed.trim()) return false;
  const n = Number(typed);
  return !Number.isInteger(n) || n < minPriceStars || n > maxPriceStars;
}

/** badRub: the same for a price in rubles. */
export function badRub(typed: string): boolean {
  if (!typed.trim()) return false;
  const n = kopecks(typed);
  return !Number.isFinite(n) || n < minPriceRub || n > maxPriceRub;
}
