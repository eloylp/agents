import type { Metadata } from 'next'
import NavBar from '@/components/NavBar'

export const metadata: Metadata = {
  title: 'Agents — Observability Dashboard',
  description: 'Read-only runtime dashboard for the agents daemon',
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <head>
        <style>{`
          * { box-sizing: border-box; margin: 0; padding: 0; }
          body {
            font-family: system-ui, -apple-system, sans-serif;
            background: #0f172a;
            color: #e2e8f0;
            min-height: 100vh;
          }
          a { color: #60a5fa; text-decoration: none; }
          a:hover { text-decoration: underline; }
          pre { font-family: 'Courier New', monospace; }
          ::-webkit-scrollbar { width: 6px; height: 6px; }
          ::-webkit-scrollbar-track { background: #1e293b; }
          ::-webkit-scrollbar-thumb { background: #475569; border-radius: 3px; }
        `}</style>
      </head>
      <body>
        <NavBar />
        <main style={{ padding: '1.5rem', maxWidth: '1400px', margin: '0 auto' }}>
          {children}
        </main>
      </body>
    </html>
  )
}
