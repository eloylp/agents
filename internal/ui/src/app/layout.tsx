import type { Metadata } from 'next'
import DashboardShell from '@/components/DashboardShell'
import { AuthProvider } from '@/lib/auth'
import { ThemeProvider } from '@/lib/theme'

export const metadata: Metadata = {
  title: 'Agents, Observability Dashboard',
  description: 'Runtime dashboard for the agents daemon',
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" data-theme="light" suppressHydrationWarning>
      <head>
        <style>{`
          * { box-sizing: border-box; margin: 0; padding: 0; }

          :root, [data-theme="light"] {
            --font-sans: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            --font-mono: "SF Mono", "Fira Code", "Cascadia Code", Consolas, monospace;
            --bg: #f0f4f8;
            --bg-grid: rgba(59,130,246,0.06);
            --bg-card: #ffffff;
            --bg-input: #f8fafc;
            --bg-subtle: #eff6ff;
            --bg-nav: #ffffff;
            --bg-modal-overlay: rgba(0,0,0,0.3);
            --bg-danger: #fff5f5;
            --text: #1e293b;
            --text-heading: #1e3a5f;
            --text-muted: #64748b;
            --text-faint: #94a3b8;
            --accent: #2563eb;
            --accent-dark: #1d4ed8;
            --accent-bg: #eff6ff;
            --border: #bfdbfe;
            --border-subtle: #e2e8f0;
            --border-nav: #2563eb;
            --border-danger: #fecaca;
            --text-danger: #dc2626;
            --success: #15803d;
            --success-bg: #dcfce7;
            --success-border: #bbf7d0;
            --error: #f87171;
            --error-bg: rgba(248,113,113,0.1);
            --btn-primary-bg: #2563eb;
            --btn-primary-border: #1d4ed8;
            --scrollbar-track: #e2e8f0;
            --scrollbar-thumb: #94a3b8;
            --link: #2563eb;
            --badge-skill-bg: #1e3a5f;
            --badge-skill-text: #93c5fd;
            --badge-skill-border: #1d4ed8;
          }

          [data-theme="dark"] {
            --bg: #0a1628;
            --bg-grid: rgba(56,189,248,0.07);
            --bg-card: #111d2e;
            --bg-input: #0f1d32;
            --bg-subtle: #0f1d32;
            --bg-nav: #0f1d32;
            --bg-modal-overlay: rgba(10,22,40,0.65);
            --bg-danger: #1c1017;
            --text: #cbd5e1;
            --text-heading: #e2e8f0;
            --text-muted: #64748b;
            --text-faint: #94a3b8;
            --accent: #38bdf8;
            --accent-dark: #0e7490;
            --accent-bg: rgba(56,189,248,0.12);
            --border: #1e3a5f;
            --border-subtle: #334155;
            --border-nav: #1e3a5f;
            --border-danger: #7f1d1d;
            --text-danger: #f87171;
            --success: #34d399;
            --success-bg: rgba(52,211,153,0.15);
            --success-border: #065f46;
            --error: #f87171;
            --error-bg: rgba(248,113,113,0.15);
            --btn-primary-bg: #0e7490;
            --btn-primary-border: #0e7490;
            --scrollbar-track: #1e293b;
            --scrollbar-thumb: #475569;
            --link: #38bdf8;
            --badge-skill-bg: #1e3a5f;
            --badge-skill-text: #93c5fd;
            --badge-skill-border: #1d4ed8;
          }

          body {
            font-family: var(--font-sans);
            font-size: 14px;
            font-weight: 400;
            line-height: 1.45;
            letter-spacing: 0;
            font-feature-settings: "cv02", "cv03", "cv04", "cv11";
            text-rendering: optimizeLegibility;
            -webkit-font-smoothing: antialiased;
            -moz-osx-font-smoothing: grayscale;
            background: var(--bg);
            background-image:
              linear-gradient(var(--bg-grid) 1px, transparent 1px),
              linear-gradient(90deg, var(--bg-grid) 1px, transparent 1px);
            background-size: 24px 24px;
            color: var(--text);
            min-height: 100vh;
          }
          button, input, select, textarea { font: inherit; letter-spacing: 0; }
          h1, h2, h3, h4, h5, h6, strong, th { font-weight: 600; }
          a { color: var(--link); text-decoration: none; }
          a:hover { text-decoration: underline; }
          code, pre, kbd, samp { font-family: var(--font-mono); }
          pre { line-height: 1.45; }
          ::-webkit-scrollbar { width: 6px; height: 6px; }
          ::-webkit-scrollbar-track { background: var(--scrollbar-track); }
          ::-webkit-scrollbar-thumb { background: var(--scrollbar-thumb); border-radius: 3px; }
        `}</style>
      </head>
      <body>
        <ThemeProvider>
          <AuthProvider>
            <DashboardShell>{children}</DashboardShell>
          </AuthProvider>
        </ThemeProvider>
      </body>
    </html>
  )
}
