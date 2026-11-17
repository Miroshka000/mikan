// Writes the specs the API reference is built from, so that no endpoint is listed by hand:
// the panel's own export (web/src/api/openapi.json, kept equal to the code by CI), once for
// each language, with what a reader of the docs needs and the export does not carry:
//   - the number of the release the docs are of (the newest in CHANGELOG.md; the export says
//     "dev", it is made without a release), and an example panel address for the samples;
//   - which key an operation takes. The export says it with the mark x-session-only and the
//     tags of the sign-in and the keys; the reference gets it as a line in the operation's
//     text and in its security, by the rule the admin's own API page uses;
//   - the sections in the order of that page, not in the order the paths happen to sort;
//   - in English, the operations named by their operationId (which is also the name of their
//     function in the SDKs) and a short introduction: the texts of the export are Russian.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { parseChangelog } from './changelog.mjs';
import { reference } from './reference.mjs';

const source = new URL('../../web/src/api/openapi.json', import.meta.url);
const changelog = new URL('../../CHANGELOG.md', import.meta.url);
const generated = new URL('../generated/', import.meta.url);

/** Where the spec of one language is written, as the config hands it to the reference. */
export const specFile = (/** @type {'ru' | 'en'} */ lang) => `./generated/openapi.${lang}.json`;

// The order of the sections on the admin's API page (web/src/admin/pages/api-reference.tsx);
// a section it does not name comes after them, in the order of the spec.
const sections = ['users', 'tariffs', 'payments', 'inbounds', 'node', 'nodes', 'stats', 'settings', 'telegram', 'api-keys', 'auth'];
// Tags whose operations no API key reaches (internal/panel/api/apikeys.go, sessionOnlyTags).
const sessionOnlyTags = ['auth', 'api-keys', 'support'];
const methods = ['get', 'put', 'post', 'delete', 'options', 'head', 'patch', 'trace'];

const texts = {
	ru: {
		title: 'mikan API',
		access: {
			public: 'Открыт без ключа.',
			session: '**Только сессия администратора.** Ключ API получает 403 `session_only`.',
			read: 'Ключ с правами «Чтение» или «Полный доступ».',
			full: 'Ключ с правами «Полный доступ».',
		},
	},
	en: {
		title: 'mikan API',
		access: {
			public: 'Open without a key.',
			session: '**Admin session only.** An API key gets 403 `session_only`.',
			read: 'A key with Read or Full access.',
			full: 'A key with Full access.',
		},
		/** @param {string} base the site's base path */
		description: (base) =>
			"The REST API of the mikan panel. Every path is under the panel's secret admin address, `https://<panel>/<secret path>/api/v1/…`, and the paths below already have the `/api/v1`.\n\n" +
			`Keys, their access levels and the errors are on the [API page](${base}/en/api/). Each operation is named by its \`operationId\`, which is also the name of its function in the [SDKs](${base}/en/api/sdk/). The summaries and the descriptions of operations and fields come from the panel's source code and are in Russian.`,
	},
};

/**
 * Which key an operation takes: public (no key), session (the admin's session only), read
 * (any key) or full (a key with full access).
 * @param {any} op
 * @param {string} method
 */
function access(op, method) {
	if (op.security?.every((/** @type {object} */ s) => Object.keys(s).length === 0)) return 'public';
	if (sessionOnlyTags.includes(op.tags?.[0]) || op['x-session-only']) return 'session';
	return method === 'get' ? 'read' : 'full';
}

/**
 * The spec the reference of one language reads.
 * @param {any} spec the panel's export
 * @param {'ru' | 'en'} lang
 * @param {string} version
 * @param {string} base the site's base path, without a trailing slash
 */
export function derive(spec, lang, version, base) {
	const text = texts[lang];
	const doc = structuredClone(spec);
	doc.info = { ...doc.info, title: text.title, version };
	if (lang === 'en') doc.info.description = texts.en.description(base);
	doc.servers = [
		{
			url: 'https://{panel}/{secret_path}',
			variables: {
				panel: { default: 'vpn.example.com:8443', description: "The panel's address and port" },
				secret_path: { default: 'SECRET-PATH', description: "The secret path of the panel's admin address" },
			},
		},
	];
	/** @type {Set<string>} */
	const tags = new Set();
	for (const item of Object.values(doc.paths)) {
		for (const method of methods.filter((m) => item[m])) {
			const op = item[method];
			if (op.tags?.[0]) tags.add(op.tags[0]);
			const kind = access(op, method);
			// The English reference names an operation by its id: the summary is Russian, and
			// stays as the first line of its text.
			const summary = lang === 'en' ? op.summary : undefined;
			if (lang === 'en') delete op.summary;
			op.description = [text.access[kind], summary, op.description].filter(Boolean).join('\n\n');
			if (kind === 'public') op.security = [];
			if (kind === 'session') op.security = [{ session: [] }];
		}
	}
	doc.tags = [...sections.filter((name) => tags.has(name)), ...[...tags].filter((name) => !sections.includes(name))].map((name) => ({ name }));
	return doc;
}

function render(/** @type {string} */ base) {
	const spec = JSON.parse(readFileSync(source, 'utf8'));
	const newest = parseChangelog(readFileSync(changelog, 'utf8'))[0]?.version;
	mkdirSync(generated, { recursive: true });
	for (const lang of /** @type {const} */ (['ru', 'en'])) {
		const doc = derive(spec, lang, newest ?? spec.info.version, base);
		writeFileSync(new URL(`openapi.${lang}.json`, generated), `${JSON.stringify(doc, null, 2)}\n`);
	}
}

/** @type {Set<string> | undefined} */
let operationIds;

/**
 * Whether a link from the docs points at a page the reference has. The links validator knows
 * only the pages of the content collection, so the links to the reference are left to it with
 * this: it says no to a link to an operation the spec does not have, and the validator then
 * reports it.
 * @param {string} link as authored, with the site's base
 * @param {string} base the site's base path, without a trailing slash
 */
export function isReferenceLink(link, base) {
	const path = link.split(/[?#]/)[0] ?? link;
	operationIds ??= new Set(
		Object.values(JSON.parse(readFileSync(new URL('openapi.ru.json', generated), 'utf8')).paths).flatMap((item) =>
			methods.filter((m) => item[m]).map((m) => item[m].operationId.toLowerCase()),
		),
	);
	return Object.values(reference).some(({ base: dir }) => {
		const root = `${base}/${dir}/`;
		if (path === root) return true;
		const operation = path.startsWith(`${root}operations/`) ? path.slice(`${root}operations/`.length).replace(/\/$/, '') : '';
		return operationIds?.has(operation) ?? false;
	});
}

/** @returns {import('astro').AstroIntegration} */
export default function openapi() {
	return {
		name: 'mikan-openapi',
		hooks: {
			'astro:config:setup': ({ config, addWatchFile }) => {
				render(config.base.replace(/\/$/, ''));
				// A change to either restarts the dev server, which writes the specs again.
				addWatchFile(fileURLToPath(source));
				addWatchFile(fileURLToPath(changelog));
			},
		},
	};
}
