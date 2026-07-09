// Judge panel: N independent design attempts from different angles,
// scored by a panel, winner synthesized.
// Usage: dyna run examples/judge-panel.js --args '{"task":"design a rate limiter for our API"}'
export const meta = {
  name: 'judge-panel',
  description: 'N independent attempts, judged, best synthesized',
  phases: [{ title: 'Attempt' }, { title: 'Judge' }, { title: 'Synthesize' }],
}

const task = (args && args.task) || 'design a caching layer'
const angles = ['MVP-first: simplest thing that ships', 'risk-first: what breaks at scale', 'user-first: best developer experience']

const smart = [...profiles].sort((a, b) => b.intelligence - a.intelligence)[0].name
const tasteful = [...profiles].sort((a, b) => b.taste - a.taste)[0].name

phase('Attempt')
const attempts = await parallel(angles.map((angle, i) => () =>
  agent(`Task: ${task}\nApproach it ${angle}. Produce a concrete design.`,
    { profile: smart, label: `attempt-${i + 1}` })))

phase('Judge')
const SCORE = { type: 'object', required: ['scores'], properties: {
  scores: { type: 'array', items: { type: 'object', required: ['attempt', 'score'],
    properties: { attempt: { type: 'integer' }, score: { type: 'number' }, why: { type: 'string' } } } } } }

const live = attempts.map((a, i) => ({ i, text: a })).filter((a) => a.text)
const judged = await agent(
  `Task: ${task}\nScore each attempt 1-10 on correctness, simplicity, and taste.\n\n` +
  live.map((a) => `--- ATTEMPT ${a.i + 1} ---\n${a.text}`).join('\n\n'),
  { profile: tasteful, label: 'judge', schema: SCORE })

const best = judged.scores.sort((a, b) => b.score - a.score)[0]
log(`winner: attempt ${best.attempt} (${best.score}/10)`)

phase('Synthesize')
const final = await agent(
  `Task: ${task}\nThis design won a judge panel:\n${live[best.attempt - 1].text}\n\nRunners-up (graft their best ideas if genuinely better):\n` +
  live.filter((a) => a.i !== best.attempt - 1).map((a) => a.text).join('\n\n---\n\n') +
  `\n\nProduce the final, polished design document.`,
  { profile: tasteful, label: 'synthesize' })

return { winner: best, design: final }
