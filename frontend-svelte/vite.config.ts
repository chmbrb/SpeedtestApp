/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { VitePWA } from 'vite-plugin-pwa'

// The dev server proxies /api to the backend (default :8080), so the app only ever talks to its own origin.
const backend = process.env.BACKEND_URL ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [
    svelte(),
    VitePWA({
      // The service worker is the one both frontends share (frontend-shared/src/sw.ts, copied to src/shared/).
      strategies: 'injectManifest',
      srcDir: 'src/shared',
      filename: 'sw.ts',
      registerType: 'autoUpdate',
      includeAssets: ['icon.svg', 'apple-touch-icon.png'],
      manifest: {
        name: 'Speedtest',
        short_name: 'Speedtest',
        description: 'Internet speed history for your network.',
        start_url: '/',
        scope: '/',
        display: 'standalone',
        background_color: '#111821',
        theme_color: '#111821',
        icons: [
          { src: 'icon-192.png', sizes: '192x192', type: 'image/png' },
          { src: 'icon-512.png', sizes: '512x512', type: 'image/png' },
          { src: 'icon-maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
        ],
      },
      injectManifest: { globPatterns: ['**/*.{js,css,html,svg,png,woff2}'] },
    }),
  ],
  server: { proxy: { '/api': backend } },
  preview: { proxy: { '/api': backend } },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,svelte.ts}'],
    css: false,
  },
})
