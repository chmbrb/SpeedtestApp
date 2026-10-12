// Renders the PWA icons from public/icon.svg and scripts/icon-maskable.svg. Needs ImageMagick (`magick`).
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const render = (src, out, size) =>
  execFileSync('magick', ['-background', 'none', '-density', '600', join(root, src), '-resize', `${size}x${size}`, join(root, 'public', out)])

render('public/icon.svg', 'icon-192.png', 192)
render('public/icon.svg', 'icon-512.png', 512)
render('public/icon.svg', 'apple-touch-icon.png', 180)
render('scripts/icon-maskable.svg', 'icon-maskable-512.png', 512)
console.log('icons written to public/')
