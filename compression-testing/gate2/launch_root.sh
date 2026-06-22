#!/bin/bash
set -e
cd /tmp
KEY_FILE=/mnt/e/superbased-observer/compression-testing/gate2/.gate2_key
export AZURE_OPENAI_API_KEY=$(cat $KEY_FILE)
export OPENAI_API_KEY=$AZURE_OPENAI_API_KEY
export PYTHONUTF8=1
export PROXY_HOST=192.168.80.1
export REPO_LINUX_PATH_ASTROPY=/mnt/e/superbased-observer/compression-testing/gate2/repos/astropy
export REPO_LINUX_PATH_DJANGO=/mnt/e/superbased-observer/compression-testing/gate2/repos/django

VENV=/home/sdrona/swe-bench-3slot-work/python-env/swebench-wsl
SCRIPT=/mnt/e/superbased-observer/compression-testing/gate2/run_gate2_swe_agent.py

echo '=== Gate 2 SWE-agent run (root) ==='
echo Proxy: $PROXY_HOST
echo Repos: astropy=$REPO_LINUX_PATH_ASTROPY  django=$REPO_LINUX_PATH_DJANGO

cd /mnt/e/superbased-observer/compression-testing/gate2
$VENV/bin/python3 $SCRIPT 2>&1