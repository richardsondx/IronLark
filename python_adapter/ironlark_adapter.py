from __future__ import annotations

import os
import shlex
import tempfile
from pathlib import Path

from terminal_bench.agents.installed_agents.abstract_installed_agent import (
    AbstractInstalledAgent,
)
from terminal_bench.agents.base_agent import AgentResult
from terminal_bench.terminal.models import TerminalCommand
from terminal_bench.terminal.tmux_session import TmuxSession


class IronLarkAgent(AbstractInstalledAgent):
    DEFAULT_GO_VERSION = "1.24.0"
    DEFAULT_INSTALL_MODE = "source"
    DEFAULT_INSTALL_DIR = "/usr/local/bin"
    DEFAULT_SOURCE_DIR = "/opt/ironlark-src"
    DEFAULT_CONFIG_HOME = "/tmp/ironlark-config"
    DEFAULT_DATA_HOME = "/tmp/ironlark-data"
    DEFAULT_PROVIDER = "openai"
    DEFAULT_MODEL = "gpt-4.1-mini"
    PASSTHROUGH_ENV_VARS = (
        "OPENAI_API_KEY",
        "OPENAI_BASE_URL",
        "OPENAI_ORG_ID",
        "OPENROUTER_API_KEY",
        "ANTHROPIC_API_KEY",
    )

    @staticmethod
    def name() -> str:
        return "ironlark"

    def __init__(
        self,
        model_name: str | None = None,
        provider: str | None = None,
        approval: str = "agent",
        install_mode: str = DEFAULT_INSTALL_MODE,
        repo_root: str | None = None,
        repo_slug: str = "richardsondx/IronLark",
        release_version: str = "latest",
        go_version: str = DEFAULT_GO_VERSION,
        install_dir: str = DEFAULT_INSTALL_DIR,
        source_dir: str = DEFAULT_SOURCE_DIR,
        xdg_config_home: str = DEFAULT_CONFIG_HOME,
        xdg_data_home: str = DEFAULT_DATA_HOME,
        *args,
        **kwargs,
    ):
        super().__init__(*args, **kwargs)
        raw_model_name = model_name or os.getenv("IRONLARK_MODEL") or self.DEFAULT_MODEL
        raw_provider = provider or os.getenv("IRONLARK_PROVIDER") or self.DEFAULT_PROVIDER
        if "/" in raw_model_name:
            inferred_provider, inferred_model = raw_model_name.split("/", 1)
            if provider is None and not os.getenv("IRONLARK_PROVIDER"):
                raw_provider = inferred_provider
            if raw_provider == "openai":
                raw_model_name = inferred_model

        self._model_name = raw_model_name
        self._provider = raw_provider
        self._approval = approval
        self._install_mode = install_mode
        self._repo_root = (
            Path(repo_root).expanduser().resolve()
            if repo_root
            else Path(__file__).resolve().parent.parent
        )
        self._repo_slug = repo_slug
        self._release_version = release_version
        self._go_version = go_version
        self._install_dir = install_dir.rstrip("/")
        self._source_dir = source_dir.rstrip("/")
        self._xdg_config_home = xdg_config_home.rstrip("/")
        self._xdg_data_home = xdg_data_home.rstrip("/")
        self._version = kwargs.get("version", release_version if install_mode == "release" else "local")

        if self._install_mode not in {"source", "release"}:
            raise ValueError("install_mode must be 'source' or 'release'")

    @property
    def _env(self) -> dict[str, str]:
        env = {
            "XDG_CONFIG_HOME": self._xdg_config_home,
            "XDG_DATA_HOME": self._xdg_data_home,
        }
        for key in self.PASSTHROUGH_ENV_VARS:
            value = os.getenv(key)
            if value:
                env[key] = value
        return env

    @property
    def _install_agent_script_path(self) -> Path:
        script = self._build_install_script()
        handle = tempfile.NamedTemporaryFile(mode="w", suffix=".sh", delete=False)
        handle.write(script)
        handle.close()
        os.chmod(handle.name, 0o755)
        return Path(handle.name)

    def _run_agent_commands(self, instruction: str) -> list[TerminalCommand]:
        flags = [
            f"PATH={shlex.quote(self._install_dir)}:$PATH",
            f"XDG_CONFIG_HOME={shlex.quote(self._xdg_config_home)}",
            f"XDG_DATA_HOME={shlex.quote(self._xdg_data_home)}",
            "lark",
            "--approval",
            shlex.quote(self._approval),
            "--provider",
            shlex.quote(self._provider),
            "--model",
            shlex.quote(self._model_name),
            "--no-context",
            "--new-thread",
            "--color",
            "never",
            "--",
            shlex.quote(instruction),
        ]
        return [
            TerminalCommand(
                command=" ".join(flags),
                min_timeout_sec=0.0,
                max_timeout_sec=float("inf"),
                block=True,
                append_enter=True,
            )
        ]

    def perform_task(
        self,
        instruction: str,
        session: TmuxSession,
        logging_dir: Path | None = None,
    ) -> AgentResult:
        if self._install_mode == "source":
            session.copy_to_container(self._repo_root, container_dir=self._source_dir)
        return super().perform_task(instruction, session, logging_dir)

    def _build_install_script(self) -> str:
        if self._install_mode == "release":
            return self._release_install_script()
        return self._source_install_script()

    def _release_install_script(self) -> str:
        repo_slug = shlex.quote(self._repo_slug)
        release_version = shlex.quote(self._release_version)
        install_dir = shlex.quote(self._install_dir)
        return f"""#!/bin/bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y curl ca-certificates tar

REPO_SLUG={repo_slug}
VERSION={release_version}
INSTALL_DIR={install_dir}

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

RELEASE_URL="https://github.com/${{REPO_SLUG}}/releases/latest/download/lark_linux_${{ARCH}}.tar.gz"
if [ "$VERSION" != "latest" ]; then
  RELEASE_URL="https://github.com/${{REPO_SLUG}}/releases/download/${{VERSION}}/lark_linux_${{ARCH}}.tar.gz"
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$INSTALL_DIR" "{self._xdg_config_home}/lark" "{self._xdg_data_home}/lark"
curl -fsSL "$RELEASE_URL" -o "$tmpdir/lark.tar.gz"
tar -xzf "$tmpdir/lark.tar.gz" -C "$tmpdir"
install "$tmpdir/lark" "$INSTALL_DIR/lark"
ln -sf "$INSTALL_DIR/lark" "$INSTALL_DIR/lk"
"""

    def _source_install_script(self) -> str:
        source_dir = shlex.quote(self._source_dir)
        install_dir = shlex.quote(self._install_dir)
        go_version = shlex.quote(self._go_version)
        return f"""#!/bin/bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y curl ca-certificates git tar

GO_VERSION={go_version}
INSTALL_DIR={install_dir}
SOURCE_DIR={source_dir}

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) GOARCH="amd64" ;;
  aarch64|arm64) GOARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

curl -fsSL "https://go.dev/dl/go${{GO_VERSION}}.linux-${{GOARCH}}.tar.gz" -o "$tmpdir/go.tar.gz"
rm -rf /usr/local/go
tar -C /usr/local -xzf "$tmpdir/go.tar.gz"
export PATH="/usr/local/go/bin:$PATH"

mkdir -p "$INSTALL_DIR" "{self._xdg_config_home}/lark" "{self._xdg_data_home}/lark"
cd "$SOURCE_DIR"
go build -o "$INSTALL_DIR/lark" ./cmd/lark
ln -sf "$INSTALL_DIR/lark" "$INSTALL_DIR/lk"
"""


def create_agent(config: dict) -> IronLarkAgent:
    return IronLarkAgent(**config)
