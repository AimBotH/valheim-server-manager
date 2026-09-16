#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
VERSION_FILE="${ROOT_DIR}/VERSION"
current="$(tr -d '[:space:]' < "$VERSION_FILE")"

IFS=. read -r major minor patch <<< "$current"
patch=$((patch + 1))
next="${major}.${minor}.${patch}"
printf '%s\n' "$next" > "$VERSION_FILE"
printf 'version: %s -> %s\n' "$current" "$next"
