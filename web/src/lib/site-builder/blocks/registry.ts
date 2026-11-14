import type { BlockData } from "../model";
import type { BlockKind } from "./block";
import { CodeBlock } from "./code";
import { CtaBlock } from "./cta";
import { FaqBlock } from "./faq";
import { GalleryBlock } from "./gallery";
import { HeroBlock } from "./hero";
import { ListBlock } from "./list";
import { PricingBlock } from "./pricing";
import { ProseBlock } from "./prose";
import { QuotesBlock } from "./quotes";
import { SplitBlock } from "./split";
import { StatsBlock } from "./stats";
import { TableBlock } from "./table";

/** Every kind of block, in the order the editor's library lists them. */
export const BLOCK_KINDS: BlockKind[] = [
  new HeroBlock(),
  new CtaBlock(),
  new ListBlock(),
  new ProseBlock(),
  new TableBlock(),
  new CodeBlock(),
  new SplitBlock(),
  new GalleryBlock(),
  new PricingBlock(),
  new FaqBlock(),
  new QuotesBlock(),
  new StatsBlock(),
] as BlockKind[];

const BY_TYPE = new Map(BLOCK_KINDS.map((k) => [k.type, k]));

/** The kind of a saved block; undefined for one this version does not know (it is skipped). */
export const kindOf = (b: BlockData): BlockKind | undefined => BY_TYPE.get(b.t);
export const kindByType = (type: string): BlockKind | undefined => BY_TYPE.get(type);
