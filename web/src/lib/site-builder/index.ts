// The front page builder: a site of a few pages made in the panel and served, as files, by
// the panel and its nodes. The parts: the document (model), a random one (generator), the
// kinds of blocks (blocks/), the pages made of a document (renderer) and the files to save
// (bundle).

export { BLOCK_KINDS, kindByType, kindOf } from "./blocks/registry";
export type { BlockGroup, BlockKind, FieldSpec } from "./blocks/block";
export { bundleSize, SiteBundle, type SiteFile } from "./bundle";
export { FONT_IDS, fontOf } from "./fonts";
export { SiteGenerator } from "./generator";
export { KINDS, newId, parseDoc, slug, type BlockData, type FontId, type ImageType, type Kind, type Lang, type Layout, type Page, type Palette, type SiteDoc, type SiteImage, type Theme } from "./model";
export { SiteRenderer, type Assets } from "./renderer";
export { freshSeed, Rng } from "./rng";
export { palettesOf } from "./themes";
