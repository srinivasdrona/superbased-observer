package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/marmutapp/superbased-observer/internal/compression/conversation"
	"github.com/marmutapp/superbased-observer/internal/scrub"
)

func TestSmoke_CompressionSavesBytes(t *testing.T) {
	scrubber := scrub.New()
	cfg := conversation.PipelineConfig{
		Enabled:       true,
		Mode:          "token",
		TargetRatio:   0.5,
		PreserveLastN: 5,
		CompressTypes: []string{"json", "logs", "text", "code"},
	}
	p := conversation.NewPipeline(cfg, conversation.DefaultRegistry(), scrubber)

	type fixture struct {
		label    string
		provider string
		body     []byte
	}

	fixtures := []fixture{
		{
			label:    "Anthropic — logs-heavy tool result",
			provider: "anthropic",
			body:     buildAnthropicLogsBody(t),
		},
		{
			label:    "Anthropic — JSON tool result (80 records)",
			provider: "anthropic",
			body:     buildAnthropicBigJSONBody(t),
		},
		{
			label:    "Anthropic — 10-turn conversation with code",
			provider: "anthropic",
			body:     buildAnthropicMultiTurnBody(t),
		},
		{
			label:    "OpenAI Chat — multi-turn with tool calls",
			provider: "openai",
			body:     buildOpenAIChatMultiTurnBody(t),
		},
		{
			label:    "Anthropic — compressed = disabled (control)",
			provider: "anthropic",
			body:     buildAnthropicLogsBody(t),
		},
	}

	fmt.Println()
	fmt.Println("========================================")
	fmt.Println(" Compression Smoke Test")
	fmt.Println("========================================")

	for i, fx := range fixtures {
		if i == 4 {
			cfgOff := cfg
			cfgOff.Enabled = false
			p = conversation.NewPipeline(cfgOff, conversation.DefaultRegistry(), scrubber)
		}

		result := p.RunInSessionContext(context.Background(), fx.provider, fx.body, "smoke-01")

		origBytes := result.OriginalBytes
		compBytes := result.CompressedBytes
		saved := origBytes - compBytes
		pct := 0.0
		if origBytes > 0 {
			pct = float64(saved) * 100.0 / float64(origBytes)
		}

		fmt.Printf("\n[%s]\n", fx.label)
		fmt.Printf("  Body:      %d bytes\n", len(fx.body))
		fmt.Printf("  Original:  %d bytes\n", origBytes)
		fmt.Printf("  Compressed:%d bytes\n", compBytes)
		fmt.Printf("  Saved:     %d bytes (%.1f%%)\n", saved, pct)

		if result.Skipped {
			fmt.Printf("  Status:    SKIPPED (compression disabled or no-op)\n")
		} else {
			fmt.Printf("  Events:    compressed=%d dropped=%d markers=%d\n",
				result.CompressedCount, result.DroppedCount, result.MarkerCount)
			for _, ev := range result.Events {
				fmt.Printf("    → [%s] %d → %d bytes\n",
					ev.Mechanism, ev.OriginalBytes, ev.CompressedBytes)
			}
		}

		if !cfg.Enabled || result.Skipped {
			continue
		}
		if compBytes >= origBytes && origBytes > 0 {
			t.Errorf("%s: compressed %d >= original %d (did not shrink)",
				fx.label, compBytes, origBytes)
		}
	}

	fmt.Println()
	fmt.Println("========================================")
}

func buildAnthropicLogsBody(t *testing.T) []byte {
	t.Helper()
	logs := strings.Repeat("ERROR: something went wrong at line 42\n", 100) +
		strings.Repeat("WARN: deprecated API called\n", 50) +
		strings.Repeat("INFO: request processed\n", 200)

	body := map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 4096,
		"messages": []any{
			map[string]any{"role": "user", "content": "Debug the test failures."},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_01",
						"name":  "Bash",
						"input": map[string]any{"command": "pytest tests/"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_01",
						"content":     logs,
					},
				},
			},
		},
	}
	return mustMarshal(t, body)
}

func buildAnthropicBigJSONBody(t *testing.T) []byte {
	t.Helper()
	records := make([]map[string]any, 80)
	for i := 0; i < 80; i++ {
		records[i] = map[string]any{
			"id":          i,
			"name":        fmt.Sprintf("api_endpoint_%d", i),
			"method":      "POST",
			"path":        fmt.Sprintf("/v1/resource/%d", i),
			"description": "Handles CRUD operations for resource type with full validation and error handling pipeline",
			"parameters":  []string{"param_a", "param_b", "param_c"},
			"headers": map[string]any{
				"Content-Type":  "application/json",
				"Authorization": "Bearer <redacted>",
				"X-Request-ID":  "abc-123-def",
			},
		}
	}

	bigJSON := mustMarshal(t, records)
	body := map[string]any{
		"model": "claude-sonnet-4-20250514",
		"messages": []any{
			map[string]any{"role": "user", "content": "analyze these records"},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_02",
						"content":     string(bigJSON),
					},
				},
			},
		},
	}
	return mustMarshal(t, body)
}

func buildAnthropicMultiTurnBody(t *testing.T) []byte {
	t.Helper()
	messages := []any{
		map[string]any{"role": "user", "content": "Please help me build a REST API."},
	}
	for i := 0; i < 10; i++ {
		messages = append(messages, map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": fmt.Sprintf("Let me read file_%d.py...", i),
				},
				map[string]any{
					"type":  "tool_use",
					"id":    fmt.Sprintf("toolu_%02d", i*2),
					"name":  "Read",
					"input": map[string]any{"file_path": fmt.Sprintf("file_%d.py", i)},
				},
			},
		})
		messages = append(messages, map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": fmt.Sprintf("toolu_%02d", i*2),
					"content":     fmt.Sprintf("# file_%d.py\n\ndef handler_%d():\n    pass\n", i, i),
				},
			},
		})
	}
	body := map[string]any{
		"model":    "claude-sonnet-4-20250514",
		"messages": messages,
	}
	return mustMarshal(t, body)
}

func buildOpenAIChatMultiTurnBody(t *testing.T) []byte {
	t.Helper()
	messages := []map[string]any{
		{"role": "system", "content": "You are a coding assistant."},
	}
	for i := 0; i < 8; i++ {
		messages = append(messages, map[string]any{
			"role":    "user",
			"content": fmt.Sprintf("Fix bug %d in the codebase.", i),
		})
		messages = append(messages, map[string]any{
			"role":       "assistant",
			"content":    fmt.Sprintf("I'll fix bug %d by reading the relevant file.", i),
			"tool_calls": []any{},
		})
	}
	body := map[string]any{
		"model":    "gpt-5",
		"messages": messages,
	}
	return mustMarshal(t, body)
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}