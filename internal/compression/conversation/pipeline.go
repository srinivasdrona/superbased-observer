package conversation

import (
	"context"
	"strings"

	"github.com/marmutapp/superbased-observer/internal/compression/conversation/types"
	"github.com/marmutapp/superbased-observer/internal/scrub"
	"github.com/marmutapp/superbased-observer/internal/stash"
)

// PipelineConfig mirrors the [config.ConversationConfig] knobs the
// pipeline consults at runtime. Copied (rather than shared) so config
// reloads don't race with in-flight compression.
type PipelineConfig struct {
	// Enabled gates the whole pipeline. When false, Run is a no-op that
	// returns the original body and OriginalBytes == CompressedBytes.
	Enabled bool
	// Mode selects drop strategy: [ModeToken] or [ModeCache]. Empty
	// string defaults to [ModeToken].
	Mode string
	// TargetRatio is the fraction of the input byte count to target.
	// Defaults to 0.85 when zero.
	TargetRatio float64
	// PreserveLastN messages are pinned at score 1.0 and are never
	// dropped.
	PreserveLastN int
	// CompressTypes names the content types eligible for per-type
	// compression. An empty map means "no per-type compression" (drops
	// still run).
	CompressTypes []string
	// PrefixBytes is the byte budget used to compute the
	// message_prefix_hash. Defaults to 8KB.
	PrefixBytes int
}

// Pipeline is the top-level compression driver the proxy wires into its
// pre-forward path. Safe for concurrent use after construction.
type Pipeline struct {
	cfg                    PipelineConfig
	registry               *Registry
	scrubber               *scrub.Scrubber
	stash                  *stash.Stash
	stashThreshold         int
	summarizerFactory      SummarizerFactory
	summarizerByProvider   map[string]SummarizerFactory
	rollingThresholdTokens int
	summaryCache           *summaryCache
	rollingState           *rollingState
}

// Summarizer condenses a slice of messages into a single short
// summary string suitable for replacing them inline. Implementations
// typically call a small model (Claude Haiku) but the interface stays
// API-agnostic so tests can mock it.
type Summarizer interface {
	Summarize(ctx context.Context, msgs []Message) (string, error)
}

// SummarizerFactory returns the Summarizer to use for a given session.
// Production passes a factory that pulls the session's most recently
// observed Anthropic Authorization header out of an in-memory cache so
// the summary call rides the same auth as the user's regular
// requests. Returns nil when no Summarizer is available for the
// session — the pipeline degrades gracefully (rolling-summ no-ops).
type SummarizerFactory interface {
	For(sessionID string) Summarizer
}

// NewPipeline constructs a Pipeline. Either registry or scrubber may be
// nil: a nil registry disables per-type compression; a nil scrubber
// disables the final scrub pass. Nil scrubber is only appropriate for
// tests — production callers MUST pass a real scrubber.
func NewPipeline(cfg PipelineConfig, registry *Registry, scrubber *scrub.Scrubber) *Pipeline {
	return &Pipeline{cfg: cfg, registry: registry, scrubber: scrubber}
}

// WithSummarizerFactory attaches a per-session summarizer factory for
// the D20 (rolling summarisation) feature. When the conversation token
// estimate exceeds thresholdTokens, the pipeline calls
// `factory.For(sessionID).Summarize(...)` on the messages older than
// the preserve-last-N tail and replaces them with a synthetic system
// message carrying the summary.
//
// Returns the same pipeline pointer for chaining. Pass nil factory or
// thresholdTokens ≤ 0 to disable.
//
// Token estimation: rough char-count / 4 heuristic. Good enough to
// pick a "is this conversation long?" threshold; not load-bearing for
// correctness.
func (p *Pipeline) WithSummarizerFactory(f SummarizerFactory, thresholdTokens int) *Pipeline {
	p.summarizerFactory = f
	p.rollingThresholdTokens = thresholdTokens
	if p.rollingState == nil {
		p.rollingState = newRollingState()
	}
	return p
}

// WithSummarizerFactoryFor binds a [SummarizerFactory] to a specific
// provider so the pipeline picks the right summariser at request time.
// Multiple calls accumulate — bind one factory per provider.
//
// Example:
//
//	pipeline.
//	    WithSummarizerFactoryFor("anthropic", anthFactory, 80000).
//	    WithSummarizerFactoryFor("openai",   oaiFactory,  80000)
//
// The thresholdTokens parameter sets a SHARED token threshold (the
// pipeline holds one). The first call's value sticks; subsequent calls
// only update the factory map. To use different thresholds per
// provider, run separate pipelines.
//
// The legacy [Pipeline.WithSummarizerFactory] still works — it sets a
// fallback factory used when the request's provider isn't in the
// per-provider map. Pass the same factory to both methods if you want
// it to fire on every provider.
func (p *Pipeline) WithSummarizerFactoryFor(provider string, f SummarizerFactory, thresholdTokens int) *Pipeline {
	if p.summarizerByProvider == nil {
		p.summarizerByProvider = make(map[string]SummarizerFactory, 2)
	}
	p.summarizerByProvider[provider] = f
	if p.rollingThresholdTokens == 0 {
		p.rollingThresholdTokens = thresholdTokens
	}
	if p.rollingState == nil {
		p.rollingState = newRollingState()
	}
	return p
}

// summarizerFor returns the [SummarizerFactory] bound to the given
// provider, falling back to the legacy field set via
// [Pipeline.WithSummarizerFactory]. Returns nil when neither is set.
func (p *Pipeline) summarizerFor(provider string) SummarizerFactory {
	if f, ok := p.summarizerByProvider[provider]; ok && f != nil {
		return f
	}
	return p.summarizerFactory
}

// WithStash attaches a content-addressed stash for the CCR (Compressed
// Content Retrieval) feature. When set, tool_result bodies whose post-
// per-type-compression size still exceeds thresholdBytes are written
// to the stash and replaced inline with a deterministic marker; the
// model retrieves originals via the `retrieve_stashed` MCP tool. Pass
// nil to disable. Returns the same pipeline pointer for chaining.
//
// thresholdBytes defaults to 8192 when ≤ 0.
func (p *Pipeline) WithStash(s *stash.Stash, thresholdBytes int) *Pipeline {
	if thresholdBytes <= 0 {
		thresholdBytes = 8192
	}
	p.stash = s
	p.stashThreshold = thresholdBytes
	return p
}

// PipelineResult carries the compressed body plus per-turn counters for
// observability.
type PipelineResult struct {
	// Body is the compressed, scrubbed request body ready to forward
	// upstream. When Skipped, Body is the unmodified input.
	Body []byte
	// Skipped is true when the pipeline did nothing (disabled, non-JSON
	// body, non-Anthropic provider). Stats fields still populate
	// OriginalBytes / CompressedBytes with len(body).
	Skipped bool
	// Provider the pipeline dispatched against. Empty for non-supported
	// providers.
	Provider string
	// MessagePrefixHash is a stable SHA-256 of the first
	// [PipelineConfig.PrefixBytes] bytes of message content after
	// compression. Empty when no messages were observable.
	MessagePrefixHash string
	// OriginalBytes is len(body) before compression.
	OriginalBytes int
	// CompressedBytes is len(Body) after compression.
	CompressedBytes int
	// CompressedCount is the number of tool_result messages whose
	// bodies were rewritten by a per-type compressor.
	CompressedCount int
	// DroppedCount is the number of source messages replaced by a
	// marker.
	DroppedCount int
	// MarkerCount is the number of marker messages emitted (consecutive
	// drops collapse into one marker).
	MarkerCount int
	// MessageCount is the number of messages in the final body.
	MessageCount int
	// Events is the per-decision detail (one record per compress or
	// drop). Empty when the pipeline skipped or no decisions were made.
	// Persisted into compression_events by the store layer.
	Events []Event
}

// Run compresses body when the pipeline is enabled and the provider is
// supported. Returns the original body unchanged on any skip path so the
// proxy can always forward.
//
// Run is the legacy entry point kept for tests and callers that don't
// have a session_id available. Production callers should use
// [Pipeline.RunInSessionContext] so v1.4.42+ features (rolling-summ,
// read-cache auto-substitution, post-compact injection) can fire —
// they need session-scoped state.
func (p *Pipeline) Run(provider string, body []byte) PipelineResult {
	return p.RunInSession(provider, body, "")
}

// RunInSession is the session-aware variant of [Pipeline.Run]. The
// `sessionID` is used by features that need cross-turn state within a
// single session (D20 rolling summaries, C16 read-cache auto-
// substitution). Pass "" for sessionID when no session context is
// available — the pipeline degrades gracefully (those features no-op).
//
// Equivalent to [Pipeline.RunInSessionContext] with a background
// context. Production callers (the proxy) should use the context-
// aware form so HTTP-bearing features (D20 summariser) respect the
// proxy request's cancellation.
func (p *Pipeline) RunInSession(provider string, body []byte, sessionID string) PipelineResult {
	return p.RunInSessionContext(context.Background(), provider, body, sessionID)
}

// RunInSessionContext is the v1.4.43+ context-aware entry point that
// the proxy adapter calls so D20 rolling summarisation HTTP requests
// respect the parent request's cancellation. Behaviour is otherwise
// identical to [Pipeline.RunInSession].
func (p *Pipeline) RunInSessionContext(ctx context.Context, provider string, body []byte, sessionID string) PipelineResult {
	result := PipelineResult{
		Body:            body,
		Skipped:         true,
		Provider:        provider,
		OriginalBytes:   len(body),
		CompressedBytes: len(body),
	}
	if !p.cfg.Enabled || len(body) == 0 {
		return result
	}
	switch provider {
	case "anthropic":
		return p.runAnthropic(ctx, provider, body, result, sessionID)
	case "openai":
		return p.runOpenAI(ctx, provider, body, result, sessionID)
	default:
		return result
	}
}

// runAnthropic handles the Messages API body: extract → per-block
// pre-compression of tool_result bodies → score → enforce → serialize
// (with cache_control injection when mode=cache) → scrub.
//
// Per-block pre-compression runs before scoring so Score and Enforce
// see the post-compression byte counts and the budget pass uses
// accurate sizes. It also fixes a long-standing bug where messages
// carrying parallel tool_results (Claude Code's common multi-Read
// pattern) had Enforce's flat-text compression silently discarded by
// the serializer.
func (p *Pipeline) runAnthropic(ctx context.Context, provider string, body []byte, skipped PipelineResult, sessionID string) PipelineResult {
	envelope, extracted, ok := anthropicExtract(body)
	if !ok || len(extracted) == 0 {
		return skipped
	}
	cacheAware := strings.EqualFold(p.cfg.Mode, ModeCacheAware)
	preserveMsg, preserveBlock := -1, -1
	if cacheAware {
		if mIdx, bIdx, found := findLastCacheBreakpoint(extracted); found {
			preserveMsg, preserveBlock = mIdx, bIdx
		} else {
			// No SDK marker → cache-aware mode no-ops gracefully (turn-1
			// or non-Claude-Code Anthropic clients). Fall back to the
			// effective behaviour of ModeToken without drops by leaving
			// cacheAware true; compressToolResults still runs on every
			// tool_result block, Enforce skips the drop pass.
		}
	}
	// Order: C16 read-cache substitution first (most aggressive — drops
	// the body entirely and skips per-type), then per-type compression
	// on what's left, then tool-definitions trim, then CCR stash on
	// anything still over the threshold.
	readCacheEvents := substituteRedundantReads(extracted, sessionID, preserveMsg, preserveBlock)
	preEvents := compressToolResults(extracted, p.registry, buildAllow(p.cfg.CompressTypes), preserveMsg, preserveBlock)
	preEvents = append(readCacheEvents, preEvents...)
	toolDefEvents := compressToolDefinitions(envelope)
	preEvents = append(preEvents, toolDefEvents...)
	if p.stash != nil {
		stashEvents := stashLargeBodies(extracted, p.stash, p.stashThreshold, preserveMsg, preserveBlock)
		preEvents = append(preEvents, stashEvents...)
	}
	msgs := toConversationMessages(extracted)
	// D20 rolling summarisation: when the conversation has crossed
	// the token threshold, replace older messages with a summary
	// marker so subsequent turns stay under Anthropic's context cap.
	// Sticky boundary preserves cross-turn invariance — see
	// rolling.go::summarizeIfThreshold for the full contract.
	if rollingMsgs, rollingEvents := p.summarizeIfThreshold(ctx, msgs, sessionID, provider); len(rollingEvents) > 0 {
		msgs = rollingMsgs
		preEvents = append(preEvents, rollingEvents...)
	}
	br, splitAt, prefixHash := p.compressMessagesCacheAware(msgs, len(body), cacheAware, preserveMsg, preserveBlock)
	if len(preEvents) > 0 {
		br.Stats.Events = append(preEvents, br.Stats.Events...)
		br.Stats.CompressedCount += len(preEvents)
	}

	// Fast path: when nothing changed (no compression events, no drops,
	// no cache_control injection), forward the original body unchanged.
	// Re-marshaling through serializeAnthropic+marshalEnvelope rewrites
	// HTML chars (`<` → `<`) and reorders top-level keys, producing
	// byte-different output that's semantically identical but causes:
	//   1. Anthropic prompt-cache lookup to miss (byte-hash sensitive).
	//   2. Subtle on-wire differences in system-reminder tags that the
	//      model may sample around differently.
	// This was the root cause of the live A/B regression where ON took
	// ~2× turns on multi-file exploration prompts vs OFF — the model's
	// tool-use strategy diverged because we were silently reformatting
	// the body.
	wantInject := strings.EqualFold(p.cfg.Mode, ModeCache) && splitAt > 0
	if len(br.Stats.Events) == 0 && br.Stats.DroppedCount == 0 && !wantInject {
		scrubbed := body
		if p.scrubber != nil {
			scrubbed = []byte(p.scrubber.String(string(body)))
		}
		return PipelineResult{
			Body:              scrubbed,
			Skipped:           false,
			Provider:          provider,
			MessagePrefixHash: prefixHash,
			OriginalBytes:     len(body),
			CompressedBytes:   len(scrubbed),
			MessageCount:      br.Stats.MessageCount,
		}
	}

	cacheBreakpointIdx := -1
	if wantInject {
		cacheBreakpointIdx = splitAt - 1
	}
	newBody, err := serializeAnthropic(envelope, extracted, br.Messages, cacheBreakpointIdx)
	if err != nil {
		return skipped
	}
	return p.finalize(provider, body, newBody, br, prefixHash)
}

// runOpenAI handles Chat Completions and Responses API bodies. Cache_control
// not applied — Chat Completions has no standard cache marker today, and
// Responses API uses session-keyed `prompt_cache_key` rather than block
// markers.
//
// Order of operations mirrors runAnthropic's pre-pass chain: read-cache
// substitution → per-type compression on tool outputs → tool-definitions
// trim → G31 stash on anything still oversized → score+enforce.
//
// Fast-path early-return when zero events fire is critical — OpenAI's
// `prompt_cache_key`-keyed cache is byte-hash sensitive (both wires use
// the same key), and re-marshaling through serializeOpenAI* +
// marshalEnvelope rewrites HTML chars and reorders top-level keys,
// producing byte-different output that misses the cache. Mirrors the
// Anthropic-side fix pinned by `TestPipelineCacheAware_Determinism`.
func (p *Pipeline) runOpenAI(ctx context.Context, provider string, body []byte, skipped PipelineResult, sessionID string) PipelineResult {
	envelope, extracted, ok := openaiExtract(body)
	if ok && len(extracted) > 0 {
		// Chat Completions full pre-pass chain (mirror of Responses API).
		// Order: C16 read-cache (most aggressive — drops the body
		// entirely) → per-type → tool-defs trim → CCR stash on anything
		// still over threshold → score+enforce.
		readCacheEvents := substituteOpenAIChatRedundantReads(extracted, sessionID)
		preEvents := compressOpenAIChatToolResults(extracted, p.registry, buildAllow(p.cfg.CompressTypes))
		preEvents = append(readCacheEvents, preEvents...)
		toolDefEvents := compressOpenAIChatToolDefinitions(envelope)
		preEvents = append(preEvents, toolDefEvents...)
		if p.stash != nil {
			stashEvents := stashOpenAIChatLargeBodies(extracted, p.stash, p.stashThreshold)
			preEvents = append(preEvents, stashEvents...)
		}

		msgs := openaiToConversationMessages(extracted)
		// D20 rolling summarisation — same gate / sticky-boundary
		// contract as the other two providers.
		if rollingMsgs, rollingEvents := p.summarizeIfThreshold(ctx, msgs, sessionID, provider); len(rollingEvents) > 0 {
			msgs = rollingMsgs
			preEvents = append(preEvents, rollingEvents...)
		}
		br, _, prefixHash := p.compressMessages(msgs, len(body))
		if len(preEvents) > 0 {
			br.Stats.Events = append(preEvents, br.Stats.Events...)
			br.Stats.CompressedCount += len(preEvents)
		}

		// Fast-path early-return: forward the original body unchanged
		// when nothing fired so OpenAI's `prompt_cache_key` cache lookup
		// hits. Re-serializing reorders map keys and rewrites HTML
		// chars, producing byte-different output that breaks cache
		// prefixes even when semantically identical.
		if len(br.Stats.Events) == 0 && br.Stats.DroppedCount == 0 {
			scrubbed := body
			if p.scrubber != nil {
				scrubbed = []byte(p.scrubber.String(string(body)))
			}
			return PipelineResult{
				Body:              scrubbed,
				Skipped:           false,
				Provider:          provider,
				MessagePrefixHash: prefixHash,
				OriginalBytes:     len(body),
				CompressedBytes:   len(scrubbed),
				MessageCount:      br.Stats.MessageCount,
			}
		}

		newBody, err := serializeOpenAI(envelope, extracted, br.Messages, -1)
		if err != nil {
			return skipped
		}
		return p.finalize(provider, body, newBody, br, prefixHash)
	}

	responsesEnvelope, responsesExtracted, responsesOK := openaiResponsesExtract(body)
	if !responsesOK || len(responsesExtracted) == 0 {
		return skipped
	}

	// Tier 0/1 pre-pass chain. Order matches runAnthropic:
	// read-cache (most aggressive — drops the body entirely) → per-type
	// → tool-defs trim → CCR stash on anything still over threshold.
	mcpSet := collectOpenAIMCPToolNames(responsesEnvelope)
	readCacheEvents := substituteOpenAIRedundantReads(responsesExtracted, sessionID)
	preEvents := compressOpenAIResponsesToolResults(responsesExtracted, p.registry, buildAllow(p.cfg.CompressTypes), mcpSet)
	preEvents = append(readCacheEvents, preEvents...)
	toolDefEvents := compressOpenAIResponsesToolDefinitions(responsesEnvelope)
	preEvents = append(preEvents, toolDefEvents...)
	if p.stash != nil {
		stashEvents := stashOpenAIResponsesLargeBodies(responsesExtracted, p.stash, p.stashThreshold, mcpSet)
		preEvents = append(preEvents, stashEvents...)
	}

	msgs := openaiResponsesToConversationMessages(responsesExtracted, mcpSet)
	// D20 rolling summarisation: when the conversation has crossed the
	// token threshold, replace older messages with a summary marker so
	// subsequent turns stay under the OpenAI Responses API context cap.
	// Sticky boundary preserves cross-turn invariance — see
	// rolling.go::summarizeIfThreshold for the full contract.
	if rollingMsgs, rollingEvents := p.summarizeIfThreshold(ctx, msgs, sessionID, provider); len(rollingEvents) > 0 {
		msgs = rollingMsgs
		preEvents = append(preEvents, rollingEvents...)
	}
	br, _, prefixHash := p.compressMessages(msgs, len(body))
	if len(preEvents) > 0 {
		br.Stats.Events = append(preEvents, br.Stats.Events...)
		br.Stats.CompressedCount += len(preEvents)
	}

	// Fast-path early-return: when nothing changed, forward the original
	// body unchanged so codex's `prompt_cache_key` cache lookup hits.
	// Re-serializing through json.Marshal reorders top-level map keys
	// and rewrites HTML chars (`<` → `<`), producing byte-different
	// output that breaks cache prefixes even when semantically identical.
	if len(br.Stats.Events) == 0 && br.Stats.DroppedCount == 0 {
		scrubbed := body
		if p.scrubber != nil {
			scrubbed = []byte(p.scrubber.String(string(body)))
		}
		return PipelineResult{
			Body:              scrubbed,
			Skipped:           false,
			Provider:          provider,
			MessagePrefixHash: prefixHash,
			OriginalBytes:     len(body),
			CompressedBytes:   len(scrubbed),
			MessageCount:      br.Stats.MessageCount,
		}
	}

	newBody, err := serializeOpenAIResponses(responsesEnvelope, responsesExtracted, br.Messages)
	if err != nil {
		return skipped
	}
	return p.finalize(provider, body, newBody, br, prefixHash)
}

// compressMessages runs the provider-agnostic middle of the pipeline:
// score → enforce → split. The scrubber is NOT applied here — each
// caller scrubs the serialized body in [finalize].
//
// originalBodyBytes is the size of the request body BEFORE any
// pre-compression. Passing it through to Enforce keeps the budget
// reference anchored to the true original size, so per-block
// pre-compression (anthropic.go::compressToolResults) shrinking
// individual tool_result bodies before scoring doesn't proportionally
// shrink the drop-trigger threshold.
func (p *Pipeline) compressMessages(msgs []Message, originalBodyBytes int) (BudgetResult, int, string) {
	return p.compressMessagesCacheAware(msgs, originalBodyBytes, false, -1, -1)
}

// compressMessagesCacheAware is the cache-aware-aware variant. When
// cacheAware is true Enforce skips drops and narrows per-type
// compression to RoleTool only (anthropic.go runAnthropic supplies
// preserveMsg/preserveBlock — currently unused inside Enforce because
// per-block compression already happens in compressToolResults; kept
// in the signature to keep the path explicit at the call site).
func (p *Pipeline) compressMessagesCacheAware(msgs []Message, originalBodyBytes int, cacheAware bool, preserveMsg, preserveBlock int) (BudgetResult, int, string) {
	_ = preserveMsg
	_ = preserveBlock
	scored := Score(msgs, ScoreOptions{PreserveLastN: p.cfg.PreserveLastN})
	compressAllow := buildAllow(p.cfg.CompressTypes)
	br := Enforce(scored, BudgetOptions{
		TargetRatio:       p.cfg.TargetRatio,
		Registry:          p.registry,
		CompressTypes:     compressAllow,
		Mode:              p.cfg.Mode,
		OriginalBodyBytes: originalBodyBytes,
		CacheAware:        cacheAware,
	})
	prefixBytes := p.cfg.PrefixBytes
	if prefixBytes <= 0 {
		prefixBytes = 8 * 1024
	}
	splitAt := SplitIndex(br.Messages, prefixBytes)
	prefixHash := PrefixHash(br.Messages, splitAt)
	return br, splitAt, prefixHash
}

// finalize scrubs the serialized body and packs the PipelineResult so
// both provider paths share the same exit shape.
func (p *Pipeline) finalize(provider string, original, newBody []byte, br BudgetResult, prefixHash string) PipelineResult {
	if p.scrubber != nil {
		newBody = []byte(p.scrubber.String(string(newBody)))
	}
	return PipelineResult{
		Body:              newBody,
		Skipped:           false,
		Provider:          provider,
		MessagePrefixHash: prefixHash,
		OriginalBytes:     len(original),
		CompressedBytes:   len(newBody),
		CompressedCount:   br.Stats.CompressedCount,
		DroppedCount:      br.Stats.DroppedCount,
		MarkerCount:       br.Stats.MarkerCount,
		MessageCount:      br.Stats.MessageCount,
		Events:            br.Stats.Events,
	}
}

// buildAllow converts a string slice of type names (from config.toml)
// into a ContentType set for the budget enforcer.
func buildAllow(names []string) map[types.ContentType]bool {
	if len(names) == 0 {
		return nil
	}
	allow := make(map[types.ContentType]bool, len(names))
	for _, n := range names {
		allow[types.ContentType(n)] = true
	}
	return allow
}
