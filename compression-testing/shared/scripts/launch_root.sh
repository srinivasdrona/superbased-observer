#!/bin/bash
set -e
cd /tmp
KEY_FILE=/mnt/e/superbased-observer/compression-testing/gate2/.gate2_key
export AZURE_OPENAI_API_KEY=$(cat $KEY_FILE)
export OPENAI_API_KEY=$AZURE_OPENAI_API_KEY
export PYTHONUTF8=1
export PROXY_HOST=192.168.80.1

# Gate 2.2 repo paths — 10 repos
RBASE=/mnt/e/superbased-observer/compression-testing/gate2/repos
export REPO_LINUX_PATH_ASTROPY=$RBASE/astropy
export REPO_LINUX_PATH_DJANGO=$RBASE/django
export REPO_LINUX_PATH_MATPLOTLIB=$RBASE/matplotlib
export REPO_LINUX_PATH_SEABORN=$RBASE/seaborn
export REPO_LINUX_PATH_XARRAY=$RBASE/xarray
export REPO_LINUX_PATH_PYLINT=$RBASE/pylint
export REPO_LINUX_PATH_PYTEST=$RBASE/pytest
export REPO_LINUX_PATH_SKLEARN=$RBASE/scikit-learn
export REPO_LINUX_PATH_SPHINX=$RBASE/sphinx
export REPO_LINUX_PATH_SYMPY=$RBASE/sympy

VENV=/home/sdrona/swe-bench-3slot-work/python-env/swebench-wsl
SCRIPT=/mnt/e/superbased-observer/compression-testing/gate2/run_gate2_swe_agent.py

echo '=== Gate 2.2 SWE-agent run (root) ==='
echo Proxy: $PROXY_HOST
echo Repos: 10 repos configured

cd /mnt/e/superbased-observer/compression-testing/gate2
$VENV/bin/python3 $SCRIPT 2>&1