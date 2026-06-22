"""Select n=2 pilot instances for Gate 2 compression testing."""
import json
from datasets import load_dataset

# Load SWE-bench Verified
ds = load_dataset("princeton-nlp/SWE-bench_Verified", split="test")
print(f"Total instances: {len(ds)}")

# Filter for multi-file problems (more likely to have large tool outputs)
candidates = []
for instance in ds:
    patch = instance.get("patch", "")
    # Count files modified in patch
    files = set()
    for line in patch.split("\n"):
        if line.startswith("diff --git"):
            parts = line.split()
            if len(parts) >= 3:
                files.add(parts[2])
    
    if len(files) >= 2:  # Multi-file change
        candidates.append({
            "instance_id": instance["instance_id"],
            "repo": instance["repo"],
            "files_modified": len(files),
            "problem_statement": instance["problem_statement"][:200] + "..."
        })

print(f"\nMulti-file instances: {len(candidates)}")
print("\nSelecting n=2 pilot:")
pilot = candidates[:2]
for inst in pilot:
    print(f"\n{inst['instance_id']}")
    print(f"  Repo: {inst['repo']}")
    print(f"  Files: {inst['files_modified']}")
    print(f"  Problem: {inst['problem_statement'][:100]}...")

# Write to file
with open("pilot_instances.json", "w") as f:
    json.dump([inst["instance_id"] for inst in pilot], f, indent=2)

print(f"\n✅ Wrote pilot_instances.json")
