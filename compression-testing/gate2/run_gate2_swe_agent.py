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

PILOT_INSTANCES = [
    # Re-run: 4 django instances only (git checkout fix)
    "django__django-10097",
    "django__django-10554",
    "django__django-10880",
    "django__django-10914",
]

BASE_COMMITS = {
    "astropy__astropy-12907": "d16bfe05a744909de4b27f5875fe0d4ed41ce607",
    "astropy__astropy-13033": "298ccb478e6bf092953bca67a3d29dc6c35f6752",
    "astropy__astropy-13236": "6ed769d58d89380ebaa1ef52b300691eefda8928",
    "astropy__astropy-13398": "6500928dc0e57be8f06d1162eacc3ba5e2eff692",
    "astropy__astropy-13453": "19cc80471739bcb67b7e8099246b391c355023ee",
    "astropy__astropy-13579": "0df94ff7097961e92fd7812036a24b145bc13ca8",
    "astropy__astropy-13977": "5250b2442501e6c671c6b380536f1edb352602d1",
    "astropy__astropy-14096": "1a4462d72eb03f30dc83a879b1dd57aac8b2c18b",
    "astropy__astropy-14182": "a5917978be39d13cd90b517e1de4e7a539ffaa48",
    "astropy__astropy-14309": "cdb66059a2feb44ee49021874605ba90801f9986",
    "astropy__astropy-14369": "fa4e8d1cd279acf9b24560813c8652494ccd5922",
    "astropy__astropy-14508": "a3f4ae6cd24d5ecdf49f213d77b3513dd509a06c",
    "astropy__astropy-14539": "c0a24c1dc957a3b565294213f435fefb2ec99714",
    "astropy__astropy-14598": "80c3854a5f4f4a6ab86c03d9db7854767fcd83c1",
    "astropy__astropy-14995": "b16c7d12ccbc7b2d20364b89fb44285bcbfede54",
    "astropy__astropy-7166":  "26d147868f8a891a6009a25cd6a8576d2e1bd747",
    "astropy__astropy-7336":  "732d89c2940156bdc0e200bb36dc38b5e424bcba",
    "astropy__astropy-7606":  "3cedd79e6c121910220f8e6df77c54a0b344ea94",
    "astropy__astropy-7671":  "a7141cd90019b62688d507ae056298507678c058",
    "astropy__astropy-8707":  "a85a0747c54bac75e9c3b2fe436b105ea029d6cf",
    "astropy__astropy-8872":  "b750a0e6ee76fb6b8a099a4d16ec51977be46bf6",
    "django__django-10097": "b9cf764be62e77b4777b3a75ec256f6209a57671",
    "django__django-10554": "14d026cccb144c6877294ba4cd4e03ebf0842498",
    "django__django-10880": "838e432e3e5519c5383d12018e6c78f8ec7833c1",
    "django__django-10914": "e7fd69d051eaa67cb17f172a39b57253e9cb831a",
    # n=50 additions (django instances 5-29)
    "django__django-10973": "ddb293685235fd09e932805771ae97f72e817181",
    "django__django-10999": "36300ef336e3f130a0dadc1143163ff3d23dc843",
    "django__django-11066": "4b45b6c8e4d7c9701a332e80d3b1c84209dc36e2",
    "django__django-11087": "8180ffba21bf10f4be905cb0d4890dc2bcff2788",
    "django__django-11095": "7d49ad76562e8c0597a0eb66046ab423b12888d8",
    "django__django-11099": "d26b2424437dabeeca94d7900b37d2df4410da0c",
    "django__django-11119": "d4df5e1b0b1c643fe0fc521add0236764ec8e92a",
    "django__django-11133": "879cc3da6249e920b8d54518a0ae06de835d7373",
    "django__django-11138": "c84b91b7603e488f7171fdff8f08368ef3d6b856",
    "django__django-11141": "5d9cf79baf07fc4aed7ad1b06990532a65378155",
    "django__django-11149": "e245046bb6e8b32360aa48b8a41fb7050f0fc730",
    "django__django-11163": "e6588aa4e793b7f56f4cadbfa155b581e0efc59a",
    "django__django-11179": "19fc6376ce67d01ca37a91ef2f55ef769f50513a",
    "django__django-11206": "571ab44e8a8936014c22e7eebe4948d9611fd7ce",
    "django__django-11211": "ba726067604ce5a8ca3919edf653496722b433ab",
    "django__django-11239": "d87bd29c4f8dfcdf3f4a4eb8340e6770a2416fe3",
    "django__django-11265": "21aa2a5e785eef1f47beb1c3760fdd7d8915ae09",
    "django__django-11276": "28d5262fa3315690395f04e3619ed554dbaf725b",
    "django__django-11292": "eb16c7260e573ec513d84cb586d96bdf508f3173",
    "django__django-11299": "6866c91b638de5368c18713fa851bfe56253ea55",
    "django__django-11333": "55b68de643b5c2d5f0a8ea7587ab3b2966021ccc",
    "django__django-11400": "1f8382d34d54061eddc41df6994e20ee38c60907",
    "django__django-11433": "21b1d239125f1228e579b1ce8d94d4d5feadd2a6",
    "django__django-11451": "e065b293878b1e3ea56655aa9d33e87576cd77ff",
    "django__django-11477": "e28671187903e6aca2428374fdd504fca3032aee",
}

# Default repo paths — overridden by REPO_LINUX_PATHS_<SLUG> env vars from launch script.
_REPOS_DIR = Path(__file__).parent / "repos"
REPO_LINUX_PATHS: dict[str, str] = {
    "astropy": os.environ.get("REPO_LINUX_PATH_ASTROPY",
                              "/mnt/e/superbased-observer/compression-testing/gate2/repos/astropy"),
    "django":  os.environ.get("REPO_LINUX_PATH_DJANGO",
                              "/mnt/e/superbased-observer/compression-testing/gate2/repos/django"),
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
