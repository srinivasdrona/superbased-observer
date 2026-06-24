#!/usr/bin/env python3
"""
Gate 2 Verdict: SWE-bench Verified Compression Testing
Compares Arm OFF (compression disabled) vs Arm ON (compression enabled)

Metrics:
- % Resolved delta (does compression hurt accuracy?)
- Token savings (billable tokens saved?)
- Cost per resolved (compression cost-effective?)
- Byte compression ratio (how much compressed?)
- Per-mechanism breakdown (json/code/logs/tools/drop)

Usage:
  python gate2_verdict.py \
    --off-db ~/swe-bench-3slot-work/observer/observer-off.db \
    --on-db ~/swe-bench-3slot-work/observer/observer-on.db \
    --off-results ~/swe-bench-3slot-work/gate2_off \
    --on-results ~/swe-bench-3slot-work/gate2_on \
    --n-instances 50
"""

import argparse
import sqlite3
import json
from pathlib import Path
from collections import Counter

def load_resolved(results_dir):
    """
    Scan arm results dir for resolved instances.
    An instance is considered resolved if its patch passes the harness.
    
    The done marker is: <instance>/repair_sample_1/output_0_processed.jsonl
    This file contains the final patch. The harness grades it separately.
    
    For now, we use existence of output_0_processed.jsonl as a proxy for "completed".
    Actual resolution is determined by the SWE-bench harness scoring later.
    
    Returns: set of instance IDs that completed
    """
    completed = []
    results_path = Path(results_dir)
    if not results_path.exists():
        return set()
    
    for instance_dir in results_path.iterdir():
        if not instance_dir.is_dir():
            continue
        
        done_mark = instance_dir / "repair_sample_1" / "output_0_processed.jsonl"
        if done_mark.exists():
            completed.append(instance_dir.name)
    
    return set(completed)

def load_observer_stats(db_path):
    """
    Extract token counts, costs, compression stats from observer.db
    
    Schema (expected):
    - input_tokens, output_tokens, cache_read_tokens
    - cost_usd
    - compression_original_bytes, compression_compressed_bytes
    - compression_count, compression_dropped_count
    - compression_events (JSON with per-mechanism breakdown)
    """
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()
    
    # Check if table exists
    cursor.execute("SELECT name FROM sqlite_master WHERE type='table' AND name='api_turns'")
    if not cursor.fetchone():
        conn.close()
        return {
            'n_turns': 0,
            'input_tokens': 0,
            'output_tokens': 0,
            'cache_read_tokens': 0,
            'cost_usd': 0.0,
            'original_bytes': 0,
            'compressed_bytes': 0,
            'compression_events': 0,
            'dropped_count': 0,
            'mechanism_breakdown': {},
        }
    
    # Aggregate across all turns
    cursor.execute("""
        SELECT 
            COUNT(*) as n_turns,
            COALESCE(SUM(input_tokens), 0) as total_input,
            COALESCE(SUM(output_tokens), 0) as total_output,
            COALESCE(SUM(cache_read_tokens), 0) as total_cache_read,
            COALESCE(SUM(cost_usd), 0.0) as total_cost,
            COALESCE(SUM(compression_original_bytes), 0) as total_original_bytes,
            COALESCE(SUM(compression_compressed_bytes), 0) as total_compressed_bytes,
            COALESCE(SUM(compression_count), 0) as total_compression_events,
            COALESCE(SUM(compression_dropped_count), 0) as total_dropped
        FROM api_turns
    """)
    row = cursor.fetchone()
    
    # Extract per-mechanism breakdown from compression_events JSON
    cursor.execute("SELECT compression_events FROM api_turns WHERE compression_events IS NOT NULL")
    mechanism_counts = Counter()
    for (events_json,) in cursor.fetchall():
        if events_json:
            try:
                events = json.loads(events_json)
                for event in events:
                    mech = event.get('mechanism', 'unknown')
                    mechanism_counts[mech] += 1
            except json.JSONDecodeError:
                pass
    
    conn.close()
    
    return {
        'n_turns': row[0],
        'input_tokens': row[1],
        'output_tokens': row[2],
        'cache_read_tokens': row[3],
        'cost_usd': row[4],
        'original_bytes': row[5],
        'compressed_bytes': row[6],
        'compression_events': row[7],
        'dropped_count': row[8],
        'mechanism_breakdown': dict(mechanism_counts),
    }

def main():
    parser = argparse.ArgumentParser(description='Gate 2 verdict computor')
    parser.add_argument('--off-db', required=True, help='observer-off.db path')
    parser.add_argument('--on-db', required=True, help='observer-on.db path')
    parser.add_argument('--off-results', required=True, help='gate2_off results dir')
    parser.add_argument('--on-results', required=True, help='gate2_on results dir')
    parser.add_argument('--n-instances', type=int, default=50, help='Total instances in test')
    parser.add_argument('--output', default='gate2_verdict.json', help='Output JSON artifact')
    args = parser.parse_args()
    
    # Load completed sets (not yet graded by harness)
    off_completed = load_resolved(args.off_results)
    on_completed = load_resolved(args.on_results)
    
    # Load observer stats
    off_stats = load_observer_stats(args.off_db)
    on_stats = load_observer_stats(args.on_db)
    
    # Compute deltas
    n_off = len(off_completed)
    n_on = len(on_completed)
    delta_completed = n_on - n_off
    delta_pp = (delta_completed / args.n_instances) * 100 if args.n_instances > 0 else 0
    
    # Token savings (input tokens are what we compress)
    token_savings_pct = 0
    if off_stats['input_tokens'] > 0:
        token_savings_pct = ((off_stats['input_tokens'] - on_stats['input_tokens']) / 
                             off_stats['input_tokens'] * 100)
    
    # Byte compression ratio
    byte_ratio = 1.0
    if off_stats['original_bytes'] > 0:
        byte_ratio = on_stats['compressed_bytes'] / off_stats['original_bytes']
    
    # Cost per completed
    cost_per_completed_off = off_stats['cost_usd'] / n_off if n_off > 0 else 0
    cost_per_completed_on = on_stats['cost_usd'] / n_on if n_on > 0 else 0
    cost_delta_pct = 0
    if cost_per_completed_off > 0:
        cost_delta_pct = ((cost_per_completed_on - cost_per_completed_off) / 
                          cost_per_completed_off * 100)
    
    # Print verdict
    print("=" * 70)
    print("GATE 2 VERDICT: Observer Compression on SWE-bench Verified (n=50)")
    print("=" * 70)
    print(f"")
    print(f"OFF (compression disabled, port 8831):")
    print(f"  Completed: {n_off}/{args.n_instances} ({n_off/args.n_instances*100:.1f}%)")
    print(f"  Tokens: {off_stats['input_tokens']:,} input + {off_stats['output_tokens']:,} output")
    print(f"  Turns: {off_stats['n_turns']}")
    print(f"  Cost: ${off_stats['cost_usd']:.2f} total, ${cost_per_completed_off:.2f} per completed")
    print(f"")
    print(f"ON (compression enabled, port 8832):")
    print(f"  Completed: {n_on}/{args.n_instances} ({n_on/args.n_instances*100:.1f}%)")
    print(f"  Tokens: {on_stats['input_tokens']:,} input + {on_stats['output_tokens']:,} output")
    print(f"  Turns: {on_stats['n_turns']}")
    print(f"  Cost: ${on_stats['cost_usd']:.2f} total, ${cost_per_completed_on:.2f} per completed")
    print(f"")
    print(f"DELTA:")
    print(f"  Completed: {delta_completed:+d} ({delta_pp:+.1f} pp)")
    print(f"  Input token savings: {token_savings_pct:+.1f}%")
    print(f"  Byte compression ratio: {byte_ratio:.3f} ({(1-byte_ratio)*100:.1f}% saved)")
    print(f"  Cost per completed: ${cost_per_completed_on:.2f} vs ${cost_per_completed_off:.2f} ({cost_delta_pct:+.1f}%)")
    print(f"")
    print(f"Compression stats (ON arm only):")
    print(f"  Original bytes: {on_stats['original_bytes']:,}")
    print(f"  Compressed bytes: {on_stats['compressed_bytes']:,}")
    print(f"  Compression events: {on_stats['compression_events']}")
    print(f"  Messages dropped: {on_stats['dropped_count']}")
    if on_stats['mechanism_breakdown']:
        print(f"  Per-mechanism breakdown:")
        for mech, count in sorted(on_stats['mechanism_breakdown'].items()):
            print(f"    {mech}: {count}")
    print(f"")
    
    # Pass/fail decision (based on Gate 2 plan criteria)
    verdict = "PASS"
    reasons = []
    
    # Criterion 1: % Completed delta ≤ 3pp (1.5 instances at n=50)
    if delta_completed < -1:  # -2 or worse = >4pp degradation at n=50
        verdict = "FAIL"
        reasons.append(f"Completed rate dropped {abs(delta_completed)} instances ({delta_pp:.1f} pp)")
    
    # Criterion 2: Input token savings ≥ 10%
    if token_savings_pct < 10:
        if verdict != "FAIL":
            verdict = "PARTIAL"
        reasons.append(f"Token savings {token_savings_pct:.1f}% < 10% threshold")
    
    # Criterion 3: Cost per completed: ON ≤ OFF
    if cost_per_completed_on > cost_per_completed_off:
        if verdict != "FAIL":
            verdict = "PARTIAL"
        reasons.append(f"Cost per completed increased {cost_delta_pct:+.1f}%")
    
    # Criterion 4: Byte compression ratio ≤ 0.80 (≥ 20% saved)
    if byte_ratio > 0.80:
        if verdict != "FAIL":
            verdict = "PARTIAL"
        reasons.append(f"Byte savings {(1-byte_ratio)*100:.1f}% < 20% threshold")
    
    print(f"VERDICT: {verdict}")
    if reasons:
        print(f"Reasons:")
        for reason in reasons:
            print(f"  - {reason}")
    print("=" * 70)
    print(f"")
    print(f"NOTE: 'Completed' means Agentless finished all 4 phases and wrote")
    print(f"      output_0_processed.jsonl. Actual resolution (patch passes tests)")
    print(f"      is determined by running the SWE-bench harness separately.")
    print(f"")
    
    # Write JSON artifact
    artifact = {
        'metadata': {
            'n_instances': args.n_instances,
            'test_set': 'pilot_subset_verified_multifile_n50',
        },
        'off': {
            'completed': n_off,
            'completed_pct': n_off/args.n_instances*100,
            **off_stats
        },
        'on': {
            'completed': n_on,
            'completed_pct': n_on/args.n_instances*100,
            **on_stats
        },
        'delta': {
            'completed': delta_completed,
            'completed_pp': delta_pp,
            'token_savings_pct': token_savings_pct,
            'byte_compression_ratio': byte_ratio,
            'byte_savings_pct': (1 - byte_ratio) * 100,
            'cost_per_completed_off': cost_per_completed_off,
            'cost_per_completed_on': cost_per_completed_on,
            'cost_delta_pct': cost_delta_pct,
        },
        'verdict': verdict,
        'reasons': reasons if reasons else None,
    }
    
    output_path = Path(args.output)
    with open(output_path, 'w') as f:
        json.dump(artifact, f, indent=2)
    
    print(f"Artifact written to {output_path.absolute()}")

if __name__ == '__main__':
    main()
