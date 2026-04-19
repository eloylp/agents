/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'export',
  distDir: 'dist',
  // The UI is served from /ui/ so assets must use relative paths.
  basePath: '/ui',
  trailingSlash: true,
  // Disable image optimization (not supported with static export).
  images: { unoptimized: true },
}

module.exports = nextConfig
