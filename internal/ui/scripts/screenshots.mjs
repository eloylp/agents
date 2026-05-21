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
import { existsSync, mkdirSync, readFileSync, rmSync, readdirSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = join(__dirname, '..', '..', '..')
const OUT = join(REPO_ROOT, 'docs', 'img')
const VIDEO_TMP = join(REPO_ROOT, '.screenshot-videos')
mkdirSync(OUT, { recursive: true })

const BASE = (process.env.AGENTS_BASE_URL || 'http://localhost:8081').replace(/\/+$/, '')
const WORKSPACE = process.env.AGENTS_WORKSPACE || 'readme-driven'
const VIEWPORT = { width: 1440, height: 900 }
// README GIFs are intentionally storyboard-like. The full smooth recording is
// preserved separately as MP4/WebM; this keeps the header image small.
const GIF_FPS = Number(process.env.AGENTS_GIF_FPS || 0.4)
const GIF_WIDTH = Number(process.env.AGENTS_GIF_WIDTH || 960)
const VIDEO_TRIM_START = Number(process.env.AGENTS_VIDEO_TRIM_START || 1.8)
const DEFAULT_TOKEN_FILE = join(REPO_ROOT, '.local', 'screenshot-api-token.txt')
const TOKEN_FILE = process.env.AGENTS_TOKEN_FILE || DEFAULT_TOKEN_FILE
const TOKEN = process.env.AGENTS_TOKEN || (existsSync(TOKEN_FILE) ? readFileSync(TOKEN_FILE, 'utf8').trim() : '')
const AUTH_HEADERS = TOKEN ? { 'Authorization': `Bearer ${TOKEN}` } : {}
const IGNORE_HTTPS_ERRORS = process.env.AGENTS_IGNORE_HTTPS_ERRORS === '1'
const DEMO_AGENT = 'release-scribe'

async function newAppContext(browser, options = {}) {
  const ctx = await browser.newContext({
    ignoreHTTPSErrors: IGNORE_HTTPS_ERRORS,
    ...options,
    extraHTTPHeaders: { ...AUTH_HEADERS, ...(options.extraHTTPHeaders ?? {}) },
  })
  await ctx.addInitScript(workspace => {
    window.localStorage.setItem('agents.workspace', workspace)
    window.localStorage.setItem('agents.sidebarCollapsed', 'false')
  }, WORKSPACE)
  return ctx
}

// Waits until the auth "Checking session" screen is gone (i.e. the app
// has resolved its /auth/status fetch and rendered real content). Times
// out after 10 s so a broken auth path fails fast with a readable error.
async function waitForAuth(page) {
  await page.waitForFunction(
    () => !document.body.textContent.includes('Checking session'),
    { timeout: 10000 },
  )
}

// One screenshot capture: visit url, wait for content, save PNG.
// `prep` runs once after navigation lands, use it to expand panels,
// click into modals, etc., before snapping the final frame.
async function capture(browser, { name, url, prep, fullPage = false, settle = 1000 }) {
  const ctx = await newAppContext(browser, { viewport: VIEWPORT })
  const page = await ctx.newPage()
  // domcontentloaded, not networkidle, because pages with SSE keep
  // the network busy indefinitely. waitForAuth blocks until the React
  // auth gate clears, then settle gives the data fetch a moment to land.
  await page.goto(BASE + url, { waitUntil: 'domcontentloaded' })
  await waitForAuth(page)
  await page.waitForTimeout(settle)
  if (prep) await prep(page)
  const out = join(OUT, `${name}.png`)
  await page.screenshot({ path: out, fullPage })
  console.log(`  ✓ ${name}.png`)
  await ctx.close()
}

// Records a video of the interaction `script(page)` performs, then
// converts the .webm Playwright produces into a GIF via ffmpeg.
async function captureVideo(browser, {
  name,
  url,
  script,
  viewportH = 700,
  gif = true,
  gifFps = GIF_FPS,
  gifWidth = GIF_WIDTH,
  mp4Name = '',
  webmOutName = '',
  trimStart = VIDEO_TRIM_START,
}) {
  // Clean any prior tmp before recording.
  rmSync(VIDEO_TMP, { recursive: true, force: true })
  mkdirSync(VIDEO_TMP, { recursive: true })

  const ctx = await newAppContext(browser, {
    viewport: { width: VIEWPORT.width, height: viewportH },
    recordVideo: { dir: VIDEO_TMP, size: { width: VIEWPORT.width, height: viewportH } },
  })
  const page = await ctx.newPage()
  await page.goto(BASE + url, { waitUntil: 'domcontentloaded' })
  await waitForAuth(page)
  await page.waitForTimeout(1500)
  await script(page)
  await page.waitForTimeout(800)
  await ctx.close() // flushes the video file

  const producedWebmName = readdirSync(VIDEO_TMP).find(f => f.endsWith('.webm'))
  if (!producedWebmName) throw new Error(`no video produced for ${name}`)
  const webm = join(VIDEO_TMP, producedWebmName)
  const trimPrefix = trimStart > 0 ? `trim=start=${trimStart},setpts=PTS-STARTPTS,` : ''

  if (webmOutName) {
    execSync(`ffmpeg -y -i "${webm}" -vf "${trimPrefix}scale=1280:-2:flags=lanczos" -c:v libvpx-vp9 -crf 34 -b:v 0 "${join(OUT, `${webmOutName}.webm`)}"`, { stdio: 'pipe' })
    console.log(`  ✓ ${webmOutName}.webm`)
  }
  if (mp4Name) {
    const mp4 = join(OUT, `${mp4Name}.mp4`)
    execSync(`ffmpeg -y -i "${webm}" -vf "${trimPrefix}scale=1280:-2:flags=lanczos" -c:v libx264 -preset medium -crf 24 -pix_fmt yuv420p -movflags +faststart "${mp4}"`, { stdio: 'pipe' })
    console.log(`  ✓ ${mp4Name}.mp4`)
  }
  if (gif) {
    const gifPath = join(OUT, `${name}.gif`)
    // Two-pass palette generation keeps UI text and graph lines legible.
    const palette = join(VIDEO_TMP, 'palette.png')
    execSync(`ffmpeg -y -i "${webm}" -vf "${trimPrefix}fps=${gifFps},scale=${gifWidth}:-1:flags=lanczos,palettegen=stats_mode=diff" "${palette}"`, { stdio: 'pipe' })
    execSync(`ffmpeg -y -i "${webm}" -i "${palette}" -filter_complex "${trimPrefix}fps=${gifFps},scale=${gifWidth}:-1:flags=lanczos[x];[x][1:v]paletteuse=dither=sierra2_4a:diff_mode=rectangle" "${gifPath}"`, { stdio: 'pipe' })
    console.log(`  ✓ ${name}.gif`)
  }
  rmSync(VIDEO_TMP, { recursive: true, force: true })
}

const browser = await chromium.launch()

try {
  await cleanupDemoState('preflight')

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
  // Trace detail (not the list), show the token usage + prompt panel.
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
  await capture(browser, { name: 'graph', url: '/ui/graph/', prep: fitGraph })
  await capture(browser, { name: 'skills', url: '/ui/skills/' })
  await capture(browser, { name: 'repos', url: '/ui/repos/' })
  await capture(browser, { name: 'memory', url: '/ui/memory/' })
  await capture(browser, { name: 'config', url: '/ui/config/' })
  await capture(browser, { name: 'guardrails', url: '/ui/config/?tab=guardrails', settle: 1500 })

  console.log('recording graph edit interaction...')
  await captureVideo(browser, {
    name: 'graph-edit',
    url: '/ui/graph/',
    viewportH: 760,
    script: async page => {
      await recordHeaderFlow(page)
    },
  })
  await cleanupDemoState('after header gif')

  console.log('recording full graph flow video...')
  await captureVideo(browser, {
    name: 'graph-flow-full',
    url: '/ui/graph/',
    viewportH: 820,
    gif: false,
    mp4Name: 'graph-flow-full',
    webmOutName: 'graph-flow-full',
    script: async page => {
      await recordFullGraphFlow(page)
    },
  })

  console.log('rendering MCP terminal mock...')
  await captureMcpTerminal(browser)

  console.log(`done, output in ${OUT}`)
} finally {
  await cleanupDemoState('final')
  await browser.close()
}

async function cleanupDemoState(phase) {
  await removeDemoDispatchReferences(phase)
  const res = await fetch(withWorkspaceURL(`/agents/${encodeURIComponent(DEMO_AGENT)}?cascade=true`), {
    method: 'DELETE',
    headers: AUTH_HEADERS,
  })
  if (res.ok) {
    console.log(`  ✓ ${phase} cleanup removed ${DEMO_AGENT}`)
    return
  }
  if (res.status === 404) {
    console.log(`  ✓ ${phase} cleanup found no ${DEMO_AGENT}`)
    return
  }
  const body = await res.text().catch(() => '')
  throw new Error(`${phase} cleanup failed for ${DEMO_AGENT}: HTTP ${res.status}${body ? ` ${body}` : ''}`)
}

async function removeDemoDispatchReferences(phase) {
  const res = await fetch(withWorkspaceURL('/agents'), { headers: AUTH_HEADERS })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`${phase} cleanup failed to list agents: HTTP ${res.status}${body ? ` ${body}` : ''}`)
  }
  const agents = await res.json()
  for (const agent of agents) {
    if (!(agent.can_dispatch ?? []).includes(DEMO_AGENT)) continue
    const detailRes = await fetch(withWorkspaceURL(`/agents/${encodeURIComponent(agent.name)}`), { headers: AUTH_HEADERS })
    if (!detailRes.ok) {
      const body = await detailRes.text().catch(() => '')
      throw new Error(`${phase} cleanup failed to read ${agent.name}: HTTP ${detailRes.status}${body ? ` ${body}` : ''}`)
    }
    const detail = await detailRes.json()
    detail.can_dispatch = (detail.can_dispatch ?? []).filter(name => name !== DEMO_AGENT)
    const writeRes = await fetch(withWorkspaceURL('/agents'), {
      method: 'POST',
      headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
      body: JSON.stringify(detail),
    })
    if (!writeRes.ok) {
      const body = await writeRes.text().catch(() => '')
      throw new Error(`${phase} cleanup failed to update ${agent.name}: HTTP ${writeRes.status}${body ? ` ${body}` : ''}`)
    }
    console.log(`  ✓ ${phase} cleanup removed ${DEMO_AGENT} dispatch reference from ${agent.name}`)
  }
}

function withWorkspaceURL(path) {
  const sep = path.includes('?') ? '&' : '?'
  return `${BASE}${path}${sep}workspace=${encodeURIComponent(WORKSPACE)}`
}

// captureMcpTerminal renders a faux Claude Code terminal session that
// asks "show me all agents and their status" and replies with a real
// table built from the seeded daemon's /agents endpoint. The mock
// shows what an MCP client conversation looks like without depending
// on a real Claude account, the data is genuine, the dialogue is
// synthesised. Output: docs/img/mcp-terminal.png.
async function captureMcpTerminal(browser) {
  const ctx = await newAppContext(browser, { viewport: { width: 1100, height: 820 } })
  const page = await ctx.newPage()

  // Pull the same data Claude would see if it called list_agents via
  // the MCP server. /agents is the fleet snapshot, same wire shape as
  // the MCP tool, and carries current_status per agent so the rendered
  // table can show running vs idle authentically.
  const agents = await fetch(`${BASE}/agents?workspace=${encodeURIComponent(WORKSPACE)}`, { headers: AUTH_HEADERS }).then(r => r.json())

  const html = renderTerminal(agents)
  await page.setContent(html, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(150)
  const out = join(OUT, 'mcp-terminal.png')
  await page.screenshot({ path: out, fullPage: true })
  console.log(`  ✓ mcp-terminal.png`)
  await ctx.close()
}

async function recordHeaderFlow(page) {
  await waitForText(page, 'Agent Interaction Graph')
  await fitGraph(page)
  await pause(page, 1400)

  await createReleaseScribe(page)
  await pause(page, 1200)
  await presentClick(page, page.locator('aside button:has-text("x")'))
  await pause(page, 700)
  await fitGraph(page)
  await pause(page, 900)

  await wireAgents(page, 'document-writer', 'release-scribe')
  await pause(page, 1600)
  await fitGraph(page)
  await pause(page, 900)

  await openAgentPanel(page, 'pr-reviewer')
  await clickPanelTab(page, 'Activity')
  await pause(page, 1400)
  const transcript = page.locator('button:has-text("Open transcript")').first()
  if (await transcript.count()) {
    await presentClick(page, transcript)
    await pause(page, 3600)
    await presentClick(page, page.locator('button:has-text("Close")').first())
    await pause(page, 600)
  }
  await presentClick(page, page.locator('aside button:has-text("x")'))
  await pause(page, 700)
  await fitGraph(page)
  await pause(page, 1600)
}

async function recordFullGraphFlow(page) {
  await waitForText(page, 'Agent Interaction Graph')
  await fitGraph(page)
  await pause(page, 1800)

  await openAgentPanel(page, 'product-strategist')
  await pause(page, 1200)
  for (const tab of ['Overview', 'Prompt', 'Dispatch', 'Activity']) {
    await clickPanelTab(page, tab)
    await pause(page, tab === 'Prompt' ? 2200 : 1600)
  }
  await presentClick(page, page.locator('aside button:has-text("x")'))
  await pause(page, 900)

  await createReleaseScribe(page)
  await pause(page, 1500)
  await presentClick(page, page.locator('aside button:has-text("x")'))
  await pause(page, 800)
  await fitGraph(page)
  await pause(page, 1000)

  await wireAgents(page, 'document-writer', 'release-scribe')
  await pause(page, 1800)
  await fitGraph(page)
  await pause(page, 1000)

  await openAgentPanel(page, 'pr-reviewer')
  await clickPanelTab(page, 'Activity')
  await pause(page, 1800)
  const transcript = page.locator('button:has-text("Open transcript")').first()
  if (await transcript.count()) {
    await presentClick(page, transcript)
    await pause(page, 4200)
    await presentClick(page, page.locator('button:has-text("Close")').first())
    await pause(page, 700)
  }
  await presentClick(page, page.locator('aside button:has-text("x")'))
  await pause(page, 800)
  await fitGraph(page)
  await pause(page, 2400)
}

async function pause(page, ms) {
  await page.waitForTimeout(ms)
}

async function waitForText(page, text) {
  await page.locator(`text=${text}`).first().waitFor({ timeout: 10000 })
}

async function fitGraph(page) {
  const fit = page.locator('.react-flow__controls-fitview').first()
  if (!(await fit.count())) return
  await presentClick(page, fit, { before: 250, after: 900 })
}

async function openAgentPanel(page, name) {
  await fitGraph(page)
  const node = page.locator('.react-flow__node', { hasText: name }).first()
  await node.waitFor({ timeout: 10000 })
  await presentClick(page, node)
  await page.locator('aside', { hasText: name }).waitFor({ timeout: 10000 })
}

async function clickPanelTab(page, name) {
  const tab = page.locator('aside button', { hasText: name }).first()
  await tab.waitFor({ timeout: 10000 })
  await presentClick(page, tab)
}

async function presentClick(page, locator, { before = 450, after = 350 } = {}) {
  await locator.waitFor({ timeout: 10000 })
  await locator.hover({ timeout: 5000 }).catch(() => {})
  await pause(page, before)
  try {
    await locator.click({ timeout: 5000 })
  } catch {
    try {
      await locator.click({ force: true, timeout: 5000 })
    } catch {
      await locator.dispatchEvent('click')
    }
  }
  await pause(page, after)
}

async function createReleaseScribe(page) {
  await presentClick(page, page.locator('button:has-text("+ Create agent")'), { before: 500, after: 500 })
  const panel = page.locator('aside', { hasText: 'Create agent' })
  await panel.waitFor({ timeout: 10000 })
  await pause(page, 600)
  await panel.locator('input[placeholder="agent-name"]').pressSequentially('release-scribe', { delay: 45 })
  await pause(page, 450)
  await selectField(panel, 'Backend', 'claude')
  await pause(page, 450)
  await selectField(panel, 'Model', 'claude-sonnet-4-6')
  await pause(page, 450)
  await selectBadge(panel, 'Skills', 'documentation')
  await pause(page, 450)
  await selectBadge(panel, 'Skills', 'dev-exp')
  await pause(page, 450)
  await panel.locator('input[placeholder="Used for identification and inter-agent routing context"]').fill('Turns completed runs into release notes, README snippets, and documentation follow-ups.')
  await pause(page, 450)
  await selectFieldMatching(panel, 'Prompt *', 'document-writer')
  await pause(page, 450)
  await panel.getByLabel('Allow PRs').check()
  await panel.getByLabel('Allow dispatch').check()
  await pause(page, 650)
  await presentClick(page, panel.locator('button:has-text("Save")'), { before: 500, after: 700 })
  await page.locator('aside', { hasText: 'release-scribe' }).waitFor({ timeout: 10000 })
}

async function selectField(root, label, value) {
  const select = root.locator(`xpath=.//label[normalize-space()="${label}"]/following-sibling::select[1]`)
  await select.waitFor({ timeout: 10000 })
  await select.selectOption(value)
}

async function selectFieldMatching(root, label, text) {
  const select = root.locator(`xpath=.//label[normalize-space()="${label}"]/following-sibling::select[1]`)
  await select.waitFor({ timeout: 10000 })
  const value = await select.evaluate((el, needle) => {
    const options = Array.from(el.options)
    const match = options.find(option => option.textContent?.includes(needle) || option.value.includes(needle))
    return match?.value || ''
  }, text)
  if (!value) throw new Error(`option containing "${text}" not found for ${label}`)
  await select.selectOption(value)
}

async function selectBadge(root, label, value) {
  const select = root.locator(`xpath=.//label[normalize-space()="${label}"]/following-sibling::div[1]//select`)
  await select.waitFor({ timeout: 10000 })
  await select.selectOption(value)
  await root.locator('span', { hasText: value }).first().waitFor({ timeout: 10000 })
}

async function wireAgents(page, sourceName, targetName) {
  const sourceNode = page.locator('.react-flow__node', { hasText: sourceName }).first()
  const targetNode = page.locator('.react-flow__node', { hasText: targetName }).first()
  await sourceNode.waitFor({ timeout: 10000 })
  await targetNode.waitFor({ timeout: 10000 })

  const srcHandle = sourceNode.locator('.react-flow__handle-bottom, .react-flow__handle.source').first()
  const tgtHandle = targetNode.locator('.react-flow__handle-top, .react-flow__handle.target').first()
  const srcBox = await srcHandle.boundingBox()
  const tgtBox = await tgtHandle.boundingBox()
  if (!srcBox || !tgtBox) {
    console.log(`    (handles for "${sourceName}" -> "${targetName}" not measurable, skipping wiring)`)
    return
  }

  const srcCx = srcBox.x + srcBox.width / 2
  const srcCy = srcBox.y + srcBox.height / 2
  const tgtCx = tgtBox.x + tgtBox.width / 2
  const tgtCy = tgtBox.y + tgtBox.height / 2

  await page.mouse.move(srcCx, srcCy)
  await page.waitForTimeout(350)
  await page.mouse.down()
  for (let i = 1; i <= 36; i++) {
    const t = i / 36
    await page.mouse.move(srcCx + (tgtCx - srcCx) * t, srcCy + (tgtCy - srcCy) * t)
    await page.waitForTimeout(28)
  }
  await page.mouse.move(tgtCx, tgtCy)
  await page.waitForTimeout(250)
  await page.mouse.up()
  await page.waitForTimeout(1000)
}

// renderTerminal emits standalone HTML that looks like a Claude Code
// session in iTerm2. Monospace, dark background, distinct colours for
// the user prompt, the assistant's narration, the [Tool: ...] markers,
// and the rendered table. No external assets, the screenshotting
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
    model:   a.model || ', ',
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
    <span style="margin-left: 8px;">claude, agents-fleet MCP, 100×40</span>
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
