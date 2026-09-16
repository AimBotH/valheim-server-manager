#!/usr/bin/env bash

PORT="${PORT:-8787}"
HOST="${HOST:-0.0.0.0}"
DATA_DIR="${DATA_DIR:-data}"
LEVEL="${LEVEL:-info}"
CERT_FILE="${CERT_FILE:-}"
KEY_FILE="${KEY_FILE:-}"
GAME_PORT="${GAME_PORT:-2456}"
SERVICE_NAME="valheim-panel"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_FILE=""
WORK_DIR="${SCRIPT_DIR}"
VERSION_FILE="${SCRIPT_DIR}/VERSION"

function resolve_binary() {
	local candidates=(
		"${SCRIPT_DIR}/bin/valheim-panel"
		"${SCRIPT_DIR}/valheim-panel-linux-1.0.5/bin/valheim-panel"
		"${SCRIPT_DIR}/valheim-panel/bin/valheim-panel"
		"${SCRIPT_DIR}/valheim-panel-linux-amd64"
		"${SCRIPT_DIR}/dist/valheim-panel-linux-amd64"
	)
	local candidate
	for candidate in "${candidates[@]}"; do
		if [[ -d "$candidate" ]]; then
			echo_red "二进制路径是目录而不是文件：${candidate}"
			continue
		fi
		if [[ -f "$candidate" ]]; then
			if [[ ! -x "$candidate" ]]; then
				chmod +x "$candidate" 2>/dev/null || true
			fi
			if [[ -x "$candidate" ]]; then
				BIN_FILE="$candidate"
				if [[ "$candidate" == "${SCRIPT_DIR}/valheim-panel/bin/valheim-panel" ]]; then
					WORK_DIR="${SCRIPT_DIR}/valheim-panel"
				elif [[ "$candidate" == "${SCRIPT_DIR}/valheim-panel-linux-1.0.5/bin/valheim-panel" ]]; then
					WORK_DIR="${SCRIPT_DIR}/valheim-panel-linux-1.0.5"
				else
					WORK_DIR="${SCRIPT_DIR}"
				fi
				if [[ -f "${WORK_DIR}/VERSION" ]]; then
					VERSION_FILE="${WORK_DIR}/VERSION"
				fi
				return 0
			fi
			echo_red "二进制没有执行权限：${candidate}"
			echo_yellow "请执行：chmod +x ${candidate}"
			return 1
		fi
	done
	return 1
}

function echo_red() { echo -e "\033[0;31m$*\033[0m"; }
function echo_green() { echo -e "\033[0;32m$*\033[0m"; }
function echo_yellow() { echo -e "\033[0;33m$*\033[0m"; }

resolve_binary || true
STATE_DIR="${DATA_DIR}"
if [[ "$STATE_DIR" != /* ]]; then
	STATE_DIR="${WORK_DIR}/${DATA_DIR}"
fi
PID_FILE="${STATE_DIR}/panel.pid"
LOG_FILE="${STATE_DIR}/panel.log"

function version() {
	if [[ -f "$VERSION_FILE" ]]; then
		tr -d '[:space:]' < "$VERSION_FILE"
	elif [[ -x "$BIN_FILE" ]]; then
		"$BIN_FILE" -v 2>/dev/null | head -n1 | sed 's/^v//; s/-go$//'
	else
		echo "unknown"
	fi
}

function require_linux() {
	if [[ "$(uname -s)" != "Linux" ]]; then
		echo_red "此脚本只用于 Linux。"
		exit 1
	fi
}

function require_binary() {
	require_linux
	resolve_binary || true
	if [[ -z "$BIN_FILE" || ! -x "$BIN_FILE" ]]; then
		echo_red "找不到 Linux 二进制。"
		echo_yellow "已查找："
		echo "  ${SCRIPT_DIR}/bin/valheim-panel"
		echo "  ${SCRIPT_DIR}/valheim-panel-linux-1.0.5/bin/valheim-panel"
		echo "  ${SCRIPT_DIR}/valheim-panel/bin/valheim-panel"
		echo "  ${SCRIPT_DIR}/valheim-panel-linux-amd64"
		echo "  ${SCRIPT_DIR}/dist/valheim-panel-linux-amd64"
		exit 1
	fi
}

function run_as_root() {
	if [[ "$(id -u)" -eq 0 ]]; then
		"$@"
	elif command -v sudo >/dev/null 2>&1; then
		sudo "$@"
	else
		echo_red "需要 root 权限或 sudo：$*"
		exit 1
	fi
}

function systemd_available() {
	command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]
}

function service_installed() {
	[[ -f "$SERVICE_FILE" ]]
}

function health_check() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsS --max-time 3 "http://127.0.0.1:${PORT}/" >/dev/null 2>&1
		return $?
	fi
	[[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" >/dev/null 2>&1
}

function start_panel() {
	require_binary
	mkdir -p "$STATE_DIR"
	if service_installed && systemd_available; then
		run_as_root systemctl start "$SERVICE_NAME"
	else
		stop_panel
		local args=(-bind "$PORT" -dbpath "$STATE_DIR" -level "$LEVEL")
		if [[ -n "$CERT_FILE" ]]; then args+=(-cert "$CERT_FILE"); fi
		if [[ -n "$KEY_FILE" ]]; then args+=(-key "$KEY_FILE"); fi
		nohup "$BIN_FILE" "${args[@]}" >> "$LOG_FILE" 2>&1 &
		echo $! > "$PID_FILE"
	fi
	sleep 1
	if health_check; then
		echo_green "Valheim Panel 已启动：http://服务器IP:${PORT}"
	else
		echo_red "启动失败，日志：${LOG_FILE}"
		tail -n 40 "$LOG_FILE" 2>/dev/null || true
		exit 1
	fi
}

function stop_panel() {
	if service_installed && systemd_available; then
		run_as_root systemctl stop "$SERVICE_NAME"
		return 0
	fi
	if [[ -f "$PID_FILE" ]]; then
		local pid
		pid="$(cat "$PID_FILE" 2>/dev/null || true)"
		if [[ -n "$pid" ]] && kill -0 "$pid" >/dev/null 2>&1; then
			kill "$pid" 2>/dev/null || true
			for _ in {1..20}; do
				kill -0 "$pid" >/dev/null 2>&1 || break
				sleep 0.25
			done
			kill -9 "$pid" 2>/dev/null || true
		fi
		rm -f "$PID_FILE"
	fi
}

function status_panel() {
	if health_check; then
		echo_green "运行中：http://服务器IP:${PORT}"
		return 0
	fi
	echo_red "未运行"
	return 1
}

function install_service() {
	require_binary
	systemd_available || {
		echo_red "当前系统未使用 systemd。"
		exit 1
	}
	cat > /tmp/${SERVICE_NAME}.service <<EOF
[Unit]
Description=Valheim Panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${WORK_DIR}
EnvironmentFile=-${WORK_DIR}/panel.env
ExecStart=${BIN_FILE} -bind ${PORT} -dbpath ${STATE_DIR} -level ${LEVEL}
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
	run_as_root cp /tmp/${SERVICE_NAME}.service "$SERVICE_FILE"
	rm -f /tmp/${SERVICE_NAME}.service
	run_as_root systemctl daemon-reload
	run_as_root systemctl enable --now "$SERVICE_NAME"
	echo_green "systemd 服务已安装：${SERVICE_NAME}"
}

function remove_service() {
	if service_installed && systemd_available; then
		run_as_root systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
		run_as_root rm -f "$SERVICE_FILE"
		run_as_root systemctl daemon-reload
		echo_green "systemd 服务已移除"
	fi
}

function open_firewall() {
	local end_port=$((GAME_PORT + 1))
	if command -v ufw >/dev/null 2>&1; then
		run_as_root ufw allow "${PORT}/tcp"
		run_as_root ufw allow "${GAME_PORT}:${end_port}/udp"
		run_as_root ufw reload || true
	elif command -v firewall-cmd >/dev/null 2>&1; then
		run_as_root firewall-cmd --permanent --add-port="${PORT}/tcp"
		run_as_root firewall-cmd --permanent --add-port="${GAME_PORT}-${end_port}/udp"
		run_as_root firewall-cmd --reload
	else
		echo_yellow "未发现 ufw/firewalld，请手动放行 TCP ${PORT} 和 UDP ${GAME_PORT}-${end_port}"
		return 0
	fi
	echo_green "已放行 TCP ${PORT} 和 UDP ${GAME_PORT}-${end_port}"
}

function doctor() {
	require_linux
	echo_green "Valheim Panel doctor"
	echo "版本：$(version)"
	echo "工作目录：${WORK_DIR}"
	echo "二进制：${BIN_FILE}"
	echo "二进制存在：$( [[ -x "$BIN_FILE" ]] && echo yes || echo no )"
	echo "数据目录：${STATE_DIR}"
	echo "HTTP 端口：${PORT}"
	if [[ -x "$BIN_FILE" ]]; then
		echo "运行版本：$("$BIN_FILE" -v 2>/dev/null | head -n1)"
	fi
	status_panel || true
}

function show_help() {
	cat <<EOF
Valheim Panel Linux 部署脚本

用法：
  ./run.sh start             启动面板
  ./run.sh stop              停止面板
  ./run.sh restart           重启面板
  ./run.sh status            查看状态
  ./run.sh doctor            环境自检
  ./run.sh logs              查看日志
  ./run.sh systemd-install   安装 systemd 服务
  ./run.sh systemd-remove    移除 systemd 服务
  ./run.sh firewall          放行面板和 Valheim 端口

环境变量：
  PORT=8787
  DATA_DIR=data
  GAME_PORT=2456
  LEVEL=info

SteamCMD 不需要提前安装。启动面板后进入“平台管理”，点击“安装 / 修复”。
EOF
}

case "${1:-help}" in
	start) start_panel ;;
	stop) stop_panel; echo_green "已停止" ;;
	restart) stop_panel; start_panel ;;
	status) status_panel ;;
	doctor) doctor ;;
	logs) tail -n 200 -f "$LOG_FILE" ;;
	systemd-install) install_service ;;
	systemd-remove) remove_service ;;
	firewall) open_firewall ;;
	help|-h|--help) show_help ;;
	*)
		echo_red "未知命令：$1"
		show_help
		exit 1
		;;
esac
