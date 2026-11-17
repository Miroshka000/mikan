import type { Assets } from "../../../lib/site-builder";

/**
 * Where the canvas takes a site's files: the fonts from the panel's own copies, the pictures
 * from the document itself. Nothing is saved or uploaded to look at a site.
 */
export const canvasAssets: Assets = {
  font: (_, file) => file.url,
  image: (_, image) => `data:${image.type};base64,${image.data}`,
};
