package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/marmutapp/superbased-observer/internal/compression/conversation"
	"github.com/marmutapp/superbased-observer/internal/config"
	"github.com/marmutapp/superbased-observer/internal/db"
	"github.com/marmutapp/superbased-observer/internal/identity"
	"github.com/marmutapp/superbased-observer/internal/intelligence/compaction"
	"github.com/marmutapp/superbased-observer/internal/intelligence/cost"
	"github.com/marmutapp/superbased-observer/internal/messagesummary"
	"github.com/marmutapp/superbased-observer/internal/pidbridge"
	"github.com/marmutapp/superbased-observer/internal/proxy"
	"github.com/marmutapp/superbased-observer/internal/scrub"
	"github.com/marmutapp/superbased-observer/internal/stash"
	"github.com/marmutapp/superbased-observer/internal/store"
)

// newProxyCmd returns the `proxy` subcommand group. Currently only hosts
// `proxy start`; room for `proxy status` / `proxy stop` if we adopt a
// long-running daemon model later.
func newProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run the API reverse proxy for accurate token capture",
	}
	cmd.AddCommand(newProxyStartCmd())
	return cmd
}

func newProxyStartCmd() *cobra.Command {
	var (
		configPath string
		port       int
		bindAddr   string
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the proxy on localhost (point ANTHROPIC_BASE_URL / OPENAI_BASE_URL here)",
		Long: "Starts the API reverse proxy on localhost:<port>.\n" +
			"Claude Code: ANTHROPIC_BASE_URL=http://localhost:<port>\n" +
			"Codex:       OPENAI_BASE_URL=http://localhost:<port>/v1",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			p, cleanup, addr, err := buildProxy(ctx, configPath, port, bindAddr)
			if err != nil {
				return err
			}
			defer cleanup()

			fmt.Fprintf(cmd.OutOrStdout(), "proxy listening on %s — ctrl-c to stop\n", addr)
			if err := p.ListenAndServe(ctx, addr); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to config.toml (defaults to ~/.observer/config.toml)")
	cmd.Flags().IntVar(&port, "port", 0, "Override [proxy].port")
	cmd.Flags().StringVar(&bindAddr, "bind", "127.0.0.1", "Bind address (default localhost only)")
	return cmd
}

// buildProxy loads config, opens the DB, constructs the Proxy wired to the
// storage layer, and returns the Proxy + cleanup closure + resolved listen
// address. The db is closed by the cleanup.
func buildProxy(ctx context.Context, configPath string, portOverride int, bindAddr string) (*proxy.Proxy, func(), string, error) {
	cfg, err := config.Load(config.LoadOptions{GlobalPath: configPath})
	if err != nil {
		return nil, nil, "", fmt.Errorf("load config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Observer.DBPath), 0o755); err != nil {
		return nil, nil, "", fmt.Errorf("ensure db dir: %w", err)
	}
	database, err := db.Open(ctx, db.Options{Path: cfg.Observer.DBPath})
	if err != nil {
		return nil, nil, "", fmt.Errorf("open db %s: %w", cfg.Observer.DBPath, err)
	}
	s := store.New(database)

	port := cfg.Proxy.Port
	if portOverride > 0 {
		port = portOverride
	}
	if port <= 0 || port > 65535 {
		_ = database.Close()
		return nil, nil, "", fmt.Errorf("proxy: port %d out of range", port)
	}

	logger := newLogger(cfg.Observer.LogLevel)

	// Org attribution: stamp api_turns (and any other rows the proxy
	// inserts) with the enrolled org_id/user_email. No-op on solo-local
	// installs — NewStamper returns a no-op stamper when org_enrolment is
	// empty/absent, so the proxy behaves identically when not enrolled.
	if stmp, err := identity.NewStamper(ctx, database); err != nil {
		logger.Warn("proxy: org stamper init failed; api_turns will not be org-attributed", "err", err)
	} else {
		s = s.WithStamper(stmp)
	}

	// Wire the pid → session_id bridge. The SessionStart hook writes
	// entries; the proxy resolves a client's TCP remote addr through
	// /proc into an ancestor pid that owns the bridge row. Prune once
	// at startup so rebooted machines don't carry stale entries.
	bridge := pidbridge.New(database)
	if _, err := bridge.Prune(ctx, 6*time.Hour); err != nil {
		logger.Warn("pidbridge prune", "err", err)
	}
	resolver := pidbridge.NewProcResolver(bridge, "", 30*time.Second)

	opts := proxy.Options{
		AnthropicUpstream: cfg.Proxy.AnthropicUpstream,
		OpenAIUpstream:    cfg.Proxy.OpenAIUpstream,
		ChatGPTUpstream:   cfg.Proxy.ChatGPTUpstream,
		ForceChatGPTHTTP:  cfg.Proxy.ForceChatGPTHTTP,
		Sink:              s,
		ObserverLog:       s,
		SessionResolver:   resolver,
		CostComputer:      costEngineAdapter{e: cost.NewEngine(cfg.Intelligence)},
		Logger:            logger,
	}
	if cfg.Compression.Conversation.Enabled {
		var scrubber *scrub.Scrubber
		if cfg.Observer.Secrets.EnableScrubbing {
			scrubber = scrub.NewWithExtra(cfg.Observer.Secrets.ExtraPatterns)
		} else {
			scrubber = scrub.New()
		}
		pipeline := conversation.NewPipeline(conversation.PipelineConfig{
			Enabled:       cfg.Compression.Conversation.Enabled,
			Mode:          cfg.Compression.Conversation.Mode,
			TargetRatio:   cfg.Compression.Conversation.TargetRatio,
			PreserveLastN: cfg.Compression.Conversation.PreserveLastN,
			CompressTypes: cfg.Compression.Conversation.CompressTypes,
		}, conversation.DefaultRegistry(), scrubber)
		if cfg.Compression.Conversation.Stash.Enabled {
			st, err := stash.New(stash.Options{
				Dir:        cfg.Compression.Conversation.Stash.Dir,
				MaxTotalMB: cfg.Compression.Conversation.Stash.MaxTotalMB,
			})
			if err != nil {
				logger.Warn("proxy: stash init failed; CCR disabled", "err", err)
			} else {
				pipeline = pipeline.WithStash(st, cfg.Compression.Conversation.Stash.ThresholdBytes)
			}
		}
		if cfg.Compression.Conversation.Rolling.Enabled {
			authCache := messagesummary.NewAuthCache(cfg.Compression.Conversation.Rolling.AuthCacheSize)
			recorder := messagesummary.NewDBRecorder(database, costEnginePricingAdapter{e: cost.NewEngine(cfg.Intelligence)})
			anthFactory := messagesummary.NewFactory(authCache, messagesummary.SummarizerOptions{
				Model:    cfg.Compression.Conversation.Rolling.SummaryModel,
				Recorder: recorder,
			})
			oaiFactory := messagesummary.NewOpenAIFactory(authCache, messagesummary.OpenAISummarizerOptions{
				Model:    cfg.Compression.Conversation.Rolling.OpenAISummaryModel,
				Recorder: recorder,
			})
			pipeline = pipeline.
				WithSummarizerFactoryFor("anthropic", anthFactory, cfg.Compression.Conversation.Rolling.ThresholdTokens).
				WithSummarizerFactoryFor("openai", oaiFactory, cfg.Compression.Conversation.Rolling.ThresholdTokens)
			opts.AuthCache = authCacheAdapter{c: authCache}
		}
		opts.Compressor = pipelineAdapter{p: pipeline}
	}
	if cfg.Compression.Conversation.Compaction.InjectPostCompact {
		opts.PostCompactInjector = compaction.NewInjector(database)
	}

	p, err := proxy.New(opts)
	if err != nil {
		_ = database.Close()
		return nil, nil, "", err
	}
	addr := net.JoinHostPort(bindAddr, strconv.Itoa(port))
	cleanup := func() { _ = database.Close() }
	return p, cleanup, addr, nil
}

// pipelineAdapter bridges conversation.Pipeline (no context parameter —
// the pipeline is a pure function by design) to proxy.Compressor which
// is context-aware. Lives in cmd/observer so the conversation package
// stays free of the proxy interface.
type pipelineAdapter struct {
	p *conversation.Pipeline
}

// costEngineAdapter bridges (*cost.Engine).Compute to proxy.CostComputer
// — same pattern as pipelineAdapter for the conversation pipeline.
// Lives here so the proxy contract stays free of an intelligence-pkg
// import cycle.
type costEngineAdapter struct {
	e *cost.Engine
}

// Compute implements proxy.CostComputer.
func (a costEngineAdapter) Compute(model string, t proxy.CostTokens) (float64, bool) {
	if a.e == nil {
		return 0, false
	}
	return a.e.Compute(model, cost.TokenBundle{
		Input:           t.Input,
		Output:          t.Output,
		CacheRead:       t.CacheRead,
		CacheCreation:   t.CacheCreation,
		CacheCreation1h: t.CacheCreation1h,
	})
}

// Compress implements proxy.Compressor (the legacy non-session-aware
// path; calls into [Pipeline.Run] which delegates to RunInSession with
// an empty sessionID).
func (a pipelineAdapter) Compress(ctx context.Context, provider string, body []byte) proxy.CompressionResult {
	return a.CompressInSession(ctx, provider, body, "")
}

// CompressInSession implements proxy.SessionAwareCompressor. The proxy
// passes the extracted session_id (Anthropic Pro/Max OAuth path) so
// session-scoped v1.4.42 features (rolling summaries, read-cache
// auto-substitution) can fire.
func (a pipelineAdapter) CompressInSession(ctx context.Context, provider string, body []byte, sessionID string) proxy.CompressionResult {
	r := a.p.RunInSessionContext(ctx, provider, body, sessionID)
	events := make([]proxy.CompressionEvent, 0, len(r.Events))
	for _, e := range r.Events {
		events = append(events, proxy.CompressionEvent{
			Mechanism:       e.Mechanism,
			OriginalBytes:   e.OriginalBytes,
			CompressedBytes: e.CompressedBytes,
			MsgIndex:        e.MsgIndex,
			ImportanceScore: e.ImportanceScore,
		})
	}
	return proxy.CompressionResult{
		Body:              r.Body,
		Skipped:           r.Skipped,
		MessagePrefixHash: r.MessagePrefixHash,
		OriginalBytes:     r.OriginalBytes,
		CompressedBytes:   r.CompressedBytes,
		CompressedCount:   r.CompressedCount,
		DroppedCount:      r.DroppedCount,
		MarkerCount:       r.MarkerCount,
		Events:            events,
	}
}

// costEnginePricingAdapter bridges cost.Engine.Lookup → the
// messagesummary.PricingLookup interface so the DBRecorder can price
// summary calls without messagesummary importing the cost package
// (avoids the import cycle: messagesummary depends on conversation,
// conversation is consumed by cost via the dashboard).
type costEnginePricingAdapter struct {
	e *cost.Engine
}

func (a costEnginePricingAdapter) Lookup(model string) (messagesummary.Pricing, bool) {
	p, ok := a.e.Lookup(model)
	if !ok {
		return messagesummary.Pricing{}, false
	}
	return messagesummary.Pricing{
		Input:           p.Input,
		Output:          p.Output,
		CacheRead:       p.CacheRead,
		CacheCreation:   p.CacheCreation,
		CacheCreation1h: p.CacheCreation1h,
	}, true
}

// authCacheAdapter bridges messagesummary.AuthCache to proxy.AuthCache.
// Both have identically-shaped credential types but live in separate
// packages for import-cycle reasons; this adapter does the trivial
// field-by-field copy.
type authCacheAdapter struct {
	c *messagesummary.AuthCache
}

// Set implements proxy.AuthCache.
func (a authCacheAdapter) Set(sessionID string, creds proxy.AuthCredentials) {
	a.c.Set(sessionID, messagesummary.AuthCredentials{
		Authorization: creds.Authorization,
		APIKey:        creds.APIKey,
	})
}
