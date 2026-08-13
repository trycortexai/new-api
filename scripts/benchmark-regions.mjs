#!/usr/bin/env node

const REGIONS = {
  '--sg': 'https://cortex-new-api-sg-4n9o9.ondigitalocean.app',
  '--nyc': 'https://newapi.withcortex.ai',
}

const MODELS = ['claude-sonnet-5', 'gpt-5.6-luna']
const RUNS = 5

function usage() {
  console.error(`Usage:
  NEW_API_BENCHMARK_KEY=... node scripts/benchmark-regions.mjs --sg
  NEW_API_BENCHMARK_KEY=... node scripts/benchmark-regions.mjs --nyc`)
}

function percentile(values, p) {
  const sorted = [...values].sort((a, b) => a - b)
  return sorted[Math.ceil(sorted.length * p) - 1]
}

const [region, ...extraArgs] = process.argv.slice(2)

if (!REGIONS[region] || extraArgs.length > 0) {
  usage()
  process.exit(2)
}

if (!process.env.NEW_API_BENCHMARK_KEY) {
  console.error('NEW_API_BENCHMARK_KEY is required')
  usage()
  process.exit(2)
}

const baseUrl = REGIONS[region]
console.log(`Region: ${region.slice(2).toUpperCase()}`)
console.log(`Base URL: ${baseUrl}`)
console.log(`Runs per model: ${RUNS}\n`)

for (const model of MODELS) {
  const results = []

  for (let run = 1; run <= RUNS; run += 1) {
    const started = performance.now()
    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${process.env.NEW_API_BENCHMARK_KEY}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        model,
        messages: [{ role: 'user', content: 'echo hi' }],
        max_tokens: 20,
        stream: true,
      }),
    })

    if (!response.ok) {
      throw new Error(
        `${model} run ${run}: HTTP ${response.status} ${await response.text()}`,
      )
    }

    if (!response.body) {
      throw new Error(`${model} run ${run}: response body is empty`)
    }

    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let ttft
    let bytes = 0
    let streamText = ''

    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      if (value?.length) {
        bytes += value.length
        streamText += decoder.decode(value, { stream: true })
        ttft ??= Math.round(performance.now() - started)
      }
    }

    streamText += decoder.decode()

    if (ttft === undefined) {
      throw new Error(`${model} run ${run}: stream returned no body bytes`)
    }

    if (!streamText.includes('data: [DONE]')) {
      throw new Error(`${model} run ${run}: stream ended without data: [DONE]`)
    }

    const e2e = Math.round(performance.now() - started)
    results.push({ ttft, e2e })
    console.log(`${model} run ${run}: TTFT ${ttft} ms, E2E ${e2e} ms, ${bytes} bytes`)
  }

  const ttfts = results.map(({ ttft }) => ttft)
  const e2es = results.map(({ e2e }) => e2e)

  console.log(`${model} summary:`)
  console.log(`  success: ${results.length}/${RUNS}`)
  console.log(`  TTFT p50 / p95: ${percentile(ttfts, 0.5)} / ${percentile(ttfts, 0.95)} ms`)
  console.log(`  E2E  p50 / p95: ${percentile(e2es, 0.5)} / ${percentile(e2es, 0.95)} ms\n`)
}
