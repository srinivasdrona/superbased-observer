import sqlite3

# UTC batch-window boundaries (derived from IST folder names + first-turn timestamps)
batches = [
    ("b1", "2026-06-23T08:30:00Z", "2026-06-23T14:38:00Z"),
    ("b2", "2026-06-23T14:38:00Z", "2026-06-23T19:54:00Z"),
    ("b3", "2026-06-23T19:54:00Z", "2026-06-24T07:00:00Z"),
    ("b4", "2026-06-24T07:00:00Z", "2026-06-24T10:46:00Z"),
    ("b5", "2026-06-24T10:46:00Z", "2026-06-24T12:00:00Z"),
]

# trajectory turns per batch (authoritative final-traj api_calls)
traj = {
    "ON":  {"b1":506,"b2":455,"b3":424,"b4":453,"b5":404},
    "OFF": {"b1":456,"b2":406,"b3":404,"b4":392,"b5":396},
}

def db_turns(dbpath, lo, hi):
    c = sqlite3.connect(dbpath); cur = c.cursor()
    cur.execute("SELECT COUNT(*), COALESCE(SUM(cost_usd),0) FROM api_turns WHERE timestamp>=? AND timestamp<?", (lo,hi))
    n, cost = cur.fetchone(); c.close()
    return n, cost

for arm, db in [("ON","/tmp/g51-on.db"), ("OFF","/tmp/g51-off.db")]:
    print(f"=== {arm} ===")
    print(f"  {'batch':5s} {'DB turns':>9s} {'traj turns':>11s} {'retries':>8s} {'DB cost':>9s}")
    tot_r = 0
    for b, lo, hi in batches:
        n, cost = db_turns(db, lo, hi)
        t = traj[arm][b]
        r = n - t
        tot_r += r
        print(f"  {b:5s} {n:9d} {t:11d} {r:8d} {cost:9.2f}")
    print(f"  total retries (DB - traj): {tot_r}")
    print()
