#!/usr/bin/env bash
# ab_run.sh — starts OFF and ON observer proxies, runs the GSM8K
# benchmark through both, then kills the proxies and exits.
#
# Usage: bash ab_run.sh [--samples N] [--offset N] [--out results.jsonl]

set -e

OBSERVER=/home/sdrona/superbased-observer/bin/observer
RUNNER=/home/sdrona/superbased-observer/compression-testing/compression_smoke.py
OFF_CFG=/tmp/ab-gsm8k/off/config.toml
ON_CFG=/tmp/ab-gsm8k/on/config.toml
SAMPLES=${SAMPLES:-50}
OFFSET=${OFFSET:-0}
OUT=${OUT:-/home/sdrona/superbased-observer/compression-testing/gsm8k_ab_results.jsonl}
SUMMARY_OUT=${OUT%.jsonl}.summary.json

export PATH=/usr/local/go/bin:$HOME/.local/bin:$PATH
export HF_HUB_OFFLINE=1
export HF_DATASETS_OFFLINE=1

# Load credentials from .env
source <(grep -v '^#' /home/sdrona/superbased-observer/.env | sed 's/^/export /')

cleanup() {
    echo "Stopping proxies..."
    kill $OFF_PID $ON_PID 2>/dev/null || true
    wait $OFF_PID $ON_PID 2>/dev/null || true
}
trap cleanup EXIT

# Fresh DBs
rm -f /tmp/ab-gsm8k/off/observer.db /tmp/ab-gsm8k/on/observer.db

# Start OFF proxy (compression disabled, port 8831)
$OBSERVER proxy start --config $OFF_CFG > /tmp/ab-gsm8k/off/proxy.log 2>&1 &
OFF_PID=$!
echo "OFF proxy PID=$OFF_PID"

# Start ON proxy (compression enabled, port 8832)
$OBSERVER proxy start --config $ON_CFG > /tmp/ab-gsm8k/on/proxy.log 2>&1 &
ON_PID=$!
echo "ON proxy PID=$ON_PID"

sleep 5

# Verify both are listening
ss -ltnp | grep -E "8831|8832" || { echo "ERROR: proxies not listening"; exit 1; }

# Preflight: single request through each arm
echo "=== PREFLIGHT: OFF proxy ==="
curl -s --max-time 15 http://127.0.0.1:8831/v1/chat/completions \
  -H "Authorization: Bearer $AZURE_OPENAI_API_KEY" \
  -H "api-key: $AZURE_OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"Kimi-K2.6","messages":[{"role":"user","content":"what is 2+2? answer only the number"}],"max_tokens":5}' \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print('OFF response:', d.get('choices',[{}])[0].get('message',{}).get('content','ERROR')[:60])"

echo "=== PREFLIGHT: ON proxy ==="
curl -s --max-time 15 http://127.0.0.1:8832/v1/chat/completions \
  -H "Authorization: Bearer $AZURE_OPENAI_API_KEY" \
  -H "api-key: $AZURE_OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"Kimi-K2.6","messages":[{"role":"user","content":"what is 2+2? answer only the number"}],"max_tokens":5}' \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print('ON response:', d.get('choices',[{}])[0].get('message',{}).get('content','ERROR')[:60])"

# Verify DB rows exist in both DBs after preflight
sleep 2
echo "=== DB PREFLIGHT CHECK ==="
for arm in off on; do
    DB=/tmp/ab-gsm8k/$arm/observer.db
    ROWS=$(sqlite3 "$DB" "SELECT COUNT(*) FROM api_turns;" 2>/dev/null || echo "0")
    COMP=$(sqlite3 "$DB" "SELECT COALESCE(SUM(compression_original_bytes),0) FROM api_turns;" 2>/dev/null || echo "0")
    echo "$arm: api_turns=$ROWS compression_original_bytes=$COMP"
done

# Run the full benchmark
echo "=== RUNNING BENCHMARK: samples=$SAMPLES offset=$OFFSET ==="
python3 $RUNNER \
    --samples $SAMPLES \
    --offset $OFFSET \
    --split test \
    --out $OUT \
    --summary-out $SUMMARY_OUT \
    --arms off on \
    --few-shot 0

# Post-run DB verification
echo "=== DB POST-RUN CHECK ==="
for arm in off on; do
    DB=/tmp/ab-gsm8k/$arm/observer.db
    sqlite3 "$DB" "SELECT '$arm', COUNT(*), COALESCE(SUM(compression_original_bytes),0), COALESCE(SUM(compression_compressed_bytes),0), COALESCE(SUM(compression_count),0), COALESCE(SUM(compression_dropped_count),0) FROM api_turns;" 2>/dev/null \
        | awk -F'|' '{printf "%-5s turns=%-4s orig_bytes=%-8s comp_bytes=%-8s comp_count=%-4s drop_count=%s\n",$1,$2,$3,$4,$5,$6}'
done

echo "Results: $OUT"
echo "Summary: $SUMMARY_OUT"
