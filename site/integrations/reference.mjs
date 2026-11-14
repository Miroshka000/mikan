// Where the API reference lives and what it is called, in each language. Shared by the
// integration that writes the spec it is built from (openapi.mjs), the Astro config that
// builds it and the route middleware that gives every page the reference of its own language
// (src/starlightRoute.ts): starlight-openapi has no languages of its own, so the reference is
// built once for each, and the two share one sidebar.
export const reference = {
	ru: { base: 'api/reference', label: 'Все методы', title: 'Справочник API' },
	en: { base: 'en/api/reference', label: 'All methods', title: 'API reference' },
};
