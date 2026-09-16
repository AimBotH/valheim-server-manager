#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "${ROOT_DIR}/bin"

if ! command -v go >/dev/null 2>&1; then
	echo "未找到 Go 工具链。生产部署不需要 Go，请直接使用 Linux 发布包中的 bin/valheim-panel。"
	exit 1
fi

cd "$ROOT_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags="-s -w" -o "${ROOT_DIR}/bin/valheim-panel" .

echo "已生成：${ROOT_DIR}/bin/valheim-panel"
