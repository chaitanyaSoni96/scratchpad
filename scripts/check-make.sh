#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "check-make: $*" >&2
  exit 1
}

assert_contains() {
  local output="$1" expected="$2" description="$3"
  [[ "$output" == *"$expected"* ]] || fail "$description: missing '$expected'"
}

install_default="$(make -n install)"
install_lan="$(make -n install LAN=1)"
web_default="$(make -n web)"
web_lan="$(make -n web LAN=1)"

assert_contains "$install_default" "--addr 127.0.0.1:8737/" "default install bind"
assert_contains "$install_lan" "--addr 0.0.0.0:8737/" "LAN install bind"
assert_contains "$web_default" "-p 127.0.0.1:8737:8737" "default web bind"
assert_contains "$web_lan" "-p 0.0.0.0:8737:8737" "LAN web bind"

if make -n install LAN=invalid >/dev/null 2>&1; then
  fail "invalid LAN value was accepted"
fi

assert_contains "$(<systemd/scratchpad-web.service)" \
  "--addr 127.0.0.1:8737" "source systemd bind"
assert_contains "$(<Containerfile)" \
  'CMD ["/scratchpad-web", "--addr", ":8737"]' "container internal bind"

echo "check-make: ok"
