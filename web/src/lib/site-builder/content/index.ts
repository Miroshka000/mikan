import type { Kind } from "../model";
import { club } from "./club";
import { gallery } from "./gallery";
import { notes } from "./notes";
import { recipes } from "./recipes";
import { studio } from "./studio";
import { tool } from "./tool";
import type { KindContent } from "./types";

export type { Bank, Item, KindContent, PagePlan, Row, SectionBank } from "./types";
export { MONTHS } from "./types";

/** The words of every kind of site. */
export const CONTENT: Record<Kind, KindContent> = { notes, tool, recipes, club, studio, gallery };
