// fmtDuration renders a millisecond-resolution duration in operator-friendly
// units. We don't care about millisecond precision once a run takes longer
// than a second, so values are rendered as ms only under 1s, seconds with
// one decimal under a minute, and minutes with one decimal beyond.
export function fmtDuration(ms?: number): string {
  if (ms === undefined || ms === 0) return '-'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60_000).toFixed(1)}m`
}
