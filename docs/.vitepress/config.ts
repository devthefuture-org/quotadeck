import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'QuotaDeck',
  description: 'Local-first quota intelligence for Claude, Codex, and Z.ai.',
  base: '/quotadeck/',
  cleanUrls: true,
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/quotadeck/quotadeck.svg' }],
    ['meta', { name: 'theme-color', content: '#151812' }],
  ],
  themeConfig: {
    logo: '/quotadeck.svg',
    nav: [
      { text: 'Guide', link: '/getting-started' },
      { text: 'Providers', link: '/providers' },
      { text: 'Desktop', link: '/desktop' },
      { text: 'Releases', link: 'https://github.com/devthefuture-org/quotadeck/releases' },
    ],
    sidebar: [
      {
        text: 'Get started',
        items: [
          { text: 'Introduction', link: '/' },
          { text: 'Installation', link: '/getting-started' },
          { text: 'Configuration', link: '/configuration' },
          { text: 'Providers', link: '/providers' },
        ],
      },
      {
        text: 'Use QuotaDeck',
        items: [
          { text: 'Desktop & Cinnamon', link: '/desktop' },
          { text: 'Troubleshooting', link: '/troubleshooting' },
          { text: 'Security', link: '/security' },
        ],
      },
      {
        text: 'Project',
        items: [
          { text: 'Architecture', link: '/architecture' },
          { text: 'Contributing', link: '/contributing' },
          { text: 'ADR 0001 · References', link: '/adr/0001-reference-projects' },
          { text: 'ADR 0002 · Domain model', link: '/adr/0002-domain-model' },
        ],
      },
    ],
    socialLinks: [
      { icon: 'github', link: 'https://github.com/devthefuture-org/quotadeck' },
    ],
    editLink: {
      pattern: 'https://github.com/devthefuture-org/quotadeck/edit/main/docs/:path',
      text: 'Edit this page on GitHub',
    },
    footer: {
      message: 'Local-first. Zero telemetry.',
      copyright: 'Released under the MIT License.',
    },
    search: { provider: 'local' },
  },
})
