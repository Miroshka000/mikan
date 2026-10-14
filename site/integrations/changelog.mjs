// Builds the changelog pages from the repository's CHANGELOG.md, so the site never keeps a
// second copy by hand. Every release there has a "### en" and a "### ru" section; each
// language gets its own page with one heading per release.
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const source = new URL('../../CHANGELOG.md', import.meta.url);
const docs = new URL('../src/content/docs/', import.meta.url);

const pages = {
	ru: {
		file: new URL('changelog.md', docs),
		title: 'Список изменений',
		description: 'Что менялось в mikan от версии к версии.',
		intro:
			'Тот же список, что панель показывает в «Настройки → Основное → Обновления». Образы и установщик каждой версии лежат в [релизах на GitHub](https://github.com/o0hhs/mikanaddons/releases).',
	},
	en: {
		file: new URL('en/changelog.md', docs),
		title: 'Changelog',
		description: 'What changed in mikan from release to release.',
		intro:
			'The same list the panel shows in Settings → General → Updates. Images and the installer of every version are in the [GitHub releases](https://github.com/o0hhs/mikanaddons/releases).',
	},
};

/** @param {string} text */
export function parseChangelog(text) {
	/** @type {{ version: string, en: string[], ru: string[] }[]} */
	const releases = [];
	let current = null;
	/** @type {'en' | 'ru' | null} */
	let lang = null;
	for (const line of text.split(/\r?\n/)) {
		const release = line.match(/^## (.+)$/);
		if (release) {
			current = { version: release[1].trim(), en: [], ru: [] };
			releases.push(current);
			lang = null;
			continue;
		}
		const section = line.match(/^### (en|ru)\s*$/);
		if (section) {
			lang = /** @type {'en' | 'ru'} */ (section[1]);
			continue;
		}
		if (current && lang) current[lang].push(line);
	}
	return releases;
}

function render() {
	const releases = parseChangelog(readFileSync(source, 'utf8'));
	for (const [lang, page] of Object.entries(pages)) {
		const body = releases
			.map((r) => `## ${r.version}\n\n${r[/** @type {'en' | 'ru'} */ (lang)].join('\n').trim()}\n`)
			.join('\n');
		const out = [
			'---',
			`title: ${page.title}`,
			`description: ${page.description}`,
			'editUrl: false',
			'tableOfContents:',
			'  maxHeadingLevel: 2',
			'---',
			'',
			'<!-- Generated from CHANGELOG.md by integrations/changelog.mjs: edit that file instead. -->',
			'',
			page.intro,
			'',
			body,
		].join('\n');
		mkdirSync(new URL('.', page.file), { recursive: true });
		writeFileSync(page.file, out);
	}
}

/** @returns {import('astro').AstroIntegration} */
export default function changelog() {
	return {
		name: 'mikan-changelog',
		hooks: {
			'astro:config:setup': ({ addWatchFile }) => {
				render();
				addWatchFile(fileURLToPath(source));
			},
			'astro:server:setup': ({ server }) => {
				server.watcher.add(fileURLToPath(source));
				server.watcher.on('change', (file) => {
					if (file === fileURLToPath(source)) render();
				});
			},
		},
	};
}
