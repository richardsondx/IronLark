#!/bin/bash
set -euo pipefail

apt-get update
apt-get install -y curl
curl -LsSf https://astral.sh/uv/0.7.13/install.sh | sh
source "$HOME/.local/bin/env"
uv venv
uv pip install pytest==8.4.1
