"""
Gate 2.4 SWE-agent runner — Gate 2.3 replay WITH cache_read_tokens capture.

This is byte-for-byte the Gate 2.3 design (50 instances × 2 arms × 3 reps,
5 batches of 10, max 10 patch attempts, harness after each batch). The ONLY
differences from Gate 2.3:
  - Proxies run the cache-fixed binary (bin/observer-2.4.exe, commit 439ff1b),
    which captures cache_read_tokens from the non-streaming Responses API
    (input_tokens_details.cached_tokens) — the gap that left Gate 2.3 cache-blind.
  - Ports 8841 (OFF) / 8842 (ON) and gate2.4 DBs/dirs, so Gate 2.3 is untouched.
  - A pre-flight cache-sanity guard aborts the run if caching is not active.

Compression knobs (ON arm) are identical to Gate 2.3.

Design:
  - 50 instances × 2 arms × 3 repetitions = 300 runs/arm, 600 total
  - Processed in 5 batches of 10 instances
  - Within each batch: all 3 reps × both arms run first, then harness fires
  - Retry per (instance, rep, arm): up to MAX_PATCH_ATTEMPTS until agent
    produces a non-empty submitted patch — harness verdict is final, no
    re-runs on resolution status
  - Checkpoint/resume: state.json updated after every instance run so a
    crash resumes from where it left off

Usage (run with the swebench WSL venv python, inside WSL):
  PROXY_HOST=<windows-host-ip> python run_gate2_4.py [--batch N] [--resume]

  --batch N          run only batch N (1-5); default: all batches
  --resume           skip instances already recorded in state.json
  --skip-preflight   skip the cache-sanity guard (NOT recommended)
"""

import argparse
import json
import logging
import os
import shutil
import subprocess
import sys
import time
from datetime import datetime
from pathlib import Path

os.environ["PYTHONUTF8"] = "1"

# Runner lives in compression-testing/gate2.4/ but swe-agent + repos live in
# compression-testing/gate2/. Resolve swe-agent relative to that sibling dir.
HERE = Path(__file__).resolve().parent                       # .../gate2.4
SWE_AGENT_ROOT = HERE.parent / "gate2" / "swe-agent"          # .../gate2/swe-agent
sys.path.insert(0, str(SWE_AGENT_ROOT))

from sweagent.run.run_single import RunSingle, RunSingleConfig
from sweagent.environment.repo import PreExistingRepoConfig
import litellm
litellm.drop_params = True

# ── git-clean NTFS fix (same as gate2_swe_agent.py) ─────────────────────────
_orig_get_reset = PreExistingRepoConfig.get_reset_commands
def _patched_get_reset(self):
    cmds = _orig_get_reset(self)
    result = []
    for c in cmds:
        if c.startswith("git fetch"):
            continue
        if c.startswith("git checkout"):
            result.append("git clean -fdq")
        result.append(c)
    return result
PreExistingRepoConfig.get_reset_commands = _patched_get_reset

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    handlers=[logging.StreamHandler(sys.stdout)],
)
logger = logging.getLogger("gate2.4")

# ── Constants ────────────────────────────────────────────────────────────────
MAX_PATCH_ATTEMPTS = 10     # retries until non-empty patch per (instance, rep, arm)
N_REPS             = 3      # repetitions per instance per arm
BATCH_SIZE         = 10     # instances per batch
PROXY_HOST         = os.environ.get("PROXY_HOST", "localhost")
SWEBENCH_VENV_PY   = "/home/sdrona/swe-bench-3slot-work/python-env/swebench-wsl/bin/python3"
MODEL_NAME         = "azure__gpt-5.3-codex"

CONFIG_YAML = SWE_AGENT_ROOT / "config" / "default.yaml"
_REPOS_BASE = "/mnt/e/superbased-observer/compression-testing/gate2/repos"

REPO_LINUX_PATHS: dict[str, str] = {
    "astropy":      os.environ.get("REPO_LINUX_PATH_ASTROPY",    f"{_REPOS_BASE}/astropy"),
    "django":       os.environ.get("REPO_LINUX_PATH_DJANGO",     f"{_REPOS_BASE}/django"),
    "matplotlib":   os.environ.get("REPO_LINUX_PATH_MATPLOTLIB", f"{_REPOS_BASE}/matplotlib"),
    "seaborn":      os.environ.get("REPO_LINUX_PATH_SEABORN",    f"{_REPOS_BASE}/seaborn"),
    "xarray":       os.environ.get("REPO_LINUX_PATH_XARRAY",     f"{_REPOS_BASE}/xarray"),
    "pylint":       os.environ.get("REPO_LINUX_PATH_PYLINT",     f"{_REPOS_BASE}/pylint"),
    "pytest":       os.environ.get("REPO_LINUX_PATH_PYTEST",     f"{_REPOS_BASE}/pytest"),
    "scikit-learn": os.environ.get("REPO_LINUX_PATH_SKLEARN",    f"{_REPOS_BASE}/scikit-learn"),
    "sphinx":       os.environ.get("REPO_LINUX_PATH_SPHINX",     f"{_REPOS_BASE}/sphinx"),
    "sympy":        os.environ.get("REPO_LINUX_PATH_SYMPY",      f"{_REPOS_BASE}/sympy"),
}

# ── Instance list (same Gate 2.2 n=50 cohort) ───────────────────────────────
ALL_INSTANCES = [
    "astropy__astropy-13398",  "astropy__astropy-14369",  "astropy__astropy-8707",
    "django__django-11099",    "django__django-11400",    "django__django-11734",
    "django__django-12406",    "django__django-13195",    "django__django-13212",
    "django__django-13344",    "django__django-14315",    "django__django-14376",
    "django__django-15561",    "django__django-16256",
    "matplotlib__matplotlib-14623", "matplotlib__matplotlib-24870",
    "matplotlib__matplotlib-25479", "matplotlib__matplotlib-25775",
    "mwaskom__seaborn-3187",
    "pydata__xarray-3095",     "pydata__xarray-3305",     "pydata__xarray-3993",
    "pydata__xarray-6938",     "pydata__xarray-6992",
    "pylint-dev__pylint-4551", "pylint-dev__pylint-4604", "pylint-dev__pylint-4661",
    "pylint-dev__pylint-6386", "pylint-dev__pylint-6528", "pylint-dev__pylint-8898",
    "pytest-dev__pytest-5840", "pytest-dev__pytest-8399",
    "scikit-learn__scikit-learn-12682", "scikit-learn__scikit-learn-25102",
    "sphinx-doc__sphinx-10673", "sphinx-doc__sphinx-7462", "sphinx-doc__sphinx-7590",
    "sphinx-doc__sphinx-8120",  "sphinx-doc__sphinx-8548", "sphinx-doc__sphinx-8551",
    "sphinx-doc__sphinx-8593",  "sphinx-doc__sphinx-9461",
    "sympy__sympy-13091",  "sympy__sympy-13877",  "sympy__sympy-14248",
    "sympy__sympy-16597",  "sympy__sympy-17318",  "sympy__sympy-19783",
    "sympy__sympy-20438",  "sympy__sympy-22080",
]

BASE_COMMITS = {
    "astropy__astropy-13398":           "6500928dc0e57be8f06d1162eacc3ba5e2eff692",
    "astropy__astropy-14369":           "fa4e8d1cd279acf9b24560813c8652494ccd5922",
    "astropy__astropy-8707":            "a85a0747c54bac75e9c3b2fe436b105ea029d6cf",
    "django__django-11099":             "d26b2424437dabeeca94d7900b37d2df4410da0c",
    "django__django-11400":             "1f8382d34d54061eddc41df6994e20ee38c60907",
    "django__django-11734":             "999891bd80b3d02dd916731a7a239e1036174885",
    "django__django-12406":             "335c9c94acf263901fb023404408880245b0c4b4",
    "django__django-13195":             "156a2138db20abc89933121e4ff2ee2ce56a173a",
    "django__django-13212":             "f4e93919e4608cfc50849a1f764fd856e0917401",
    "django__django-13344":             "e39e727ded673e74016b5d3658d23cbe20234d11",
    "django__django-14315":             "187118203197801c6cb72dc8b06b714b23b6dd3d",
    "django__django-14376":             "d06c5b358149c02a62da8a5469264d05f29ac659",
    "django__django-15561":             "6991880109e35c879b71b7d9d9c154baeec12b89",
    "django__django-16256":             "76e37513e22f4d9a01c7f15eee36fe44388e6670",
    "matplotlib__matplotlib-14623":     "d65c9ca20ddf81ef91199e6d819f9d3506ef477c",
    "matplotlib__matplotlib-24870":     "6091437be9776139d3672cde28a19cbe6c09dcd5",
    "matplotlib__matplotlib-25479":     "7fdf772201e4c9bafbc16dfac23b5472d6a53fa2",
    "matplotlib__matplotlib-25775":     "26224d96066b5c60882296c551f54ca7732c0af0",
    "mwaskom__seaborn-3187":            "22cdfb0c93f8ec78492d87edb810f10cb7f57a31",
    "pydata__xarray-3095":              "1757dffac2fa493d7b9a074b84cf8c830a706688",
    "pydata__xarray-3305":              "69c7e01e5167a3137c285cb50d1978252bb8bcbf",
    "pydata__xarray-3993":              "8cc34cb412ba89ebca12fc84f76a9e452628f1bc",
    "pydata__xarray-6938":              "c4e40d991c28be51de9ac560ce895ac7f9b14924",
    "pydata__xarray-6992":              "45c0a114e2b7b27b83c9618bc05b36afac82183c",
    "pylint-dev__pylint-4551":          "99589b08de8c5a2c6cc61e13a37420a868c80599",
    "pylint-dev__pylint-4604":          "1e55ae64624d28c5fe8b63ad7979880ee2e6ef3f",
    "pylint-dev__pylint-4661":          "1d1619ef913b99b06647d2030bddff4800abdf63",
    "pylint-dev__pylint-6386":          "754b487f4d892e3d4872b6fc7468a71db4e31c13",
    "pylint-dev__pylint-6528":          "273a8b25620467c1e5686aa8d2a1dbb8c02c78d0",
    "pylint-dev__pylint-8898":          "1f8c4d9eb185c16a2c1d881c054f015e1c2eb334",
    "pytest-dev__pytest-5840":          "73c5b7f4b11a81e971f7d1bb18072e06a87060f4",
    "pytest-dev__pytest-8399":          "6e7dc8bac831cd8cf7a53b08efa366bd84f0c0fe",
    "scikit-learn__scikit-learn-12682": "d360ffa7c5896a91ae498b3fb9cf464464ce8f34",
    "scikit-learn__scikit-learn-25102": "f9a1cf072da9d7375d6c2163f68a6038b13b310f",
    "sphinx-doc__sphinx-10673":         "f35d2a6cc726f97d0e859ca7a0e1729f7da8a6c8",
    "sphinx-doc__sphinx-7462":          "b3e26a6c851133b82b50f4b68b53692076574d13",
    "sphinx-doc__sphinx-7590":          "2e506c5ab457cba743bb47eb5b8c8eb9dd51d23d",
    "sphinx-doc__sphinx-8120":          "795747bdb6b8fb7d717d5bbfc2c3316869e66a73",
    "sphinx-doc__sphinx-8548":          "dd1615c59dc6fff633e27dbb3861f2d27e1fb976",
    "sphinx-doc__sphinx-8551":          "57ed10c68057c96491acbd3e62254ccfaf9e3861",
    "sphinx-doc__sphinx-8593":          "07983a5a8704ad91ae855218ecbda1c8598200ca",
    "sphinx-doc__sphinx-9461":          "939c7bb7ff7c53a4d27df067cea637540f0e1dad",
    "sympy__sympy-13091":               "d1320814eda6549996190618a21eaf212cfd4d1e",
    "sympy__sympy-13877":               "1659712001810f5fc563a443949f8e3bb38af4bd",
    "sympy__sympy-14248":               "9986b38181cdd556a3f3411e553864f11912244e",
    "sympy__sympy-16597":               "6fd65310fa3167b9626c38a5487e171ca407d988",
    "sympy__sympy-17318":               "d4e0231b08147337745dcf601e62de7eefe2fb2d",
    "sympy__sympy-19783":               "586a43201d0357e92e8c93548d69a9f42bf548f4",
    "sympy__sympy-20438":               "33b47e4bd60e2302e42616141e76285038b724d6",
    "sympy__sympy-22080":               "3f8c8c2377cb8e0daaf8073e8d03ac7d87580813",
}

BATCHES = [ALL_INSTANCES[i:i+BATCH_SIZE] for i in range(0, len(ALL_INSTANCES), BATCH_SIZE)]
ARMS = [("off", 8841), ("on", 8842)]


# ── Helpers ──────────────────────────────────────────────────────────────────

import yaml

def repo_slug(instance_id: str) -> str:
    return instance_id.split("__")[1].rsplit("-", 1)[0]

_DATASET_CACHE: dict = {}

def load_swe_bench_instance(instance_id: str) -> dict:
    global _DATASET_CACHE
    if not _DATASET_CACHE:
        from datasets import load_dataset
        logger.info("Loading SWE-bench_Verified dataset...")
        ds = load_dataset("princeton-nlp/SWE-bench_Verified", split="test")
        for inst in ds:
            _DATASET_CACHE[inst["instance_id"]] = inst
    return _DATASET_CACHE[instance_id]

def deep_merge(base: dict, override: dict) -> dict:
    result = dict(base)
    for k, v in override.items():
        if k in result and isinstance(result[k], dict) and isinstance(v, dict):
            result[k] = deep_merge(result[k], v)
        else:
            result[k] = v
    return result

def make_run_config(instance: dict, proxy_port: int, output_dir: Path) -> RunSingleConfig:
    instance_id = instance["instance_id"]
    api_base = f"http://{PROXY_HOST}:{proxy_port}"
    base_commit = BASE_COMMITS[instance_id]
    slug = repo_slug(instance_id)
    repo_linux = REPO_LINUX_PATHS[slug]
    repo_name_for_cd = repo_linux.lstrip("/")

    with open(CONFIG_YAML) as f:
        base_cfg = yaml.safe_load(f)

    override = {
        "agent": {
            "model": {
                "name": "azure/gpt-5.3-codex",
                "api_base": api_base,
                "api_version": "2025-04-01-preview",
                "per_instance_cost_limit": 15.0,
                "max_output_tokens": 32768,
                "completion_kwargs": {"max_tokens": 32768},
            }
        },
        "env": {
            "deployment": {"type": "local"},
            "repo": {
                "type": "preexisting",
                "repo_name": repo_name_for_cd,
                "base_commit": base_commit,
                "reset": True,
            },
        },
        "problem_statement": {
            "type": "text",
            "text": instance["problem_statement"],
            "id": instance_id,
        },
        "output_dir": str(output_dir),
        "actions": {"apply_patch_locally": False},
    }

    merged = deep_merge(base_cfg, override)
    return RunSingleConfig.model_validate(merged)


def run_one_attempt(instance: dict, proxy_port: int, output_dir: Path) -> str:
    """Run SWE-agent once; return patch string (empty string on failure)."""
    instance_id = instance["instance_id"]

    tools_dir = Path("/root/tools")
    if tools_dir.exists():
        shutil.rmtree(str(tools_dir))

    cfg = make_run_config(instance, proxy_port, output_dir)
    runner = RunSingle.from_config(cfg)
    runner.run()

    patch_file = output_dir / instance_id / f"{instance_id}.patch"
    if patch_file.exists():
        patch = patch_file.read_text(encoding="utf-8").strip()
        return patch
    return ""


def run_with_retry(
    instance: dict,
    proxy_port: int,
    arm: str,
    output_dir: Path,
    max_attempts: int = MAX_PATCH_ATTEMPTS,
) -> dict:
    """
    Retry until agent produces a non-empty patch or max_attempts exhausted.
    Returns dict with patch, attempts_used, success flag.
    Each retry overwrites the instance output dir so we don't accumulate stale artefacts.
    """
    instance_id = instance["instance_id"]
    api_base = f"http://{PROXY_HOST}:{proxy_port}"
    logger.info("─" * 60)
    logger.info(f"  {instance_id} [{arm.upper()}]  proxy={api_base}")

    os.environ["AZURE_OPENAI_ENDPOINT"] = api_base
    os.environ["AZURE_OPENAI_API_VERSION"] = "2025-04-01-preview"

    for attempt in range(1, max_attempts + 1):
        inst_dir = output_dir / instance_id
        if inst_dir.exists() and attempt > 1:
            shutil.rmtree(str(inst_dir))  # clear stale artefacts before retry

        logger.info(f"  attempt {attempt}/{max_attempts}")
        try:
            patch = run_one_attempt(instance, proxy_port, output_dir)
        except Exception as exc:
            logger.warning(f"  attempt {attempt} raised: {exc}")
            patch = ""

        if patch:
            logger.info(f"  ✓ patch produced ({len(patch)} bytes) after {attempt} attempt(s)")
            return {"instance_id": instance_id, "arm": arm, "patch": patch,
                    "attempts": attempt, "success": True}

        logger.warning(f"  attempt {attempt}: empty patch")

    logger.error(f"  ✗ {instance_id} [{arm}] exhausted {max_attempts} attempts — empty patch")
    return {"instance_id": instance_id, "arm": arm, "patch": "",
            "attempts": max_attempts, "success": False}


# ── Pre-flight cache-sanity guard ────────────────────────────────────────────

def preflight_cache_guard(api_key: str, skip: bool = False) -> None:
    """
    Before any batches, prove that prompt caching is active end-to-end through
    BOTH proxies. Send two identical >1024-token warmup completions per arm; the
    second must report cached_tokens > 0. If not, abort — Gate 2.3 silently lost
    cache and we will not repeat that. This is litellm-side (client) verification;
    the proxy-DB capture is verified separately by the operator before launch.
    """
    if skip:
        logger.warning("Pre-flight cache guard SKIPPED (--skip-preflight)")
        return

    filler = " ".join(["The quick brown fox jumps over the lazy dog."] * 400)
    msgs = [
        {"role": "system", "content": "You are a helpful coding assistant. " + filler},
        {"role": "user", "content": "Reply with the single word OK.\n" + filler},
    ]

    logger.info("=" * 60)
    logger.info("PRE-FLIGHT CACHE GUARD")
    for arm, port in ARMS:
        api_base = f"http://{PROXY_HOST}:{port}"
        os.environ["AZURE_OPENAI_ENDPOINT"] = api_base
        os.environ["AZURE_OPENAI_API_VERSION"] = "2025-04-01-preview"
        cached = 0
        for i in range(2):
            r = litellm.completion(
                model="azure/gpt-5.3-codex", messages=msgs, max_tokens=16,
                temperature=0, api_base=api_base,
                api_version="2025-04-01-preview", api_key=api_key,
            )
            u = r.usage
            ptd = getattr(u, "prompt_tokens_details", None)
            if isinstance(ptd, dict):
                cached = ptd.get("cached_tokens", 0) or 0
            elif ptd is not None:
                cached = getattr(ptd, "cached_tokens", 0) or 0
            else:
                cached = 0
            logger.info(f"  {arm.upper()} warmup {i+1}/2  prompt={u.prompt_tokens} cached={cached}")
            time.sleep(2)
        if cached <= 0:
            raise SystemExit(
                f"PRE-FLIGHT FAIL: arm {arm} reported cached_tokens=0 after warmup. "
                f"Caching is not active on port {port}. Aborting Gate 2.4."
            )
    logger.info("  PRE-FLIGHT CACHE GUARD PASSED — caching active on both arms")
    logger.info("=" * 60)


# ── Harness ──────────────────────────────────────────────────────────────────

def build_predictions_jsonl(results: list[dict], out_path: Path) -> int:
    """Write swebench-format predictions; return count with non-empty patches."""
    n = 0
    with open(out_path, "w", encoding="utf-8") as fh:
        for r in results:
            patch = r.get("patch", "")
            fh.write(json.dumps({
                "instance_id": r["instance_id"],
                "model_patch": patch,
                "model_name_or_path": MODEL_NAME,
            }) + "\n")
            if patch:
                n += 1
    return n


def run_harness_wsl(predictions_path: Path, run_id: str, max_workers: int = 4) -> dict | None:
    """
    Invoke swebench.harness.run_evaluation (Docker required).
    Detects if we're already inside WSL/Linux and calls directly;
    otherwise falls back to wsl -u root -e (Windows host invocation).
    Returns the parsed report dict or None on failure.
    """
    import platform
    inside_wsl = platform.system() == "Linux"

    if inside_wsl:
        # Already in WSL — call python directly (runner is root, docker is available)
        cmd = [
            sys.executable, "-m", "swebench.harness.run_evaluation",
            "-p", str(predictions_path),
            "-d", "princeton-nlp/SWE-bench_Verified",
            "-s", "test",
            "--max_workers", str(max_workers),
            "--report_dir", str(predictions_path.parent),
            "-id", run_id,
        ]
    else:
        # Running on Windows — route through WSL
        wsl_pred = "/mnt/" + str(predictions_path).replace("\\", "/").replace(":", "").lower()
        wsl_report_dir = "/mnt/" + str(predictions_path.parent).replace("\\", "/").replace(":", "").lower()
        cmd = [
            "wsl", "-u", "root", "-e",
            SWEBENCH_VENV_PY, "-m", "swebench.harness.run_evaluation",
            "-p", wsl_pred,
            "-d", "princeton-nlp/SWE-bench_Verified",
            "-s", "test",
            "--max_workers", str(max_workers),
            "--report_dir", wsl_report_dir,
            "-id", run_id,
        ]

    logger.info(f"  Running harness: run_id={run_id}")
    result = subprocess.run(cmd, capture_output=False)
    if result.returncode != 0:
        logger.warning(f"  Harness returned exit {result.returncode}")

    # Report lands in predictions_path.parent (we passed --report_dir there)
    report_name = f"{MODEL_NAME}.{run_id}.json"
    for candidate in [predictions_path.parent / report_name, Path(".") / report_name]:
        if candidate.exists():
            with candidate.open() as fh:
                return json.load(fh)

    logger.warning(f"  Harness report not found: {report_name}")
    return None


# ── State checkpoint ─────────────────────────────────────────────────────────

def load_state(state_path: Path) -> dict:
    if state_path.exists():
        with open(state_path) as fh:
            return json.load(fh)
    return {"runs": {}, "harness": {}}


def save_state(state: dict, state_path: Path) -> None:
    with open(state_path, "w") as fh:
        json.dump(state, fh, indent=2, default=str)


def state_key(batch_idx: int, rep: int, arm: str, instance_id: str) -> str:
    return f"b{batch_idx+1:02d}_r{rep}_{arm}_{instance_id}"


# ── Main ──────────────────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--batch", type=int, default=None,
                        help="Run only batch N (1-5); default: all")
    parser.add_argument("--resume", action="store_true",
                        help="Skip instances already in state.json")
    parser.add_argument("--harness-workers", type=int, default=4)
    parser.add_argument("--skip-preflight", action="store_true",
                        help="Skip the cache-sanity guard (NOT recommended)")
    args = parser.parse_args()

    api_key = (os.environ.get("AZURE_OPENAI_API_KEY") or
               os.environ.get("OPENAI_API_KEY", "dummy"))
    os.environ["AZURE_OPENAI_API_KEY"] = api_key
    os.environ["OPENAI_API_KEY"] = api_key

    # Resume support: GATE24_RESUME_DIR pins the output dir across invocations.
    resume_root_env = os.environ.get("GATE24_RESUME_DIR")
    if args.resume and resume_root_env:
        output_root = Path(resume_root_env)
        run_id = output_root.name.replace("gate2_4_swe_", "")
        state_path = output_root / "state.json"
        output_root.mkdir(parents=True, exist_ok=True)
        logger.info(f"Resuming run at {output_root}")
    else:
        run_id = datetime.now().strftime("%Y%m%d_%H%M%S")
        output_root = HERE / "runs" / f"gate2_4_swe_{run_id}"
        output_root.mkdir(parents=True, exist_ok=True)
        state_path = output_root / "state.json"
        logger.info(f"New Gate 2.4 run: {output_root}")

    # ── Pre-flight cache-sanity guard (abort if caching not active) ──────────
    preflight_cache_guard(api_key, skip=args.skip_preflight)

    state = load_state(state_path)

    batch_indices = (
        [args.batch - 1] if args.batch
        else list(range(len(BATCHES)))
    )

    grand_results: list[dict] = []

    for batch_idx in batch_indices:
        batch_instances = BATCHES[batch_idx]
        logger.info(f"\n{'#'*70}")
        logger.info(f"# BATCH {batch_idx+1}/5  instances {batch_idx*BATCH_SIZE+1}–{batch_idx*BATCH_SIZE+len(batch_instances)}")
        logger.info(f"{'#'*70}")

        # ── Load dataset entries for this batch ─────────────────────────────
        inst_data = [load_swe_bench_instance(iid) for iid in batch_instances]

        # ── Agent runs: 3 reps × 2 arms × 10 instances ──────────────────────
        batch_run_results: dict[str, dict] = {}  # key → result dict

        for rep in range(1, N_REPS + 1):
            for arm, port in ARMS:
                arm_output = output_root / f"rep_{rep}" / arm
                arm_output.mkdir(parents=True, exist_ok=True)

                logger.info(f"\n── Rep {rep}/{N_REPS}  {arm.upper()} arm  batch {batch_idx+1} ──")

                for inst in inst_data:
                    iid = inst["instance_id"]
                    key = state_key(batch_idx, rep, arm, iid)

                    if args.resume and key in state["runs"]:
                        logger.info(f"  SKIP (resume): {key}")
                        batch_run_results[key] = state["runs"][key]
                        grand_results.append(state["runs"][key])
                        continue

                    result = run_with_retry(inst, port, arm, arm_output)
                    result["rep"] = rep
                    result["batch"] = batch_idx + 1
                    batch_run_results[key] = result
                    grand_results.append(result)

                    state["runs"][key] = result
                    save_state(state, state_path)

        # ── Build predictions + run harness per rep per arm ─────────────────
        harness_dir = output_root / "harness" / f"batch_{batch_idx+1:02d}"
        harness_dir.mkdir(parents=True, exist_ok=True)
        batch_harness_results: dict = {}

        for rep in range(1, N_REPS + 1):
            for arm, _ in ARMS:
                harness_key = f"b{batch_idx+1:02d}_r{rep}_{arm}"
                if args.resume and harness_key in state["harness"]:
                    logger.info(f"  SKIP harness (resume): {harness_key}")
                    batch_harness_results[harness_key] = state["harness"][harness_key]
                    continue

                # Collect results for this rep+arm, this batch
                rep_arm_results = [
                    batch_run_results[state_key(batch_idx, rep, arm, iid)]
                    for iid in batch_instances
                ]

                preds_path = harness_dir / f"rep_{rep}_{arm}_predictions.jsonl"
                n_patched = build_predictions_jsonl(rep_arm_results, preds_path)
                logger.info(f"  Predictions: {n_patched}/{len(batch_instances)} patches → {preds_path.name}")

                harness_run_id = f"gate2-4-b{batch_idx+1:02d}-r{rep}-{arm}"
                if n_patched == 0:
                    logger.warning(f"  Skipping harness (0 patches): {harness_run_id}")
                    batch_harness_results[harness_key] = {"run_id": harness_run_id, "skipped": True}
                else:
                    report = run_harness_wsl(preds_path, harness_run_id, args.harness_workers)
                    if report:
                        n_res = report.get("resolved_instances", 0)
                        logger.info(f"  Harness {harness_run_id}: {n_res}/{len(batch_instances)} resolved")
                        # Move report to harness_dir for cleanliness
                        report_src = Path(".") / f"{MODEL_NAME}.{harness_run_id}.json"
                        if report_src.exists():
                            report_src.rename(harness_dir / report_src.name)
                        batch_harness_results[harness_key] = report
                    else:
                        batch_harness_results[harness_key] = {"run_id": harness_run_id, "error": True}

                state["harness"][harness_key] = batch_harness_results[harness_key]
                save_state(state, state_path)

        # ── Batch summary ────────────────────────────────────────────────────
        logger.info(f"\n── Batch {batch_idx+1} summary ──")
        for arm, _ in ARMS:
            for rep in range(1, N_REPS + 1):
                hk = f"b{batch_idx+1:02d}_r{rep}_{arm}"
                hr = batch_harness_results.get(hk, {})
                n_res = hr.get("resolved_instances", "?")
                logger.info(f"  rep={rep} arm={arm}  resolved={n_res}/{len(batch_instances)}")

    # ── Final grand summary ──────────────────────────────────────────────────
    logger.info("\n" + "=" * 70)
    logger.info("GATE 2.4 COMPLETE")
    logger.info("=" * 70)

    empty = sum(1 for r in grand_results if not r.get("patch"))
    multi_attempt = sum(1 for r in grand_results if r.get("attempts", 1) > 1)
    logger.info(f"  Total runs:           {len(grand_results)}")
    logger.info(f"  Empty-patch runs:     {empty}")
    logger.info(f"  Multi-attempt runs:   {multi_attempt}")

    summary_path = output_root / "grand_summary.json"
    summary_path.write_text(
        json.dumps({
            "run_id": run_id,
            "total_runs": len(grand_results),
            "empty_patch": empty,
            "multi_attempt": multi_attempt,
            "results": grand_results,
        }, indent=2, default=str),
        encoding="utf-8",
    )
    logger.info(f"  Grand summary: {summary_path}")


if __name__ == "__main__":
    main()
