// cmd/compress-bench/main.go
// Standalone compression measurement tool for the GSM8K benchmark.
// Reads a JSON request body from stdin, runs it through the conversation
// compression pipeline, and prints a JSON stats object to stdout.
//
// Usage:
//   echo '{"model":"...", "messages":[...]}' | compress-bench --provider openai
//
// Output JSON:
//   {
//     "original_bytes": 1234,
//     "compressed_bytes": 987,
//     "ratio": 0.80,
//     "skipped": false,
//     "compressed_count": 2,
//     "dropped_count": 0,
//     "marker_count": 0,
//     "events": [
//       {"mechanism":"json","original_bytes":400,"compressed_bytes":120}
//     ]
//   }

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/marmutapp/superbased-observer/internal/compression/conversation"
	"github.com/marmutapp/superbased-observer/internal/scrub"
)

type result struct {
	OriginalBytes   int     `json:"original_bytes"`
	CompressedBytes int     `json:"compressed_bytes"`
	Ratio           float64 `json:"ratio"`
	Skipped         bool    `json:"skipped"`
	CompressedCount int     `json:"compressed_count"`
	DroppedCount    int     `json:"dropped_count"`
	MarkerCount     int     `json:"marker_count"`
	Events          []event `json:"events"`
}

type event struct {
	Mechanism       string `json:"mechanism"`
	OriginalBytes   int    `json:"original_bytes"`
	CompressedBytes int    `json:"compressed_bytes"`
}

func main() {
	provider    := flag.String("provider", "openai", "Provider: openai or anthropic")
	targetRatio := flag.Float64("target-ratio", 0.85, "Compression target ratio [0,1]")
	preserveN   := flag.Int("preserve-last-n", 5, "Number of tail messages to never drop")
	flag.Parse()

	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
		os.Exit(1)
	}

	cfg := conversation.PipelineConfig{
		Enabled:       true,
		Mode:          "cache_aware",
		TargetRatio:   *targetRatio,
		PreserveLastN: *preserveN,
		CompressTypes: []string{"json", "logs", "code", "tools"},
	}

	p := conversation.NewPipeline(cfg, conversation.DefaultRegistry(), scrub.New())
	r := p.RunInSessionContext(context.Background(), *provider, body, "bench-session")

	var events []event
	for _, e := range r.Events {
		events = append(events, event{
			Mechanism:       e.Mechanism,
			OriginalBytes:   e.OriginalBytes,
			CompressedBytes: e.CompressedBytes,
		})
	}

	ratio := 1.0
	if r.OriginalBytes > 0 {
		ratio = float64(r.CompressedBytes) / float64(r.OriginalBytes)
	}

	out := result{
		OriginalBytes:   r.OriginalBytes,
		CompressedBytes: r.CompressedBytes,
		Ratio:           ratio,
		Skipped:         r.Skipped,
		CompressedCount: r.CompressedCount,
		DroppedCount:    r.DroppedCount,
		MarkerCount:     r.MarkerCount,
		Events:          events,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}
