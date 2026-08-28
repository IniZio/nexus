// VitePress config for the nexus3 documentation site.
//
// Structure follows microsandbox's docs (docs.microsandbox.dev): a short
// getting-started path, then concepts separated from operational guides, with
// the CLI surface in its own section. Every page carries a one-line description
// in its frontmatter, which is what makes the index legible at a glance.
//
// The site is deliberately MINIMAL (D-PD-43): it introduces the spec. It is not
// an exhaustive per-flag reference, and the reference-tool comparison is
// motive-internal and is never published here.

import { withMermaid } from 'vitepress-plugin-mermaid'

// The site is published to GitHub Pages at https://inizio.github.io/nexus3/,
// so every asset URL has to be prefixed with the repository name. Overridable
// via DOCS_BASE for a different host (a custom domain would use '/').
const base = process.env.DOCS_BASE ?? '/nexus3/'

export default withMermaid({
  title: 'nexus3',
  description: 'MicroVM sandboxes for parallel agent development',
  lang: 'en-US',
  cleanUrls: true,
  base,

  // Build output and cache live outside the source tree and are gitignored.
  outDir: '.vitepress/dist',
  cacheDir: '.vitepress/cache',

  // A broken internal link is a docs bug — fail the build rather than ship it.
  ignoreDeadLinks: false,

  // Favicon served from docs/site/public/favicon.svg (VitePress copies public/
  // to the site root at both build and dev time). head[] hrefs are emitted
  // verbatim — VitePress does not prefix them — so `base` is applied by hand.
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}favicon.svg` }],
  ],

  // Pin the dev server to a fixed port so the URL is always predictable.
  // strictPort: true causes a hard failure instead of drifting to another port.
  //
  // optimizeDeps.include: ['mermaid'] — pre-bundle mermaid and all its deps
  // (including dayjs) together. In pnpm's isolated layout dayjs has no
  // top-level node_modules symlink, so Vite cannot find it on its own and
  // falls back to serving dayjs.min.js (CJS) directly, which has no named
  // `default` export and breaks the dev server. Pre-bundling mermaid resolves
  // dayjs from mermaid's own per-package node_modules where it is visible.
  //
  // ssr.noExternal: ['mermaid'] — prevent VitePress from treating mermaid as
  // a Node SSR external; without this the dev server can still bypass the
  // pre-bundle on the SSR leg and hit the same CJS/ESM mismatch.
  vite: {
    server: {
      port: 5180,
      strictPort: true,
    },
    optimizeDeps: {
      include: ['mermaid'],
    },
    ssr: {
      noExternal: ['mermaid', 'vitepress-plugin-mermaid'],
    },
  },

  themeConfig: {
    search: {
      provider: 'local',
    },

    nav: [
      { text: 'Documentation', link: '/quickstart' },
      { text: 'CLI Reference', link: '/cli/' },
      { text: 'Security', link: '/security/egress-and-perimeter' },
    ],

    sidebar: [
      {
        text: 'Getting Started',
        items: [
          { text: 'Introduction', link: '/' },
          { text: 'Quickstart', link: '/quickstart' },
          { text: 'AI agents', link: '/ai-agents' },
        ],
      },
      {
        text: 'Sandboxes',
        collapsed: false,
        items: [
          { text: 'Overview', link: '/sandboxes/' },
          { text: 'Sandbox model', link: '/sandboxes/sandbox-model' },
          { text: 'Lifecycle states', link: '/sandboxes/lifecycle-states' },
          { text: 'Execution substrate', link: '/sandboxes/execution-substrate' },
          { text: 'Images', link: '/sandboxes/images' },
          { text: 'Guest agent', link: '/sandboxes/guest-agent' },
          { text: 'Snapshots and fork', link: '/sandboxes/snapshots-and-fork' },
        ],
      },
      {
        text: 'CLI Reference',
        collapsed: false,
        items: [
          { text: 'Overview', link: '/cli/' },
          { text: 'Configuration', link: '/cli/configuration' },
          { text: 'Sandbox commands', link: '/cli/sandbox-commands' },
          { text: 'Exec, SSH, and forward', link: '/cli/exec-ssh-forward' },
          { text: 'Image commands', link: '/cli/image-commands' },
          { text: 'Snapshot, fork, and restore', link: '/cli/snapshot-fork-restore' },
          { text: 'Auth, MCP, and reap', link: '/cli/auth-mcp-reap' },
        ],
      },
      {
        text: 'Recipes',
        collapsed: false,
        items: [
          { text: 'Mounts and worktrees', link: '/recipes/mounts-and-worktrees' },
          { text: 'Building images', link: '/recipes/building-images' },
          { text: 'Docker in a sandbox', link: '/recipes/docker-in-sandbox' },
          { text: 'Parallel development flow', link: '/recipes/parallel-dev-flow' },
          { text: 'Using nexus3 from herdr', link: '/recipes/herdr' },
        ],
      },
      {
        text: 'Security',
        collapsed: false,
        items: [
          { text: 'Egress & perimeter', link: '/security/egress-and-perimeter' },
          { text: 'Accepted risks', link: '/security/accepted-risks' },
        ],
      },
      {
        text: 'Operations',
        collapsed: false,
        items: [
          { text: 'Resource lifecycle', link: '/operations/resource-lifecycle' },
        ],
      },
    ],

    outline: { level: [2, 3] },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/IniZio/nexus3' },
    ],
  },

  // Mermaid diagrams — the plugin auto-detects the VitePress dark/light theme
  // and switches between mermaid's 'default' and 'dark' themes accordingly,
  // so both modes are legible without any extra configuration here.
  mermaid: {},
})
