"""
Gate 2 SWE-agent runner — ON/OFF compression arms.

Uses RunSingleConfig.model_validate() to merge default.yaml with arm-specific
overrides. SWE-agent routes azure/gpt-5.3-codex via litellm.completion() to
the Observer proxy at api_base.

Multi-repo: REPO_LINUX_PATHS maps repo-slug → WSL2 Linux path. Each instance's
slug is derived from its instance_id prefix (e.g. "astropy__astropy" → "astropy").
"""
import json
import logging
import os
import sys
import yaml
from datetime import datetime
from pathlib import Path

os.environ["PYTHONUTF8"] = "1"

SWE_AGENT_ROOT = Path(__file__).parent / "swe-agent"
sys.path.insert(0, str(SWE_AGENT_ROOT))

from sweagent.run.run_single import RunSingle, RunSingleConfig
from sweagent.environment.repo import PreExistingRepoConfig
import litellm
litellm.drop_params = True  # azure/gpt-5.3-codex rejects unsupported params like top_p

# Patch out `git fetch` — hangs on Windows-mounted repos via pexpect.
_orig_get_reset = PreExistingRepoConfig.get_reset_commands
def _patched_get_reset(self):
    cmds = _orig_get_reset(self)
    result = []
    for c in cmds:
        if c.startswith("git fetch"):
            continue
        # Insert git clean -fdxq before git checkout so untracked files
        # at current HEAD don't block the checkout to an older base commit.
        if c.startswith("git checkout"):
            result.append("git clean -fdxq")
        result.append(c)
    return result
PreExistingRepoConfig.get_reset_commands = _patched_get_reset

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    handlers=[logging.StreamHandler(sys.stdout)],
)
logger = logging.getLogger("gate2-swe-agent")

PROXY_HOST = os.environ.get("PROXY_HOST", "localhost")

# ---------------------------------------------------------------------------
# Instance list — edit PILOT_INSTANCES + BASE_COMMITS to change the run set.
# REPO_LINUX_PATHS: repo-slug (prefix before first "__") → WSL2 absolute path.
# ---------------------------------------------------------------------------

# Gate 2.2 — balanced n=50 cohort (gate2_2_subset_balanced_n50.txt)
# seed=20260622, 10 repos, 98% multi-file
PILOT_INSTANCES = [
    "astropy__astropy-13398",
    "astropy__astropy-14369",
    "astropy__astropy-8707",
    "django__django-11099",
    "django__django-11400",
    "django__django-11734",
    "django__django-12406",
    "django__django-13195",
    "django__django-13212",
    "django__django-13344",
    "django__django-14315",
    "django__django-14376",
    "django__django-15561",
    "django__django-16256",
    "matplotlib__matplotlib-14623",
    "matplotlib__matplotlib-24870",
    "matplotlib__matplotlib-25479",
    "matplotlib__matplotlib-25775",
    "mwaskom__seaborn-3187",
    "pydata__xarray-3095",
    "pydata__xarray-3305",
    "pydata__xarray-3993",
    "pydata__xarray-6938",
    "pydata__xarray-6992",
    "pylint-dev__pylint-4551",
    "pylint-dev__pylint-4604",
    "pylint-dev__pylint-4661",
    "pylint-dev__pylint-6386",
    "pylint-dev__pylint-6528",
    "pylint-dev__pylint-8898",
    "pytest-dev__pytest-5840",
    "pytest-dev__pytest-8399",
    "scikit-learn__scikit-learn-12682",
    "scikit-learn__scikit-learn-25102",
    "sphinx-doc__sphinx-10673",
    "sphinx-doc__sphinx-7462",
    "sphinx-doc__sphinx-7590",
    "sphinx-doc__sphinx-8120",
    "sphinx-doc__sphinx-8548",
    "sphinx-doc__sphinx-8551",
    "sphinx-doc__sphinx-8593",
    "sphinx-doc__sphinx-9461",
    "sympy__sympy-13091",
    "sympy__sympy-13877",
    "sympy__sympy-14248",
    "sympy__sympy-16597",
    "sympy__sympy-17318",
    "sympy__sympy-19783",
    "sympy__sympy-20438",
    "sympy__sympy-22080",
]

BASE_COMMITS = {
    "astropy__astropy-13398": "6500928dc0e57be8f06d1162eacc3ba5e2eff692",
    "astropy__astropy-14369": "fa4e8d1cd279acf9b24560813c8652494ccd5922",
    "astropy__astropy-8707": "a85a0747c54bac75e9c3b2fe436b105ea029d6cf",
    "django__django-11099": "d26b2424437dabeeca94d7900b37d2df4410da0c",
    "django__django-11400": "1f8382d34d54061eddc41df6994e20ee38c60907",
    "django__django-11734": "999891bd80b3d02dd916731a7a239e1036174885",
    "django__django-12406": "335c9c94acf263901fb023404408880245b0c4b4",
    "django__django-13195": "156a2138db20abc89933121e4ff2ee2ce56a173a",
    "django__django-13212": "f4e93919e4608cfc50849a1f764fd856e0917401",
    "django__django-13344": "e39e727ded673e74016b5d3658d23cbe20234d11",
    "django__django-14315": "187118203197801c6cb72dc8b06b714b23b6dd3d",
    "django__django-14376": "d06c5b358149c02a62da8a5469264d05f29ac659",
    "django__django-15561": "6991880109e35c879b71b7d9d9c154baeec12b89",
    "django__django-16256": "76e37513e22f4d9a01c7f15eee36fe44388e6670",
    "matplotlib__matplotlib-14623": "d65c9ca20ddf81ef91199e6d819f9d3506ef477c",
    "matplotlib__matplotlib-24870": "6091437be9776139d3672cde28a19cbe6c09dcd5",
    "matplotlib__matplotlib-25479": "7fdf772201e4c9bafbc16dfac23b5472d6a53fa2",
    "matplotlib__matplotlib-25775": "26224d96066b5c60882296c551f54ca7732c0af0",
    "mwaskom__seaborn-3187": "22cdfb0c93f8ec78492d87edb810f10cb7f57a31",
    "pydata__xarray-3095": "1757dffac2fa493d7b9a074b84cf8c830a706688",
    "pydata__xarray-3305": "69c7e01e5167a3137c285cb50d1978252bb8bcbf",
    "pydata__xarray-3993": "8cc34cb412ba89ebca12fc84f76a9e452628f1bc",
    "pydata__xarray-6938": "c4e40d991c28be51de9ac560ce895ac7f9b14924",
    "pydata__xarray-6992": "45c0a114e2b7b27b83c9618bc05b36afac82183c",
    "pylint-dev__pylint-4551": "99589b08de8c5a2c6cc61e13a37420a868c80599",
    "pylint-dev__pylint-4604": "1e55ae64624d28c5fe8b63ad7979880ee2e6ef3f",
    "pylint-dev__pylint-4661": "1d1619ef913b99b06647d2030bddff4800abdf63",
    "pylint-dev__pylint-6386": "754b487f4d892e3d4872b6fc7468a71db4e31c13",
    "pylint-dev__pylint-6528": "273a8b25620467c1e5686aa8d2a1dbb8c02c78d0",
    "pylint-dev__pylint-8898": "1f8c4d9eb185c16a2c1d881c054f015e1c2eb334",
    "pytest-dev__pytest-5840": "73c5b7f4b11a81e971f7d1bb18072e06a87060f4",
    "pytest-dev__pytest-8399": "6e7dc8bac831cd8cf7a53b08efa366bd84f0c0fe",
    "scikit-learn__scikit-learn-12682": "d360ffa7c5896a91ae498b3fb9cf464464ce8f34",
    "scikit-learn__scikit-learn-25102": "f9a1cf072da9d7375d6c2163f68a6038b13b310f",
    "sphinx-doc__sphinx-10673": "f35d2a6cc726f97d0e859ca7a0e1729f7da8a6c8",
    "sphinx-doc__sphinx-7462": "b3e26a6c851133b82b50f4b68b53692076574d13",
    "sphinx-doc__sphinx-7590": "2e506c5ab457cba743bb47eb5b8c8eb9dd51d23d",
    "sphinx-doc__sphinx-8120": "795747bdb6b8fb7d717d5bbfc2c3316869e66a73",
    "sphinx-doc__sphinx-8548": "dd1615c59dc6fff633e27dbb3861f2d27e1fb976",
    "sphinx-doc__sphinx-8551": "57ed10c68057c96491acbd3e62254ccfaf9e3861",
    "sphinx-doc__sphinx-8593": "07983a5a8704ad91ae855218ecbda1c8598200ca",
    "sphinx-doc__sphinx-9461": "939c7bb7ff7c53a4d27df067cea637540f0e1dad",
    "sympy__sympy-13091": "d1320814eda6549996190618a21eaf212cfd4d1e",
    "sympy__sympy-13877": "1659712001810f5fc563a443949f8e3bb38af4bd",
    "sympy__sympy-14248": "9986b38181cdd556a3f3411e553864f11912244e",
    "sympy__sympy-16597": "6fd65310fa3167b9626c38a5487e171ca407d988",
    "sympy__sympy-17318": "d4e0231b08147337745dcf601e62de7eefe2fb2d",
    "sympy__sympy-19783": "586a43201d0357e92e8c93548d69a9f42bf548f4",
    "sympy__sympy-20438": "33b47e4bd60e2302e42616141e76285038b724d6",
    "sympy__sympy-22080": "3f8c8c2377cb8e0daaf8073e8d03ac7d87580813",
}

# Repo paths — all 10 repos for Gate 2.2. Overridable via env vars from launch script.
_REPOS_BASE = "/mnt/e/superbased-observer/compression-testing/gate2/repos"
REPO_LINUX_PATHS: dict[str, str] = {
    "astropy":       os.environ.get("REPO_LINUX_PATH_ASTROPY",      f"{_REPOS_BASE}/astropy"),
    "django":        os.environ.get("REPO_LINUX_PATH_DJANGO",       f"{_REPOS_BASE}/django"),
    "matplotlib":    os.environ.get("REPO_LINUX_PATH_MATPLOTLIB",   f"{_REPOS_BASE}/matplotlib"),
    "seaborn":       os.environ.get("REPO_LINUX_PATH_SEABORN",      f"{_REPOS_BASE}/seaborn"),
    "xarray":        os.environ.get("REPO_LINUX_PATH_XARRAY",       f"{_REPOS_BASE}/xarray"),
    "pylint":        os.environ.get("REPO_LINUX_PATH_PYLINT",       f"{_REPOS_BASE}/pylint"),
    "pytest":        os.environ.get("REPO_LINUX_PATH_PYTEST",       f"{_REPOS_BASE}/pytest"),
    "scikit-learn":  os.environ.get("REPO_LINUX_PATH_SKLEARN",      f"{_REPOS_BASE}/scikit-learn"),
    "sphinx":        os.environ.get("REPO_LINUX_PATH_SPHINX",       f"{_REPOS_BASE}/sphinx"),
    "sympy":         os.environ.get("REPO_LINUX_PATH_SYMPY",        f"{_REPOS_BASE}/sympy"),
}

CONFIG_YAML = SWE_AGENT_ROOT / "config" / "default.yaml"


def repo_slug(instance_id: str) -> str:
    """Return the short repo key for an instance ('astropy', 'django', …)."""
    # instance_id format: "<org>__<repo>-<number>"
    return instance_id.split("__")[1].rsplit("-", 1)[0]

_DATASET_CACHE: dict = {}


def deep_merge(base: dict, override: dict) -> dict:
    result = dict(base)
    for k, v in override.items():
        if k in result and isinstance(result[k], dict) and isinstance(v, dict):
            result[k] = deep_merge(result[k], v)
        else:
            result[k] = v
    return result


def load_swe_bench_instance(instance_id: str) -> dict:
    global _DATASET_CACHE
    if not _DATASET_CACHE:
        from datasets import load_dataset
        logger.info("Loading SWE-bench_Verified dataset...")
        ds = load_dataset("princeton-nlp/SWE-bench_Verified", split="test")
        for inst in ds:
            _DATASET_CACHE[inst["instance_id"]] = inst
    return _DATASET_CACHE[instance_id]


def make_run_config(
    instance: dict,
    proxy_port: int,
    arm_output: Path,
) -> RunSingleConfig:
    instance_id = instance["instance_id"]
    api_base = f"http://{PROXY_HOST}:{proxy_port}"
    base_commit = BASE_COMMITS[instance_id]

    # Resolve per-repo Linux path for WSL2.
    # SWE-agent PreExistingRepoConfig does `cd /{repo_name}`, so we strip the
    # leading slash and let it prepend one back.
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
        "output_dir": str(arm_output),
        "actions": {"apply_patch_locally": False},
    }

    merged = deep_merge(base_cfg, override)
    return RunSingleConfig.model_validate(merged)


def run_instance(instance: dict, proxy_port: int, arm: str, arm_output: Path) -> str:
    import shutil
    instance_id = instance["instance_id"]
    api_base = f"http://{PROXY_HOST}:{proxy_port}"
    slug = repo_slug(instance_id)
    logger.info("=" * 60)
    logger.info(f"  {instance_id} [{slug}] — {arm.upper()} arm  proxy={api_base}")
    logger.info("=" * 60)

    os.environ["AZURE_OPENAI_ENDPOINT"] = api_base
    os.environ["AZURE_OPENAI_API_VERSION"] = "2025-04-01-preview"

    # Remove /root/tools from prior instance — shutil.copytree fails if it exists
    tools_dir = Path("/root/tools")
    if tools_dir.exists():
        shutil.rmtree(str(tools_dir))
        logger.info("Cleared /root/tools from prior instance")

    cfg = make_run_config(instance, proxy_port, arm_output)
    runner = RunSingle.from_config(cfg)
    runner.run()

    patch_file = arm_output / instance_id / f"{instance_id}.patch"
    if patch_file.exists():
        patch = patch_file.read_text(encoding="utf-8")
        logger.info(f"Patch written: {len(patch)} bytes")
        return patch
    logger.warning(f"No patch at {patch_file}")
    return ""


def build_predictions_jsonl(results: list, arm: str, out_path: Path) -> int:
    n = 0
    with open(out_path, "w", encoding="utf-8") as f:
        for r in results:
            if r.get("patch"):
                f.write(json.dumps({
                    "instance_id": r["instance_id"],
                    "model_patch": r["patch"],
                    "model_name_or_path": f"gate2-swe-{arm}",
                }) + "\n")
                n += 1
    return n


def main():
    api_key = os.environ.get("AZURE_OPENAI_API_KEY") or \
              os.environ.get("OPENAI_API_KEY", "dummy")
    os.environ["AZURE_OPENAI_API_KEY"] = api_key
    os.environ["OPENAI_API_KEY"] = api_key

    run_id = datetime.now().strftime("%Y%m%d_%H%M%S")
    output_root = Path(__file__).parent / "runs" / f"gate2_swe_{run_id}"
    output_root.mkdir(parents=True, exist_ok=True)
    logger.info(f"Output dir: {output_root}")

    instances = [load_swe_bench_instance(iid) for iid in PILOT_INSTANCES]
    all_results = []

    for arm, port in [("off", 8831), ("on", 8832)]:
        arm_output = output_root / arm
        arm_output.mkdir(exist_ok=True)
        logger.info(f"\n{'#'*60}\n# {arm.upper()} ARM  (port {port})\n{'#'*60}")

        arm_results = []
        for inst in instances:
            try:
                patch = run_instance(inst, port, arm, arm_output)
                arm_results.append({
                    "instance_id": inst["instance_id"],
                    "arm": arm,
                    "patch": patch,
                    "success": True,
                })
            except Exception as e:
                logger.error(f"FAILED {inst['instance_id']}: {e}", exc_info=True)
                arm_results.append({
                    "instance_id": inst["instance_id"],
                    "arm": arm,
                    "patch": "",
                    "success": False,
                    "error": str(e),
                })

        preds_path = arm_output / "predictions.jsonl"
        n = build_predictions_jsonl(arm_results, arm, preds_path)
        logger.info(f"{arm.upper()}: {n}/{len(instances)} patches → {preds_path}")
        all_results.extend(arm_results)

    logger.info("\n" + "=" * 60)
    logger.info("GATE 2 SWE-AGENT SUMMARY")
    logger.info("=" * 60)
    for r in all_results:
        logger.info(f"  {r['instance_id']}  [{r['arm']}]  {'patch' if r['patch'] else 'empty'}")

    (output_root / "summary.json").write_text(
        json.dumps(all_results, indent=2, default=str), encoding="utf-8"
    )
    logger.info(f"Full results: {output_root}")


if __name__ == "__main__":
    main()
