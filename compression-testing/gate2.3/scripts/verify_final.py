import sqlite3

WIN_LO = "2026-06-23T07:30:00Z"
WIN_HI = "2026-06-24T12:00:00Z"

def q(dbpath, label):
    c = sqlite3.connect(dbpath)
    cur = c.cursor()
    # turn-level aggregates in window
    cur.execute("""
        SELECT COUNT(*), 
               COALESCE(SUM(input_tokens),0),
               COALESCE(SUM(output_tokens),0),
               COALESCE(SUM(cost_usd),0),
               COALESCE(SUM(compression_original_bytes),0),
               COALESCE(SUM(compression_compressed_bytes),0),
               MIN(timestamp), MAX(timestamp)
        FROM api_turns
        WHERE timestamp >= ? AND timestamp < ?
    """, (WIN_LO, WIN_HI))
    turns, intok, outtok, cost, cob, ccb, tmin, tmax = cur.fetchone()
    print(f"=== {label} ===")
    print(f"  window     : {tmin}  ->  {tmax}")
    print(f"  turns      : {turns}")
    print(f"  input_tok  : {intok:,}   ({intok/turns:.0f}/turn)")
    print(f"  output_tok : {outtok:,}")
    print(f"  cost_usd   : ${cost:.2f}")
    if cob:
        print(f"  comp bytes : {cob/1e6:.1f}MB -> {ccb/1e6:.1f}MB  ({100*(cob-ccb)/cob:.1f}% saved)")
    else:
        print(f"  comp bytes : (none / disabled)")
    c.close()
    return dict(turns=turns, intok=intok, outtok=outtok, cost=cost, cob=cob, ccb=ccb)

on = q("/tmp/g51-on.db", "ON (compression enabled)")
off = q("/tmp/g51-off.db", "OFF (compression disabled)")

print()
print("=== ON vs OFF deltas ===")
print(f"  input_tok/turn : ON {on['intok']/on['turns']:.0f}  vs OFF {off['intok']/off['turns']:.0f}  -> {100*((on['intok']/on['turns'])/(off['intok']/off['turns'])-1):+.1f}%")
print(f"  total input    : ON {on['intok']/1e6:.2f}M vs OFF {off['intok']/1e6:.2f}M  -> {100*(on['intok']/off['intok']-1):+.1f}%")
print(f"  cost (DB)      : ON ${on['cost']:.2f} vs OFF ${off['cost']:.2f}  -> {100*(on['cost']/off['cost']-1):+.1f}%")

# mechanism breakdown ON (compression_events in window)
print()
print("=== ON compression_events mechanism (in window) ===")
c = sqlite3.connect("/tmp/g51-on.db")
cur = c.cursor()
cur.execute("""
    SELECT e.mechanism, COUNT(*), COALESCE(SUM(e.original_bytes),0), COALESCE(SUM(e.compressed_bytes),0)
    FROM compression_events e
    JOIN api_turns t ON e.api_turn_id = t.id
    WHERE t.timestamp >= ? AND t.timestamp < ?
    GROUP BY e.mechanism
    ORDER BY SUM(e.original_bytes - e.compressed_bytes) DESC
""", (WIN_LO, WIN_HI))
rows = cur.fetchall()
tot_saved = sum(o-cc for _,_,o,cc in rows)
tot_orig = sum(o for _,_,o,_ in rows)
tot_comp = sum(cc for _,_,_,cc in rows)
for mech, cnt, o, cc in rows:
    saved = o-cc
    pct_self = 100*saved/o if o else 0
    pct_share = 100*saved/tot_saved if tot_saved else 0
    print(f"  {mech:6s}: events={cnt:5d}  {o/1e6:6.2f}MB -> {cc/1e6:6.2f}MB  self={pct_self:5.1f}%  share={pct_share:5.1f}%")
print(f"  TOTAL : events={sum(r[1] for r in rows):5d}  {tot_orig/1e6:6.2f}MB -> {tot_comp/1e6:6.2f}MB  saved={100*tot_saved/tot_orig:5.1f}%")
c.close()
