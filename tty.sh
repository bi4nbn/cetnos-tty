#!/bin/bash
# linux-tty 一键安装/更新脚本
# 用法: bash tty.sh
set -e

BIN_URL="https://bash.niteng.net/iso/linux-tty"
BIN_PATH="/usr/local/sbin/linux-tty"
SHELL_PATH="/usr/local/sbin/linux-root-shell"

echo "=== linux-tty installer ==="

# root check
if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: need root, run with sudo"
  exit 1
fi

# download binary
echo "[1/6] downloading linux-tty ..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o /tmp/linux-tty "$BIN_URL"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O /tmp/linux-tty "$BIN_URL"
else
  echo "ERROR: need curl or wget"
  exit 1
fi

# install binary
echo "[2/6] installing binary ..."
install -m 0700 /tmp/linux-tty "$BIN_PATH"
rm -f /tmp/linux-tty

# systemd unit
echo "[3/6] installing systemd units ..."
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

cat > /etc/systemd/system/linux-root@.service <<'UNIT'
[Unit]
Description=Root shell autologin (hidden)
After=systemd-user-sessions.service
Conflicts=linux-tty@%i.service

[Service]
ExecStart=-/sbin/agetty --autologin root --login-program /usr/local/sbin/linux-root-shell --noclear %I linux
Restart=no
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/%I
KillMode=process
ExecStopPost=/usr/bin/systemctl start linux-tty@%i.service

[Install]
WantedBy=multi-user.target
UNIT

# root-shell script
echo "[4/6] installing root-shell ..."
cat > "$SHELL_PATH" <<'SH'
#!/bin/bash
/bin/bash -l
exec /usr/local/sbin/linux-tty
SH
chmod 700 "$SHELL_PATH"

# login-exit script (option 7: normal login, return to menu on logout)
cat > /usr/local/sbin/login-exit <<'SH'
#!/bin/bash
stty sane 2>/dev/null
clear
/bin/login
exec /usr/local/sbin/linux-tty
SH
chmod 755 /usr/local/sbin/login-exit

# disable sysrq
echo "[5/5] disabling sysrq ..."
echo 'kernel.sysrq = 0' > /etc/sysctl.d/99-linux-tty.conf
sysctl -w kernel.sysrq=0 2>/dev/null || true

# reload and start
systemctl daemon-reload
systemctl enable linux-tty@tty1.service
systemctl stop getty@tty1.service 2>/dev/null || true
systemctl restart linux-tty@tty1.service

echo ""
echo "=== DONE ==="
systemctl is-active linux-tty@tty1 && echo "linux-tty is running on tty1" || echo "WARNING: service not active"
