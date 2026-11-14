#!/usr/bin/env bash
# Comprehensive installer test suite for install.sh
# Tests error handling, help text, and argument parsing
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SH="${SCRIPT_DIR}/install.sh"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

test_count=0
pass_count=0
fail_count=0

test_case() {
  local name="$1"
  test_count=$((test_count + 1))
  printf "%2d. %-60s " "$test_count" "$name"
}

pass() {
  pass_count=$((pass_count + 1))
  printf "${GREEN}PASS${NC}\n"
}

fail() {
  local reason="$1"
  fail_count=$((fail_count + 1))
  printf "${RED}FAIL${NC}\n"
  printf "     Error: %s\n" "$reason"
}

# ============================================================================
# Test 1: Bash syntax validation
# ============================================================================
test_case "Bash syntax is valid"
if bash -n "$INSTALL_SH" 2>&1 >/dev/null; then
  pass
else
  fail "Bash syntax check failed"
fi

# ============================================================================
# Test 2: Help flag displays usage
# ============================================================================
test_case "Help flag (--help) displays usage"
output=$(bash "$INSTALL_SH" --help 2>&1 || true)
if echo "$output" | grep -q "Usage: install.sh"; then
  pass
else
  fail "Help text not found in output"
fi

# ============================================================================
# Test 3: Help flag shows all options
# ============================================================================
test_case "Help includes all documented options"
output=$(bash "$INSTALL_SH" --help 2>&1 || true)
required_opts=(
  "help"
  "cli-only"
  "browser-auth"
  "force"
  "allow-downgrade"
  "release"
  "token-file"
)
all_found=true
for opt in "${required_opts[@]}"; do
  if ! echo "$output" | grep -F "$opt" >/dev/null; then
    all_found=false
    echo "Missing option: $opt"
    break
  fi
done
if $all_found; then
  pass
else
  fail "Not all options found in help text"
fi

# ============================================================================
# Test 4: Invalid argument shows help
# ============================================================================
test_case "Invalid argument shows help and errors"
output=$(bash "$INSTALL_SH" --invalid-arg 2>&1 || true)
if echo "$output" | grep -q "Usage: install.sh" && echo "$output" | grep -q "ERROR"; then
  pass
else
  fail "Help not shown for invalid argument"
fi

# ============================================================================
# Test 5: Short help flag (-h)
# ============================================================================
test_case "Short help flag (-h) works"
output=$(bash "$INSTALL_SH" -h 2>&1 || true)
if echo "$output" | grep -q "Usage: install.sh"; then
  pass
else
  fail "Short help flag doesn't work"
fi

# ============================================================================
# Test 6: Missing --release argument shows error
# ============================================================================
test_case "--release without version shows error"
output=$(bash "$INSTALL_SH" --release 2>&1 || true)
if echo "$output" | grep -q "ERROR" || echo "$output" | grep -q "requires"; then
  pass
else
  fail "Error not shown for missing --release argument"
fi

# ============================================================================
# Test 6b: Empty --release=value shows error
# ============================================================================
test_case "--release= without version shows error"
output=$(bash "$INSTALL_SH" --release= 2>&1 || true)
if echo "$output" | grep -q "ERROR" || echo "$output" | grep -q "requires"; then
  pass
else
  fail "Error not shown for empty --release= argument"
fi

# ============================================================================
# Test 7: Missing --token-file argument shows error
# ============================================================================
test_case "--token-file without path shows error"
output=$(bash "$INSTALL_SH" --token-file 2>&1 || true)
if echo "$output" | grep -q "ERROR" || echo "$output" | grep -q "requires"; then
  pass
else
  fail "Error not shown for missing --token-file argument"
fi

# ============================================================================
# Test 8: Non-existent token file handled gracefully
# ============================================================================
test_case "Non-existent token file produces helpful error"
output=$(bash "$INSTALL_SH" --token-file /tmp/nonexistent-token-99999.txt --cli-only 2>&1 || true)
if echo "$output" | grep -qE "token file|not readable|ERROR"; then
  pass
else
  fail "Error message not clear for missing token file"
fi

# ============================================================================
# Test 9: Environment variables are documented
# ============================================================================
test_case "Help shows environment variable examples"
output=$(bash "$INSTALL_SH" --help 2>&1 || true)
env_vars=(
  "VERSION"
  "GITHUB_TOKEN"
  "GH_TOKEN"
)
all_found=true
for var in "${env_vars[@]}"; do
  if ! echo "$output" | grep -q "$var"; then
    all_found=false
    break
  fi
done
if $all_found; then
  pass
else
  fail "Environment variables not documented in help"
fi

# ============================================================================
# Test 10: Examples are provided in help
# ============================================================================
test_case "Help includes usage examples"
output=$(bash "$INSTALL_SH" --help 2>&1 || true)
if echo "$output" | grep -q "Examples:" && echo "$output" | grep -q "curl\|bash"; then
  pass
else
  fail "Examples section not found in help"
fi

# ============================================================================
# Test 11: Browser auth option is documented
# ============================================================================
test_case "Browser auth (--browser-auth) is documented"
output=$(bash "$INSTALL_SH" --help 2>&1 || true)
if echo "$output" | grep -q "\-\-browser-auth"; then
  pass
else
  fail "Browser auth option not documented"
fi

# ============================================================================
# Test 12: Help text mentions GitHub CLI requirement for browser auth
# ============================================================================
test_case "Help text mentions GitHub CLI for browser auth"
output=$(bash "$INSTALL_SH" --help 2>&1 || true)
if echo "$output" | grep -A1 "browser-auth" | grep -q "gh\|GitHub CLI"; then
  pass
else
  fail "GitHub CLI requirement not mentioned"
fi

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "======================================================================"
printf "Test Results: ${GREEN}%d passed${NC}, ${RED}%d failed${NC}, %d total\n" \
  "$pass_count" "$fail_count" "$test_count"
echo "======================================================================"

if [[ $fail_count -eq 0 ]]; then
  echo "✓ All tests passed!"
  exit 0
else
  echo "✗ Some tests failed"
  exit 1
fi
