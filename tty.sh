#!/bin/bash
# linux-tty 一键安装/更新脚本
# 用法: bash tty.sh
#
# 可选环境变量:
#   LINUX_TTY_TARGET=tty1       目标 tty（默认 tty1）
#   LINUX_TTY_NO_RESTART=1      只安装，不重启服务（菜单 8 在线更新使用）
set -euo pipefail

BIN_URL="https://bash.niteng.net/iso/linux-tty"
BIN_PATH="/usr/local/sbin/linux-tty"
BIN_BAK="${BIN_PATH}.bak"
SHELL_PATH="/usr/local/sbin/linux-root-shell"
TARGET_TTY="${LINUX_TTY_TARGET:-tty1}"
SERVICE="linux-tty@${TARGET_TTY}.service"

echo "=== linux-tty installer (${SERVICE}) ==="

# root check
if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: need root, run with sudo"
  exit 1
fi

case "$TARGET_TTY" in
  ""|*[!A-Za-z0-9_.-]*)
    echo "ERROR: invalid LINUX_TTY_TARGET: $TARGET_TTY"
    exit 1
    ;;
esac

# private temp dir; never write to a predictable /tmp path
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/linux-tty.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT
TMP_BIN="$TMP_DIR/linux-tty"

# download binary
echo "[1/5] downloading linux-tty ..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o "$TMP_BIN" "$BIN_URL"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$TMP_BIN" "$BIN_URL"
else
  echo "ERROR: need curl or wget"
  exit 1
fi

if [ ! -s "$TMP_BIN" ]; then
  echo "ERROR: downloaded binary is empty"
  exit 1
fi

# Reject an HTML error page / truncated download before touching the real path.
MAGIC="$(head -c 4 "$TMP_BIN" | od -An -tx1 | tr -d ' \n')"
if [ "$MAGIC" != "7f454c46" ]; then
  echo "ERROR: downloaded file is not an ELF binary"
  exit 1
fi

# install binary (keep the previous one for rollback)
echo "[2/5] installing binary ..."
if [ -x "$BIN_PATH" ]; then
  cp -a "$BIN_PATH" "$BIN_BAK"
fi
install -m 0700 "$TMP_BIN" "$BIN_PATH"

rollback_binary() {
  if [ -x "$BIN_BAK" ]; then
    install -m 0700 "$BIN_BAK" "$BIN_PATH"
  else
    rm -f "$BIN_PATH"
  fi
}

# Cheap smoke test: a binary that cannot even start is restored before the
# running service is touched. timeout exit 124 is accepted because older
# releases busy-loop on /dev/null instead of exiting.
smoke_test() {
  command -v timeout >/dev/null 2>&1 || return 0
  local rc=0
  timeout -s KILL 2 "$BIN_PATH" </dev/null >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]
}

if ! smoke_test; then
  echo "ERROR: new binary failed smoke test; rolling back ..."
  rollback_binary
  exit 1
fi

# systemd unit
echo "[3/5] installing systemd units ..."
cat > /etc/systemd/system/linux-tty@.service <<'UNIT'
[Unit]
Description=SSCLOUD Linux-TTY Console
After=systemd-user-sessions.service plymouth-quit-wait.service
Before=getty.target
Conflicts=getty@%i.service linux-root@%i.service

[Service]
Environment=LANG=en_US.UTF-8
ExecStart=/usr/local/sbin/linux-tty
Restart=always
RestartSec=0
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/%I
KillMode=process

[Install]
WantedBy=multi-user.target
UNIT

# root-shell script
echo "[4/5] installing root-shell ..."
cat > "$SHELL_PATH" <<'SH'
#!/bin/bash
/bin/bash -l
exec /usr/local/sbin/linux-tty
SH
chmod 700 "$SHELL_PATH"

# disable sysrq
echo "[5/5] disabling sysrq ..."
echo 'kernel.sysrq = 0' > /etc/sysctl.d/99-linux-tty.conf
sysctl -w kernel.sysrq=0 2>/dev/null || true

# reload and enable the requested instance
systemctl daemon-reload
systemctl enable "$SERVICE"

restart_or_rollback() {
  if ! systemctl restart "$SERVICE"; then
    return 1
  fi
  sleep 1
  for _ in 1 2 3 4 5; do
    if systemctl is-active --quiet "$SERVICE"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

if [ "${LINUX_TTY_NO_RESTART:-0}" = "1" ]; then
  echo "Restart skipped (LINUX_TTY_NO_RESTART=1)."
else
  if ! restart_or_rollback; then
    echo "ERROR: $SERVICE failed to start; rolling back ..."
    rollback_binary
    systemctl daemon-reload
    restart_or_rollback || true
    exit 1
  fi
fi

echo ""
echo "=== DONE ==="
if [ "${LINUX_TTY_NO_RESTART:-0}" = "1" ]; then
  echo "Binary installed. Restart $SERVICE to use it."
elif systemctl is-active --quiet "$SERVICE"; then
  echo "linux-tty is running on ${TARGET_TTY}"
else
  echo "WARNING: service not active"
fi
