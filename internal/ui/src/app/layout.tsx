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
            font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', 'Consolas', monospace;
            background: #0a1628;
            background-image:
              linear-gradient(rgba(56,189,248,0.07) 1px, transparent 1px),
              linear-gradient(90deg, rgba(56,189,248,0.07) 1px, transparent 1px);
            background-size: 24px 24px;
            color: #cbd5e1;
            min-height: 100vh;
          }
          a { color: #38bdf8; text-decoration: none; }
          a:hover { text-decoration: underline; }
          pre { font-family: inherit; }
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
