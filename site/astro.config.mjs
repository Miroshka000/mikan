// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightOpenAPI, { createOpenAPISidebarGroup } from 'starlight-openapi';
import changelog from './integrations/changelog.mjs';
import openapi, { isReferenceLink, specFile } from './integrations/openapi.mjs';
import { reference } from './integrations/reference.mjs';

// The API reference of each language: starlight-openapi puts its pages in the sidebar where
// these groups stand, and src/starlightRoute.ts shows a page the one of its own language.
const referenceGroup = { ru: createOpenAPISidebarGroup(), en: createOpenAPISidebarGroup() };

// GitHub Pages serves the project at miroshka000.github.io/mikan/. Links inside the docs are
// written with the base (/mikan/...), and the link validator checks them on every build.
const base = '/mikan';

export default defineConfig({
	site: 'https://miroshka000.github.io',
	base,
	trailingSlash: 'always',
	integrations: [
		changelog(),
		// Before Starlight: the reference reads the specs this writes.
		openapi(),
		starlight({
			title: 'mikan',
			description:
				'VPN-панель на ядре mihomo: установка одной командой, ноды и каскады, Telegram-бот с оплатой. Свободная, GPL-3.0.',
			logo: { src: './src/assets/logo.webp', alt: '' },
			favicon: '/favicon.png',
			defaultLocale: 'root',
			locales: {
				root: { label: 'Русский', lang: 'ru' },
				en: { label: 'English', lang: 'en' },
			},
			editLink: { baseUrl: 'https://github.com/Miroshka000/mikan/edit/dev/site/' },
			lastUpdated: false,
			credits: false,
			routeMiddleware: './src/starlightRoute.ts',
			customCss: [
				'@fontsource-variable/onest',
				'@fontsource-variable/unbounded',
				'@fontsource-variable/jetbrains-mono',
				'./src/styles/theme.css',
			],
			components: {
				Head: './src/components/Head.astro',
				Header: './src/components/Header.astro',
				ThemeSelect: './src/components/ThemeSelect.astro',
				LanguageSelect: './src/components/LanguageSelect.astro',
				Footer: './src/components/Footer.astro',
				Hero: './src/components/Hero.astro',
			},
			expressiveCode: {
				// Code is dark in both modes, as in the panel (--code-bg there).
				themes: ['github-dark-default'],
				useStarlightDarkModeSwitch: false,
				useStarlightUiThemeColors: false,
				// Plain blocks for shell commands too: no fake window bar with dots.
				defaultProps: { frame: 'code' },
				styleOverrides: {
					borderRadius: '12px',
					borderColor: 'var(--mk-code-line)',
					codeBackground: 'var(--mk-code-bg)',
					codeForeground: '#e7e9ed',
					codeFontFamily: 'var(--sl-font-mono)',
					codeFontSize: '0.8125rem',
					uiFontFamily: 'var(--sl-font)',
					frames: {
						shadowColor: 'transparent',
						editorActiveTabIndicatorTopColor: 'var(--mk-accent)',
						terminalTitlebarDotsOpacity: '0.35',
					},
				},
			},
			sidebar: [
				{
					label: 'Начало',
					translations: { en: 'Getting started' },
					items: [
						{ slug: 'start/overview' },
						{ slug: 'start/install' },
						{ slug: 'start/first-steps' },
						{ slug: 'start/domain' },
						{ slug: 'start/migrate' },
					],
				},
				{
					label: 'Ноды',
					translations: { en: 'Nodes' },
					items: [
						{ slug: 'nodes/nodes' },
						{ slug: 'nodes/balancing' },
						{ slug: 'nodes/shared-443' },
						{ slug: 'nodes/hardening' },
						{ slug: 'nodes/cascades' },
						{ slug: 'nodes/warp' },
					],
				},
				{
					label: 'Протоколы',
					translations: { en: 'Protocols' },
					items: [
						{ slug: 'protocols/presets' },
						{ slug: 'protocols/cdn' },
						{ slug: 'protocols/autotune' },
						{ slug: 'protocols/behind-proxy' },
						{ slug: 'protocols/xhttp-masking' },
					],
				},
				{
					label: 'Подписки и приложения',
					translations: { en: 'Subscriptions and apps' },
					items: [
						{ slug: 'subscriptions/page' },
						{ slug: 'subscriptions/domain' },
						{ slug: 'subscriptions/happ' },
						{ slug: 'subscriptions/routing' },
						{ slug: 'subscriptions/templates' },
						{ slug: 'subscriptions/clients' },
						{ slug: 'subscriptions/spec' },
					],
				},
				{
					label: 'Пользователи и продажи',
					translations: { en: 'Users and billing' },
					items: [
						{ slug: 'billing/plans' },
						{ slug: 'billing/lifecycle' },
						{ slug: 'billing/pools' },
						{ slug: 'billing/telegram' },
						{ slug: 'billing/payments' },
						{ slug: 'billing/balance' },
						{ slug: 'billing/campaigns' },
						{ slug: 'billing/promo-codes' },
					],
				},
				{
					label: 'Сервер',
					translations: { en: 'Operations' },
					items: [
						{ slug: 'operations/updates' },
						{ slug: 'operations/backups' },
						{ slug: 'operations/import' },
						{ slug: 'operations/alerts' },
						{ slug: 'operations/diagnostics' },
						{ slug: 'operations/filters' },
						{ slug: 'operations/front-page' },
						{ slug: 'operations/community-addons' },
						{ slug: 'operations/support-access' },
						{ slug: 'operations/cli' },
					],
				},
				{
					label: 'API',
					items: [
						{ slug: 'api' },
						{ slug: 'api/create-user' },
						{ slug: 'api/own-bot' },
						{ slug: 'api/sdk' },
						{ slug: 'api/webhooks' },
						{ slug: 'api/stability' },
						referenceGroup.ru,
						referenceGroup.en,
					],
				},
				{
					label: 'Справка',
					translations: { en: 'Reference' },
					items: [{ slug: 'faq' }, { slug: 'changelog' }, { slug: 'support' }, { slug: 'support/paid' }],
				},
			],
			plugins: [
				// It does not know the pages starlight-openapi makes: a link to one is checked by isReferenceLink.
				starlightLinksValidator({ errorOnLocalLinks: true, exclude: ({ link }) => isReferenceLink(link, base) }),
				// The reference reads the web admin's own spec (web/src/api/openapi.json), as openapi.mjs derives it.
				starlightOpenAPI(
					/** @type {const} */ (['ru', 'en']).map((lang) => ({
						base: reference[lang].base,
						schema: specFile(lang),
						sidebar: {
							label: reference[lang].label,
							group: referenceGroup[lang],
							operations: { badges: true, labels: lang === 'en' ? 'operationId' : 'summary' },
						},
					})),
				),
			],
		}),
	],
	vite: {
		// The screenshots live in .github/assets, outside the site folder.
		server: { fs: { allow: ['..'] } },
	},
});
