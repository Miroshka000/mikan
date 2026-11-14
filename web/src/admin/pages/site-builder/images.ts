import type { SiteDoc, SiteImage } from "../../../lib/site-builder";

/** The longest side a picture of a site keeps, and how hard it is packed. */
const MAX_SIDE = 1600;
const QUALITY = 0.82;

/**
 * A picture file as a site keeps it: drawn again in the browser no larger than a page shows
 * it, as JPEG. What the file carried besides the picture (where and with what it was taken)
 * does not survive the redraw, so nothing of the admin's camera reaches a visitor.
 */
export async function readImage(file: File): Promise<SiteImage> {
  const bitmap = await createImageBitmap(file);
  const scale = Math.min(1, MAX_SIDE / Math.max(bitmap.width, bitmap.height));
  const w = Math.max(1, Math.round(bitmap.width * scale));
  const h = Math.max(1, Math.round(bitmap.height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("no canvas");
  // A picture with see-through parts gets a white ground: JPEG has none.
  ctx.fillStyle = "#ffffff";
  ctx.fillRect(0, 0, w, h);
  ctx.drawImage(bitmap, 0, 0, w, h);
  bitmap.close();
  const url = canvas.toDataURL("image/jpeg", QUALITY);
  return { type: "image/jpeg", data: url.slice(url.indexOf(",") + 1), w, h };
}

/** The document without the pictures no block shows any more. */
export function pruneImages(doc: SiteDoc): SiteDoc {
  const text = JSON.stringify(doc.pages);
  const images = Object.fromEntries(Object.entries(doc.images).filter(([id]) => text.includes(`"${id}"`)));
  return { ...doc, images };
}
