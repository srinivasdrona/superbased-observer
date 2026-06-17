# Critique of Observer Compression Benchmarking Plan

> **Critic**: Kimi K2.6 persona — reasoning model with strong SWE-bench
> and long-context code generation capabilities.
> **Target**: `compression-testing/PLAN.md` — Observer Compression Benchmarking Plan
> **Date**: 2026-06-16

---

## Overall Assessment

The plan is well-structured with a sensible gated approach — offline
trace replay before live A/B before full SWE-bench. The evaluation
rubric has clear quantitative thresholds. **Rating: 7/10.** Strong
foundation with several addressable gaps.

---

## Critical Gaps

### 1. Gate 1 fixture realism is under-specified

The plan says "captured request bodies from real agent coding sessions
(OR synthetic request generators)." The "OR" is the problem. Synthetic
fixtures (`buildClaudeCodeShapedBody`, `buildBigJSON`) will produce
favorable compression ratios because they lack the messiness of real
agent traffic:

- Real tool outputs contain ANSI escape sequences, shell prompts,
  backspaces, progress bars, mixed line endings — the exact content
  that makes log compression valuable
- Real code has inconsistent indentation, commented-out blocks,
  inline TODOs — not the clean generated bodies in test helpers
- Real sessions have unpredictable message ordering, tool-call chaining,
  and interleaved read/write/grep bursts that stress the message scoring algorithm

**Recommendation**: Gate 1 must require real traces. The synthetic
generators should only exist as a smoke test. Capture 30+ real
sessions across Claude Code and Codex CLI on repos of varying sizes
(express, django, the observer repo itself). These become the fixture
corpus. Delete the "OR synthetic" escape hatch.

### 2. No compression-toxicity measurement

The plan measures byte reduction but ignores what compression REMOVES.
Every dropped tool output, every truncated log tail, every JSON
field replaced with a type sentinel is information the model can no
longer see. The rubric only catches this at Gate 3 via SWE-bench %
Resolved — a lagging indicator that costs $500–2000/model to surface.

**Recommendation**: Add a Gate 1 metric: **compression toxicity**.
For each compressed request body, a discriminator model (Kimi K2.6
or Sonnet 4.6) answers: "Given the compressed context, could you
still answer the user's next query correctly?" Compare against the
uncompressed baseline. This is a leading indicator — if toxicity > 5%,
tune compression profiles before spending on SWE-bench.

### 3. Gate 2 has no blind evaluation protocol

"Human eval" of output quality is mentioned but not defined. Without a
protocol, Gate 2 results are unreliable:
- Evaluators know which output came from compression ON vs OFF
- No inter-rater agreement metric
- No rubric for what "discerning degradation" means per task type

**Recommendation**: Gate 2 should use blind pairwise comparison.
Generate two code patches (ON and OFF) for the same task. Present both
to evaluators without labels. Ask: "Which implementation is more
correct / complete / maintainable?" If compression-ON wins ≥ 40% of
comparisons, compression is not causing degradation.

### 4. Missing agent diversity in Gate 3

The plan says "using a coding agent (Claude Code or Codex CLI)."
Singular. But compression interacts with agent behavior differently:
- Claude Code uses parallel tool execution (multiple Read/Grep/Glob in
  one turn) — more message-dropping pressure
- Codex CLI tends toward sequential tool calls — more read-cache hits
- OpenHands/CodeAct uses a completely different tool schema with
  `str_replace_editor` — the `tools` compression code path

Running only one agent gives a single-dimensional view. If Gate 3 goes
ahead with 50 instances, split across 2 agents (25 each). If budget
allows, 3 agents.

### 5. No stratification by instance difficulty

SWE-bench instances vary dramatically in complexity:
- Trivial: single-file typo fix, 1 test (flask, sympy simple issues)
- Moderate: multi-file logic change, 5–10 tests
- Hard: cross-module architecture change, 20+ tests, new edge cases

Compression is most dangerous on hard instances because they require
the model to track deep context. Trivial instances compress easily
without harm, inflating the aggregate metric.

**Recommendation**: Stratify the 50-instance sample by difficulty
(easy/medium/hard, each ~17 instances). Report % Resolved per
difficulty tier separately. If compression drops hard-tier resolution
by >5% but aggregate looks fine, the pipeline still has a problem.

### 6. No long-context stress testing

The plan mentions 1M context for GPT-5.4 but never tests near the
limit. The log compressor's `head+tail` strategy and the stash
threshold are tuned for typical 50K–200K request bodies. At 800K+
(which GPT-5.4 and Kimi K2.6 with 256K can accumulate across turns):
- Dedup tables hit memory limits
- Rolling summarisation triggers late or too aggressively
- Budget enforce operates on a tiny fraction of the total messages

**Recommendation**: Add a Gate 1 stress test: synthetic sessions
with 50+ turns and 1M+ byte request bodies. Measure compression ratio
and latency at 50K, 200K, 500K, and 1M body sizes to find the knee
point.

### 7. Cache-mode semantics conflated with token savings

The plan compares `ModeCache` and `ModeCacheAware` as if they're
compression strategies. They're not — they're cache-preservation
strategies that trade compression for prefix-cache hits. A model
with `ModeCacheAware` might have higher input tokens (less aggressive
compression) but lower _billable_ tokens (cache hits count as
discounted reads on Anthropic). The rubric's "input token savings"
metric doesn't distinguish billable from cache-read tokens.

**Recommendation**: Report three token numbers per run:
- Total input tokens (raw)
- Billable input tokens (after cache discounts)
- Cache-read tokens

Cost savings should be calculated from billable tokens, not raw tokens.

### 8. No mechanism-interaction analysis

The plan treats compression mechanisms as independent (`Per-Mechanism
Breakdown`). But they interact:
- Read-cache (C16) reduces tool_result size → fewer bytes in budget →
  budget enforcer drops fewer messages
- Log compression reduces content → codegraph symbol hints become less
  useful because the hints reference content that was compressed away
- JSON compressor rewrites keys → but later stashing is content-addressed
  on the compressed body, changing stash hit rates

**Recommendation**: Add a Gate 1 analysis: run the pipeline with each
mechanism individually disabled, measuring the marginal contribution
of each. Then run with all mechanisms on for the interaction effect.
The delta between "sum of individual savings" and "full pipeline
savings" reveals synergy/conflict.

### 9. Gemini 2.5 Pro inclusion is impractical

The observer's proxy `providerForPath()` dispatches to Anthropic and
OpenAI — there is no Google Gemini provider path. The "not directly
supported" note acknowledges this but still includes Gemini as Tier 2.
This creates a false promise.

**Recommendation**: Either drop Gemini until the proxy supports it
(requires a new provider path + Gemini-specific envelope extraction for
the conversation pipeline), or replace it with a model the observer
already proxies. Claude Haiku 4.6 or GPT-5.4-mini would fill the
"large context + budget" slot.

### 10. No baseline comparison to competitor methods

The observer isn't the only compression approach. Anthropic's native
prompt caching, OpenAI's automatic prompt caching, and prompt
compaction tools like `llm-compressor` or `promptimize` all address
the same problem. The plan evaluates the observer against "no
compression" but not against "alternative compression."

**Recommendation**: If feasible, add a third arm to Gate 2/3:
"native caching only" (no observer proxy, relying on Anthropic's
built-in cache). This answers: does the observer's intelligent
compression beat the provider's naive caching?

---

## Strengths (What Works Well)

1. **Gated approach prevents sunk cost.** Gate 1 is zero cost and
   catches fundamental issues before spending money.

2. **Per-mechanism breakdown is excellent.** Knowing which compressor
   contributes most savings makes tuning actionable.

3. **Failure conditions have remediation actions.** Each failure
   condition maps to a specific tuning action — this is rarely done
   this well.

4. **Custom coding-task battery is smart.** SWE-bench is a lagging
   indicator; task-type-specific stress tests reveal per-compressor
   issues early.

5. **The scoring system (recency/reference/density/role) is directly
   evaluable.** Gate 1 should also check if `drop` events correlate
   with messages that later turns reference — a proxy for harmful drops.

6. **Existing A/B infrastructure is leveraged.** Not reinventing the
   `scripts/ab-claude-*.sh` wheel.

7. **Per-model config profiles acknowledge model differences.**
   Opus 4.6 with 200K context and Sonnet 4.6 with 200K need different
   `preserve_last_n` — the plan implies this.

---

## Priority Fixes (Pre-Gate-1 Must-Do)

| # | Fix | Impact |
|---|---|---|
| 1 | Require real traces only (drop "OR synthetic") | High — synthetic fixtures give fake confidence |
| 3 | Define blind evaluation protocol for Gate 2 | High — human eval is Gate 2's primary metric |
| 7 | Report billable vs raw input tokens | High — cost is what matters, not byte counts |
| 9 | Drop or replace Gemini | Medium — avoids committing to unsupported path |
| 5 | Stratify SWE-bench by difficulty | Medium — prevents inflated aggregate metrics |

---

## Summary

The plan is sound but optimistic about fixture realism, misses
toxicity measurement as a leading indicator, conflates cache-mode
choices with compression savings, and lacks blind evaluation and
difficulty stratification. Fix the 10 gaps above, prioritizing
the 5 "pre-Gate-1" fixes, and this becomes an 8.5/10 plan suitable
for publication alongside the observer project.