// Route data of every page (the config's routeMiddleware). The API reference is built twice,
// once per language (integrations/reference.mjs), and starlight-openapi puts both of its
// sidebar groups into the one sidebar the languages share, and makes the previous and next
// links of every page over both. This gives each page the reference of its own language, and
// puts the links as they would be without it.
import { defineRouteMiddleware, type StarlightRouteData } from '@astrojs/starlight/route-data';
import { reference } from '../integrations/reference.mjs';

type Entry = StarlightRouteData['sidebar'][number];
type Group = Extract<Entry, { type: 'group' }>;
type Link = Extract<Entry, { type: 'link' }>;

const labels = new Set(Object.values(reference).map((r) => r.label));

/** What a page takes of the sidebar: the group of its language's reference, whole on the reference's own pages and as one link to its start elsewhere (the tree is some 200 entries). */
function tidy(entries: Entry[], own: string, whole: boolean): Entry[] {
	return entries.flatMap((entry): Entry[] => {
		if (entry.type !== 'group') return [entry];
		if (!labels.has(entry.label)) return [{ ...entry, entries: tidy(entry.entries, own, whole) }];
		if (entry.label !== own) return [];
		if (whole) return [entry];
		// The group's first entry is the reference's start.
		const [start] = entry.entries as [Link];
		return [{ ...start, label: entry.label, isCurrent: false }];
	});
}

function links(entries: Entry[]): Link[] {
	return entries.flatMap((entry) => (entry.type === 'group' ? links(entry.entries) : [entry]));
}

/** The title starlight-openapi gives the start of the reference is "Overview" in every language. The head was made with it already: the page's title and the shared previews say it too. */
function retitle(route: StarlightRouteData, title: string) {
	const old = route.entry.data.title;
	const swap = (text: string | boolean | undefined) => (typeof text === 'string' ? text.replace(old, title) : text);
	route.entry.data.title = title;
	route.head = route.head.map((tag) => ({
		...tag,
		content: swap(tag.content) as string | undefined,
		attrs: tag.attrs && Object.fromEntries(Object.entries(tag.attrs).map(([name, value]) => [name, name === 'content' ? swap(value) : value])),
	}));
}

export const onRequest = defineRouteMiddleware(async (context, next) => {
	const route = context.locals.starlightRoute;
	const own = reference[route.lang === 'en' ? 'en' : 'ru'];
	const path = context.url.pathname.slice(import.meta.env.BASE_URL.length);
	const inReference = path.startsWith(`${own.base}/`);
	// What Starlight made, before starlight-openapi's pass over both groups.
	const pagination = route.pagination;
	await next();
	route.sidebar = tidy(route.sidebar, own.label, inReference);
	if (!inReference) {
		route.pagination = pagination;
		return;
	}
	if (path === `${own.base}/`) retitle(route, own.title);
	const all = links(route.sidebar);
	const at = all.findIndex((link) => link.isCurrent);
	if (at !== -1) route.pagination = { prev: all[at - 1], next: all[at + 1] };
});
