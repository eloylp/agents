// Playwright script that captures every page documented in docs/ui.md
// against the synthetic fixture daemon (cmd/screenshotseed). For pages
// whose value is a flow, not a snapshot, it records a video of the
// interaction and converts it to GIF via ffmpeg.
//
// Run from the repo root with the seeder already up on :8081:
//
//   node internal/ui/scripts/screenshots.mjs
//
// Output: docs/img/*.png and docs/img/*.gif. Re-running overwrites.
import { chromium } from 'playwright'
import { execSync } from 'child_process'
import { mkdirSync, renameSync, rmSync, readdirSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = join(__dirname, '..', '..', '..')
const OUT = join(REPO_ROOT, 'docs', 'img')
const VIDEO_TMP = join(REPO_ROOT, '.screenshot-videos')
mkdirSync(OUT, { recursive: true })

const BASE = 'http://localhost:8081'
const VIEWPORT = { width: 1440, height: 900 }

// One screenshot capture: visit url, wait for content, save PNG.
// `prep` runs once after navigation lands — use it to expand panels,
// click into modals, etc., before snapping the final frame.
async function capture(browser, { name, url, prep, fullPage = false, settle = 1000 }) {
  const ctx = await browser.newContext({ viewport: VIEWPORT })
  const page = await ctx.newPage()
  // domcontentloaded — not networkidle — because pages with SSE keep
  // the network busy indefinitely. settle gives React hydration +
  // initial fetch a moment to land before we snap.
  await page.goto(BASE + url, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(settle)
  if (prep) await prep(page)
  const out = join(OUT, `${name}.png`)
  await page.screenshot({ path: out, fullPage })
  console.log(`  ✓ ${name}.png`)
  await ctx.close()
}

// Records a video of the interaction `script(page)` performs, then
// converts the .webm Playwright produces into a GIF via ffmpeg.
async function captureVideo(browser, { name, url, script, viewportH = 700 }) {
  // Clean any prior tmp before recording.
  rmSync(VIDEO_TMP, { recursive: true, force: true })
  mkdirSync(VIDEO_TMP, { recursive: true })

  const ctx = await browser.newContext({
    viewport: { width: VIEWPORT.width, height: viewportH },
    recordVideo: { dir: VIDEO_TMP, size: { width: VIEWPORT.width, height: viewportH } },
  })
  const page = await ctx.newPage()
  await page.goto(BASE + url, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(1500)
  await script(page)
  await page.waitForTimeout(800)
  await ctx.close() // flushes the video file

  const webmName = readdirSync(VIDEO_TMP).find(f => f.endsWith('.webm'))
  if (!webmName) throw new Error(`no video produced for ${name}`)
  const webm = join(VIDEO_TMP, webmName)
  const gif = join(OUT, `${name}.gif`)

  // 12 fps + 720px wide is a reasonable middle ground for a doc gif.
  // Two-pass: generate a palette so colours don't dither badly.
  const palette = join(VIDEO_TMP, 'palette.png')
  execSync(`ffmpeg -y -i "${webm}" -vf "fps=12,scale=900:-1:flags=lanczos,palettegen" "${palette}"`, { stdio: 'pipe' })
  execSync(`ffmpeg -y -i "${webm}" -i "${palette}" -filter_complex "fps=12,scale=900:-1:flags=lanczos[x];[x][1:v]paletteuse" "${gif}"`, { stdio: 'pipe' })
  console.log(`  ✓ ${name}.gif`)
  rmSync(VIDEO_TMP, { recursive: true, force: true })
}

const browser = await chromium.launch()

console.log('capturing screenshots...')

await capture(browser, { name: 'fleet', url: '/ui/' })
await capture(browser, { name: 'events', url: '/ui/events/' })
await capture(browser, {
  name: 'runners',
  url: '/ui/runners/',
  prep: async page => {
    // Expand the live row so the operator can see what's inside.
    const liveRow = page.locator('text=running').first()
    if (await liveRow.count()) await liveRow.click()
  },
})
// Trace detail (not the list) — show the token usage + prompt panel.
// span-001 is seeded with realistic prompt + tokens; navigate straight
// to the detail URL the in-app router uses (`/ui/traces/?id=<root>`),
// then expand the Prompt accordion before snapping.
await capture(browser, {
  name: 'traces',
  url: '/ui/traces/?id=evt-001',
  settle: 1200,
  prep: async page => {
    const promptToggle = page.locator('button', { hasText: 'Prompt' }).first()
    if (await promptToggle.count()) {
      await promptToggle.click()
      await page.waitForTimeout(800) // wait for lazy fetch
    }
  },
})
await capture(browser, { name: 'graph', url: '/ui/graph/' })
await capture(browser, { name: 'skills', url: '/ui/skills/' })
await capture(browser, { name: 'repos', url: '/ui/repos/' })
await capture(browser, { name: 'memory', url: '/ui/memory/' })
await capture(browser, { name: 'config', url: '/ui/config/' })

console.log('recording graph edit interaction...')
await captureVideo(browser, {
  name: 'graph-edit',
  url: '/ui/graph/',
  viewportH: 720,
  script: async page => {
    // Toggle edit mode.
    const editBtn = page.locator('button:has-text("Edit wiring")').first()
    if (await editBtn.count() === 0) {
      console.log('    (edit-wiring button not found; recording static graph)')
      return
    }
    await editBtn.click()
    await page.waitForTimeout(1200)

    // The seeded fixture has pr-reviewer with no can_dispatch entries
    // and scout with allow_dispatch=true plus a description, so the
    // pr-reviewer→scout connection passes validateConnection and
    // onConnect actually persists the wiring change. The agent name
    // is the React Flow node id and shows up as data-id on
    // .react-flow__node.
    const sourceName = 'pr-reviewer'
    const targetName = 'scout'

    const sourceNode = page.locator(`.react-flow__node[data-id="${sourceName}"]`)
    const targetNode = page.locator(`.react-flow__node[data-id="${targetName}"]`)
    if (!(await sourceNode.count()) || !(await targetNode.count())) {
      console.log(`    (node "${sourceName}" or "${targetName}" not rendered, skipping)`)
      return
    }

    // The graph node renders <Handle type="source" position={Position.Bottom}/>
    // and <Handle type="target" position={Position.Top}/>, so the bottom
    // handle on the source and the top handle on the target are the
    // legal endpoints for a connection.
    const srcHandle = sourceNode.locator('.react-flow__handle-bottom, .react-flow__handle.source')
    const tgtHandle = targetNode.locator('.react-flow__handle-top, .react-flow__handle.target')
    const srcBox = await srcHandle.first().boundingBox()
    const tgtBox = await tgtHandle.first().boundingBox()
    if (!srcBox || !tgtBox) {
      console.log('    (handles not measurable, skipping)')
      return
    }

    const srcCx = srcBox.x + srcBox.width / 2
    const srcCy = srcBox.y + srcBox.height / 2
    const tgtCx = tgtBox.x + tgtBox.width / 2
    const tgtCy = tgtBox.y + tgtBox.height / 2

    // Hover briefly so the source handle highlights and the operator
    // can see what's about to happen.
    await page.mouse.move(srcCx, srcCy)
    await page.waitForTimeout(400)
    await page.mouse.down()

    // Slow drag in many small steps so the connecting line is visible.
    const steps = 30
    for (let i = 1; i <= steps; i++) {
      const t = i / steps
      await page.mouse.move(srcCx + (tgtCx - srcCx) * t, srcCy + (tgtCy - srcCy) * t, { steps: 1 })
      await page.waitForTimeout(35)
    }
    // Settle on the target handle for a beat — React Flow needs a
    // stable hover before mouseup commits the connection.
    await page.mouse.move(tgtCx, tgtCy)
    await page.waitForTimeout(250)
    await page.mouse.up()
    // Hold the final frame so the new edge is visibly persisted in the
    // gif before we cut.
    await page.waitForTimeout(2000)
  },
})

console.log('rendering MCP terminal mock...')
await captureMcpTerminal(browser)

console.log(`done — output in ${OUT}`)
await browser.close()

// captureMcpTerminal renders a faux Claude Code terminal session that
// asks "show me all agents and their status" and replies with a real
// table built from the seeded daemon's /agents endpoint. The mock
// shows what an MCP client conversation looks like without depending
// on a real Claude account — the data is genuine, the dialogue is
// synthesised. Output: docs/img/mcp-terminal.png.
async function captureMcpTerminal(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1100, height: 820 } })
  const page = await ctx.newPage()

  // Pull the same data Claude would see if it called list_agents via
  // the MCP server. /agents is the fleet snapshot — same wire shape as
  // the MCP tool — and carries current_status per agent so the rendered
  // table can show running vs idle authentically.
  const agents = await fetch(BASE + '/agents').then(r => r.json())

  const html = renderTerminal(agents)
  await page.setContent(html, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(150)
  const out = join(OUT, 'mcp-terminal.png')
  await page.screenshot({ path: out, fullPage: true })
  console.log(`  ✓ mcp-terminal.png`)
  await ctx.close()
}

// renderTerminal emits standalone HTML that looks like a Claude Code
// session in iTerm2. Monospace, dark background, distinct colours for
// the user prompt, the assistant's narration, the [Tool: ...] markers,
// and the rendered table. No external assets — the screenshotting
// pipeline must work offline.
function renderTerminal(agents) {
  const padCell = (s, n) => String(s).padEnd(n)
  const cols = [
    { k: 'name',    label: 'Agent',    width: 14 },
    { k: 'backend', label: 'Backend',  width: 11 },
    { k: 'model',   label: 'Model',    width: 22 },
    { k: 'skills',  label: 'Skills',   width: 26 },
    { k: 'status',  label: 'Status',   width: 9 },
  ]
  const rows = agents.map(a => ({
    name:    a.name,
    backend: a.backend,
    model:   a.model || '—',
    skills:  (a.skills ?? []).join(', '),
    status:  a.current_status === 'running' ? 'running' : 'idle',
  }))
  const running = new Set(rows.filter(r => r.status === 'running').map(r => r.name))

  const header = '│ ' + cols.map(c => padCell(c.label, c.width)).join(' │ ') + ' │'
  const sep    = '├' + cols.map(c => '─'.repeat(c.width + 2)).join('┼') + '┤'
  const top    = '┌' + cols.map(c => '─'.repeat(c.width + 2)).join('┬') + '┐'
  const bot    = '└' + cols.map(c => '─'.repeat(c.width + 2)).join('┴') + '┘'
  const dataRows = rows.map(r =>
    '│ ' + cols.map(c => {
      const cell = padCell(r[c.k], c.width)
      if (c.k === 'status') {
        const colour = r[c.k] === 'running' ? 'var(--running)' : 'var(--idle)'
        return `<span style="color:${colour}">${cell}</span>`
      }
      return cell
    }).join(' │ ') + ' │',
  )

  const tableHTML = [top, header, sep, ...dataRows, bot].join('\n')
  const totalAgents = agents.length
  const dispatchable = agents.filter(a => a.allow_dispatch).length

  return `<!doctype html>
<html><head><meta charset="utf-8"><style>
  :root {
    --bg: #1a1b26;
    --fg: #c0caf5;
    --muted: #565f89;
    --prompt: #7aa2f7;
    --user: #c0caf5;
    --tool: #bb9af7;
    --running: #9ece6a;
    --idle: #7dcfff;
    --accent: #ff9e64;
  }
  html, body { margin: 0; padding: 0; background: var(--bg); }
  body {
    font-family: 'Menlo', 'Monaco', 'Consolas', monospace;
    color: var(--fg);
    padding: 24px 28px;
    line-height: 1.55;
    font-size: 13.5px;
  }
  .titlebar {
    display: flex; align-items: center; gap: 8px;
    padding: 6px 12px; margin: -24px -28px 16px;
    background: #16161e; border-bottom: 1px solid #292e42;
    font-size: 12px; color: var(--muted);
  }
  .titlebar .dot { width: 12px; height: 12px; border-radius: 50%; }
  .titlebar .r { background: #f7768e; }
  .titlebar .y { background: #e0af68; }
  .titlebar .g { background: #9ece6a; }
  .prompt { color: var(--prompt); }
  .user   { color: var(--user); }
  .tool   { color: var(--tool); }
  .muted  { color: var(--muted); }
  .accent { color: var(--accent); }
  pre     { margin: 4px 0; white-space: pre; font: inherit; }
</style></head>
<body>
  <div class="titlebar">
    <span class="dot r"></span><span class="dot y"></span><span class="dot g"></span>
    <span style="margin-left: 8px;">claude — agents-fleet MCP — 100×40</span>
  </div>
  <pre><span class="prompt">❯</span> claude</pre>
  <pre class="muted">Welcome to Claude Code (claude-opus-4-7)</pre>
  <pre class="muted">Connected MCP servers: agents-fleet (3 tools active)</pre>
  <pre> </pre>
  <pre><span class="prompt">&gt;</span> <span class="user">show me all agents and their status</span></pre>
  <pre> </pre>
  <pre>I'll query the agents-fleet MCP server.</pre>
  <pre> </pre>
  <pre><span class="tool">⏺ list_agents()</span></pre>
  <pre><span class="muted">  ⎿  ${totalAgents} agents · ${dispatchable} with allow_dispatch: true</span></pre>
  <pre> </pre>
  <pre>Here's the fleet status:</pre>
  <pre> </pre>
  <pre>${tableHTML}</pre>
  <pre> </pre>
  <pre>${running.size > 0
    ? `<span class="accent">▸</span> <span class="running" style="color: var(--running)">${[...running].join(', ')}</span> currently running. The rest are idle, waiting for their next trigger (label, webhook event, or cron tick).`
    : `All agents are idle, waiting for their next trigger (label, webhook event, or cron tick).`
  }</pre>
  <pre> </pre>
  <pre><span class="prompt">&gt;</span> <span class="muted">_</span></pre>
</body></html>`
}
