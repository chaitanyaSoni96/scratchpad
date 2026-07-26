#!/usr/bin/env bash
# Register the scratchpad MCP server (podman-wrapped) with local agent CLIs.
# Usage: register-mcp.sh [claude|opencode|goose|pi|all]
set -euo pipefail

TARGET="${1:-all}"
IMAGE="localhost/scratchpad:latest"
DATA="$HOME/.scratchpad"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# One podman invocation, reused everywhere. SCRATCHPAD_ROOT=/data is baked
# into the image; -i wires stdio through to the MCP server.
PODMAN_ARGS=(run -i --rm -v "$DATA:/data:z" "$IMAGE" /scratchpad-mcp)

register_claude() {
  claude mcp remove -s user scratchpad >/dev/null 2>&1 || true
  claude mcp remove -s local scratchpad >/dev/null 2>&1 || true
  claude mcp add -s user scratchpad -- podman "${PODMAN_ARGS[@]}"
  echo "claude: registered user-wide (claude mcp list to verify)"
}

register_opencode() {
  # opencode 1.x schema: servers directly under "mcp", command as one array.
  local cfg="$HOME/.config/opencode/opencode.json"
  mkdir -p "$(dirname "$cfg")"
  CFG="$cfg" DATA="$DATA" IMAGE="$IMAGE" python3 - <<'EOF'
import json, os
cfg_path = os.environ["CFG"]
cfg = {}
if os.path.exists(cfg_path):
    with open(cfg_path) as f:
        cfg = json.load(f)
cfg.setdefault("$schema", "https://opencode.ai/config.json")
cfg.setdefault("mcp", {})["scratchpad"] = {
    "type": "local",
    "command": ["podman", "run", "-i", "--rm",
                "-v", f"{os.environ['DATA']}:/data:z",
                os.environ["IMAGE"], "/scratchpad-mcp"],
    "enabled": True,
}
with open(cfg_path, "w") as f:
    json.dump(cfg, f, indent=2)
print(f"opencode: registered in {cfg_path}")
EOF
}

register_goose() {
  local cfg="$HOME/.config/goose/config.yaml"
  mkdir -p "$(dirname "$cfg")"
  local py="python3"
  if ! python3 -c 'import yaml' 2>/dev/null && command -v uv >/dev/null; then
    py="uv run --quiet --with pyyaml python3"
  fi
  if $py -c 'import yaml' 2>/dev/null; then
    CFG="$cfg" DATA="$DATA" IMAGE="$IMAGE" $py - <<'EOF'
import os, yaml
cfg_path = os.environ["CFG"]
cfg = {}
if os.path.exists(cfg_path):
    with open(cfg_path) as f:
        cfg = yaml.safe_load(f) or {}
cfg.setdefault("extensions", {})["scratchpad"] = {
    "name": "scratchpad",
    "type": "stdio",
    "cmd": "podman",
    "args": ["run", "-i", "--rm", "-v", f"{os.environ['DATA']}:/data:z",
             os.environ["IMAGE"], "/scratchpad-mcp"],
    "enabled": True,
    "timeout": 300,
    "description": "Publish html/css/js artifacts to the local scratchpad",
}
with open(cfg_path, "w") as f:
    yaml.safe_dump(cfg, f, sort_keys=False)
print(f"goose: registered in {cfg_path}")
EOF
  elif [ ! -f "$cfg" ]; then
    cat > "$cfg" <<EOF
extensions:
  scratchpad:
    name: scratchpad
    type: stdio
    cmd: podman
    args: [run, -i, --rm, -v, "$DATA:/data:z", "$IMAGE", /scratchpad-mcp]
    enabled: true
    timeout: 300
    description: Publish html/css/js artifacts to the local scratchpad
EOF
    echo "goose: created $cfg"
  else
    echo "goose: PyYAML unavailable and $cfg exists - add the extensions.scratchpad block manually (see README)" >&2
  fi
}

register_pi() {
  # pi has no MCP support; install the bundled extension instead.
  local dst="$HOME/.pi/agent/extensions"
  mkdir -p "$dst"
  cp "$REPO_DIR/contrib/pi/scratchpad.ts" "$dst/scratchpad.ts"
  echo "pi: extension installed at $dst/scratchpad.ts"
}

case "$TARGET" in
  claude)   register_claude ;;
  opencode) register_opencode ;;
  goose)    register_goose ;;
  pi)       register_pi ;;
  all)      register_claude; register_opencode; register_goose; register_pi ;;
  *) echo "usage: $0 [claude|opencode|goose|pi|all]" >&2; exit 2 ;;
esac
