#!/usr/bin/env bash
# Backward-compatible alias for scripts/dist.sh
exec bash "$(dirname "$0")/dist.sh" "$@"
