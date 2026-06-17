# Second-Round Critique: Observer Compression Benchmarking Plan

> **Critic**: GPT-5.4 persona — 1M context reasoning model with strong
> SWE-bench and agent-benchmarks performance. Evaluates the revised plan
> critically, focusing on systemic rigor, statistical validity, and
> practical feasibility.
> **Target**: `compression-testing/PLAN.md` (revision incorporating Kimi K2.6 critique)
> **Date**: 2026-06-16

---

## Revised Assessment

The first-round critique (from Kimi K2.6) was incorporated well. The plan
now mandates real traces, adds compression-toxicity measurement, requires
blind pairwise evaluation, stratifies SWE-bench by difficulty, reports
billable vs raw tokens, and replaces unsupported Gemini with Claude Haiku
4.6. **Revised rating: 8.5/10.** The plan is now publication-ready with one
structural concern and several detail-level gaps.

---

## Structural Concern

### 1. Compression toxicity measurement is underspecified (reopened)

The plan says: "For each compressed body, a discriminator model answers
'can you still answer the user's next query correctly?' Diff against
uncompressed baseline."

This is the right concept but needs a protocol. As written, it's a
one-shot LLM judgment that can be gamed or misinterpreted:

- **Problem A — calibration drift**: The discriminator model (e.g., Kimi
  K2.6, Sonnet 4.6) has its own failure modes. If the discriminator
  hallucinates a task it can't answer, that's a discriminator problem,
  not a compression problem. Without a calibration set — asking the
  discriminator to answer tasks from uncompressed context and measuring
  its baseline error rate — you can't subtract discriminator noise from
  the toxicity signal.

- **Problem B — "can answer" is binary but quality is continuous**: A
  compressed context might still allow a correct answer but with worse
  code quality (less idiomatic, missing edge case). The discriminator
  marking "yes you can answer" doesn't catch quality degradation.

- **Problem C — prompt leakage**: If the discriminator sees the compressed
  body AND the uncompressed body, it can infer which is which from the
  compression markers (`[N messages compressed]`, `<string>` sentinels).
  This breaks evaluator blindness.

**Recommendation**:
1. Run the discriminator on uncompressed context first. Measure its
   baseline task-completion accuracy. This becomes the calibration ceiling.
2. Then run on compressed context. Toxicity = `(calibration_accuracy -
   compressed_accuracy)` — not a standalone binary judgment.
3. Add a quality step: for tasks the discriminator marks as "can answer,"
   have it generate the answer and run the repo's tests against it.
   Measure pass rate delta between compressed and uncompressed contexts.
4. Never show both compressed and uncompressed to the same discriminator
   instance — randomize which bodies go to which evaluator.

### 2. Statistical power is unstated

The plan uses 50 SWE-bench instances for Gate 3. With 50 instances and an
expected % Resolved of ~70% (Sonnet 4.6 on Verified), the 95% confidence
interval is approximately ±13 percentage points. A "≤ 3% absolute
degradation" threshold is inside that noise floor — you cannot
statistically distinguish a true 3% drop from random variance at n=50.

**Recommendation**: Either:
- Increase to n=200 instances (CI narrows to ±6.5pp) — more expensive
  but makes the ≤ 3% threshold meaningful, or
- Acknowledge the statistical limitation: report the threshold as "≤ 3%
  absolute degradation (measured; not statistically significant at n=50;
  interpret directionally)," or
- Use a different primary metric: instead of % Resolved Δ, use **cost
  per resolved instance** — `total_cost_ON / n_resolved_ON` vs
  `total_cost_OFF / n_resolved_OFF`. This metric incorporates both
  accuracy and token savings into a single efficiency score that is
  less sensitive to instance-level stochasticity.

### 3. Gate 2 sample size is too small for reliable pairwise comparison

5 coding tasks with 2 evaluators each = 10 pairwise judgments. If
compression-ON wins 4 of 10 comparisons, is 40% "below the 40%
threshold" or statistical noise? The plan's "≥ 40%" threshold has no
confidence bound at this sample size.

**Recommendation**: Increase to ≥ 15 tasks. At 15 tasks with 2 evaluators
(30 judgments), a 40% win rate has a 95% CI of approximately [22%, 60%] —
still wide but actionable directionally. Report the CI alongside the point
estimate.

## Detail-Level Gaps

### 4. Agent selection for Gate 3 is binary but incomplete

The plan says "Claude Code and Codex CLI, 25 instances each." These are
both CLI-based agents with similar interaction patterns (ReAct loop,
tool calls per turn). A structurally different agent — OpenHands/CodeAct
or Aider — would stress different compression paths. OpenHands uses a
`str_replace_editor` tool that produces very different tool_result shapes
from Claude Code's Unix-shell tool set.

**Recommendation**: Consider adding a third agent for completeness,
or document that the two-agent choice covers the dominant agent
paradigm and is a deliberate scope constraint.

### 5. The "native caching only" baseline arm has an agent-switching confound

Gate 2 and Gate 3 add a third arm: native provider caching with no
observer proxy. But this arm also switches the ANTHROPIC_BASE_URL back
to the real API endpoint, which means the agent itself might behave
differently (different latency profiles affect retry logic, different
error modes). This confounds the comparison: you can't attribute
differences solely to compression vs native caching.

**Recommendation**: For the native-caching baseline, keep the observer
proxy in the path but set `compression.conversation.enabled = false`
AND disable routing, guard, and all other proxy middleware. The proxy
should be a transparent passthrough. This keeps agent behavior
identical between arms. The native caching comparison then becomes:
does the observer's intelligent compression beat Anthropic's/OpenAI's
passive caching, given identical agent behavior?

### 6. Kimi K2.6 API compatibility check is absent

The plan lists Kimi K2.6 as "Anthropic-compatible" in the envelope
column, but Kimi's API is OpenAI-compatible (`api.moonshot.cn/v1`),
not Anthropic Messages API-compatible. The observer's `providerForPath()`
dispatches by URL path (`/v1/messages` → Anthropic, `/v1/chat/completions`
→ OpenAI). Kimi K2.6 would use the OpenAI chat path, not the Anthropic
Messages path.

**Recommendation**: Correct the envelope column for DeepSeek-V4-Pro and
Kimi K2.6 from "Anthropic-compatible" to "OpenAI-compatible." Validate
that `MOONSHOT_BASE_URL=https://api.moonshot.cn/v1` works through the
observer proxy before Gate 2. Alternatively, if DeepSeek-V4-Pro uses
Anthropic Messages API format, keep it as-is — verify independently.

### 7. Compression toxicity calibration cost is not budgeted

Running a discriminator model against every compressed body doubles the
API cost. If Gate 1 uses 30 sessions × 4 turn bodies per session × 7
models = 840 discriminator calls. At Kimi K2.6 pricing (¥6.50/M input,
¥27/M output, ~10K tokens/call), that's approximately ¥150 (~$20). Gate
3 adds another 50 instances × 2 arms × ~20 turns × discriminator =
2,000 calls. This is small but should be line-itemed.

**Recommendation**: Add a budget line for toxicity measurement:
- Gate 1: ~$20 discriminator costs
- Gate 3: ~$50 discriminator costs
- Total: ~$70 additional (negligible relative to the $500–2000/model
  SWE-bench cost)

### 8. No mechanism for detecting harmful dropped messages post-hoc

The plan adds "check if dropped messages are referenced by later turns"
as a proxy for harmful drops but doesn't specify how. Later turns in
Anthropic Messages API reference tool_use blocks by `tool_use_id`, not
by message index. If a tool_result is dropped from the history and the
model never references it, the drop was harmless. If it was referenced,
you can detect the missing dependency chain.

**Recommendation**: Parse the uncompressed session's message chain.
Build a dependency graph: tool_use → tool_result → assistant response
referencing both. For each dropped tool_result, check if any
subsequent assistant message contains the tool's output (verbatim string
match, not just ID reference — models paraphrase). This gives a concrete
"harmful drop %" metric that doesn't require a discriminator model.

### 9. The 800K–1M body size stress test may produce artifacts

At >500K bytes, the observer's `Pipeline` may hit memory allocation
pressure that doesn't exist in production (because real agents never
produce 1M-byte request bodies — the API rejects them at the provider
level). Anthropic's Messages API has a ~10MB request limit but practical
agent sessions rarely exceed 400K bytes before cache breakpoints reset
the window.

**Recommendation**: Keep the stress test but add a realism check:
report what fraction of real sessions actually exceed 500K/800K/1M
bytes. If it's <1% of real traces, demote the stress test to
informational (not gating).

### 10. No observability into per-model tuning decisions

The plan captures per-mechanism breakdown but doesn't specify how
results feed back into config tuning. If Sonnet 4.6 gets 40% savings
from log compression but Codex-5.3 gets 5%, the `compress_types`
allow-list should differ per model. The plan doesn't close the loop.

**Recommendation**: Add a "Per-Model Tuning" section: after Gate 1
completes, for each model, compute the optimal `compress_types` subset
(mechanisms that contribute ≥ 2% savings with toxicity ≤ 2%). Use
these model-specific profiles in Gates 2 and 3. This makes the
benchmark self-improving rather than just observational.

---

## Updated Priority Table

| # | Fix | Impact | Applies To |
|---|---|---|---|
| 1 | Protocolize toxicity measurement | High | Gate 1 |
| 2 | Address statistical power at n=50 | High | Gate 3 |
| 3 | Increase Gate 2 tasks to ≥ 15 | Medium | Gate 2 |
| 7 | Add toxicity calibration budget | Low | Budget doc |
| 6 | Correct Kimi/DeepSeek API compatibility | Medium | Model table |
| 8 | Add harmful-drop mechanic detection | Medium | All gates |
| 10 | Per-model tuning feedback loop | Medium | All gates |

---

## Final Verdict

The plan is strong. The incorporation of first-round feedback was
thorough. The remaining issues are real but addressable: toxicity
measurement needs calibration (not one-shot), statistical power at n=50
is insufficient for the stated ≤ 3% threshold, and per-model tuning
should close the loop from measurement to configuration. Fix these and
this is a 9.5/10 benchmarking plan that any observability/agent-infra
project would be proud to publish.

**Recommended next action**: Address fixes 1, 2, and 3 before
commencing Gate 1 build. The other 7 fixes can be incorporated
incrementally during implementation.