// Adversarial code review: cheap wide sweep → expensive verification.
// Usage: dyna run examples/adversarial-review.js --args '{"target":"src/", "dimensions":["bugs","perf","security"]}'
// Expects profiles: a cheap high-cost-stat sweeper and a high-taste reviewer.
export const meta = {
  name: 'adversarial-review',
  description: 'Sweep for issues with a cheap worker, adversarially verify with a high-taste worker',
  phases: [{ title: 'Find' }, { title: 'Verify' }],
}

const target = (args && args.target) || '.'
const dimensions = (args && args.dimensions) || ['bugs', 'perf']

// Pick workers by stats instead of hardcoding names.
const byStat = (k) => [...profiles].sort((a, b) => b[k] - a[k])
const sweeper = byStat('cost')[0].name    // cheapest
const reviewer = byStat('taste')[0].name  // best taste

log(`sweeping with ${sweeper}, verifying with ${reviewer}`)

const FINDINGS = {
  type: 'object', required: ['findings'],
  properties: { findings: { type: 'array', items: {
    type: 'object', required: ['title', 'file'],
    properties: { title: { type: 'string' }, file: { type: 'string' }, detail: { type: 'string' } },
  }}},
}
const VERDICT = {
  type: 'object', required: ['isReal'],
  properties: { isReal: { type: 'boolean' }, reason: { type: 'string' } },
}

const results = await pipeline(
  dimensions,
  (d) => agent(
    `You are reviewing the code under ${target} for ${d} issues. Read the relevant files and report concrete findings with file paths.`,
    { profile: sweeper, label: `find:${d}`, phase: 'Find', schema: FINDINGS }),
  (review, d) => parallel((review.findings || []).map((f) => () =>
    agent(
      `Adversarially verify this ${d} finding — try hard to REFUTE it by reading the code. Finding: ${f.title} in ${f.file}. ${f.detail || ''}`,
      { profile: reviewer, label: `verify:${f.title.slice(0, 30)}`, phase: 'Verify', schema: VERDICT }
    ).then((v) => ({ ...f, dimension: d, verdict: v }))
  ))
)

const confirmed = results.flat().filter(Boolean).filter((f) => f.verdict && f.verdict.isReal)
log(`${confirmed.length} findings confirmed`)
return { confirmed }
