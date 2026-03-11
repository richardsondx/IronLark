#!/bin/bash
set -euo pipefail

source "$HOME/.local/bin/env"
uv run pytest "$TEST_DIR/test_outputs.py" -rA
