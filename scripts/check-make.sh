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

assert_not_contains() {
  local output="$1" unexpected="$2" description="$3"
  [[ "$output" != *"$unexpected"* ]] || fail "$description: unexpectedly contains '$unexpected'"
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

# P5.3 — Windows cross-builds: both arches, CGO off, reproducible flags, and
# output isolated in dist/ so it can never collide with the host bin/.
build_default="$(make -n build)"
build_windows="$(make -n build-windows)"

assert_contains "$build_default" "go build -o bin/ ./cmd/..." "host build shape"
assert_not_contains "$build_default" "GOOS=windows" "host build stays host-only"
assert_not_contains "$build_default" "dist/" "host build stays out of dist/"

assert_contains "$build_windows" "GOOS=windows GOARCH=amd64 CGO_ENABLED=0" "windows amd64 cross-build env"
assert_contains "$build_windows" "GOOS=windows GOARCH=arm64 CGO_ENABLED=0" "windows arm64 cross-build env"
assert_contains "$build_windows" "-trimpath" "windows build reproducibility flag"
assert_contains "$build_windows" "-o dist/windows-amd64/" "windows amd64 output dir"
assert_contains "$build_windows" "-o dist/windows-arm64/" "windows arm64 output dir"
assert_not_contains "$build_windows" "bin/" "windows build stays out of bin/"

echo "check-make: ok"
