// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import changelog from './integrations/changelog.mjs';

// GitHub Pages serves the fork at o0hhs.github.io/mikanaddons/. Links inside the docs are
// written with the base (/mikanaddons/...), and the link validator checks them on every build.
export default defineConfig({
	site: 'https://o0hhs.github.io',
	base: '/mikanaddons',
	trailingSlash: 'always',
	integrations: [
		changelog(),
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
			editLink: { baseUrl: 'https://github.com/o0hhs/mikanaddons/edit/main/site/' },
			lastUpdated: false,
			credits: false,
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
					],
				},
				{
					label: 'Ноды',
					translations: { en: 'Nodes' },
					items: [{ slug: 'nodes/nodes' }, { slug: 'nodes/cascades' }, { slug: 'nodes/warp' }, { slug: 'nodes/turn' }],
				},
				{
					label: 'Протоколы',
					translations: { en: 'Protocols' },
					items: [
						{ slug: 'protocols/presets' },
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
						{ slug: 'subscriptions/happ' },
						{ slug: 'subscriptions/routing' },
					],
				},
				{
					label: 'Пользователи и продажи',
					translations: { en: 'Users and billing' },
					items: [
						{ slug: 'billing/plans' },
						{ slug: 'billing/pools' },
						{ slug: 'billing/telegram' },
						{ slug: 'billing/payments' },
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
						{ slug: 'operations/cli' },
					],
				},
				{
					label: 'Справка',
					translations: { en: 'Reference' },
					items: [{ slug: 'api' }, { slug: 'faq' }, { slug: 'changelog' }, { slug: 'support' }],
				},
			],
			plugins: [starlightLinksValidator({ errorOnLocalLinks: true })],
		}),
	],
	vite: {
		// The screenshots live in .github/assets, outside the site folder.
		server: { fs: { allow: ['..'] } },
	},
});
