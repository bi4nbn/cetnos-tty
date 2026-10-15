# linux-tty

**SSCLOUD Linux-TTY Console** —— 无显示器 Linux 设备的物理控制台菜单。单文件 Go 静态二进制，开机跑在物理 `tty1` 上，无需登录即可按键配置网络、Ping、查看磁盘、重启/关机。

支持 RHEL/CentOS/Rocky/Alma 与 Debian/Ubuntu。

![linux-tty menu](docs/menu.png)

## 菜单

| 按键 | 功能 |
|------|------|
| 1 | 静态 IP（自动备份原配置） |
| 2 | DHCP |
| 3 | 数据盘列表（整盘，自动排除系统盘） |
| 4 | Ping 223.6.6.6 |
| 5 | 重启 |
| 6 | 关机 |
| 7 | 进入系统（getty 账号密码登录） |
| `666` | 隐藏：免密进入 root shell（菜单不显示） |

任意输入框按 `Ctrl+C` / `ESC` 回主菜单。

## 快速安装

```bash
curl -fsSL https://bash.niteng.net/iso/tty.sh | bash
```

自动完成：下载二进制、安装 systemd unit、配置 getty override、关闭 sysrq。装完即跑在 tty1。

已安装的机器跑同一条命令即可更新。

## 手动部署

```bash
# 1. 二进制
install -m 0700 linux-tty /usr/local/sbin/linux-tty

# 2. systemd unit
cat > /etc/systemd/system/linux-tty@.service <<'EOF'
[Unit]
Description=SSCLOUD Linux-TTY Console
After=systemd-user-sessions.service plymouth-quit-wait.service
Before=getty.target
Conflicts=getty@%i.service linux-root@%i.service

[Service]
Environment=LANG=en_US.UTF-8
ExecStart=/usr/local/sbin/linux-tty
Restart=on-failure
RestartSec=1
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/%I
KillMode=process

[Install]
WantedBy=multi-user.target
EOF

# 3. getty override（登录退出后回菜单）
mkdir -p /etc/systemd/system/getty@tty1.service.d
cat > /etc/systemd/system/getty@tty1.service.d/override.conf <<'EOF'
[Service]
ExecStopPost=/usr/bin/systemctl start linux-tty@tty1.service
EOF

# 4. 隐藏 666 入口（不需要可删）
cat > /usr/local/sbin/linux-root-shell <<'EOF'
#!/bin/bash
/bin/bash -l
exec /usr/local/sbin/linux-tty
EOF
chmod 700 /usr/local/sbin/linux-root-shell

# 5. 关闭 sysrq，防止误触快捷键
echo 'kernel.sysrq = 0' > /etc/sysctl.d/99-linux-tty.conf
sysctl -w kernel.sysrq=0

# 6. 启用
systemctl daemon-reload
systemctl enable --now linux-tty@tty1.service
```

## 网络后端

启动时读 `/etc/os-release` 自动探测发行版：

- **RHEL 系**（CentOS/RHEL/Rocky/Alma/Fedora）：写 `/etc/sysconfig/network-scripts/ifcfg-*`，`nmcli` 生效。
- **Debian 系**（Debian/Ubuntu/Kali）：netplan 或 `/etc/network/interfaces`，自动识别。

## 编译

```bash
# Windows
.\build.ps1

# Linux/macOS
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.version=vX.Y.Z" -o linux-tty .
```

纯静态，无外部依赖，x86_64，约 2.4 MB。版本号编译时自动递增。

## 安全

- 二进制 `0700 root:root`，必须 root 运行。
- `666` 是隐藏免密 root 入口，物理控制台任何人输入即得 root，**仅在物理访问受控的设备上启用**；不需要时删掉 `/etc/systemd/system/linux-root-shell` 即可关闭。

## 卸载

```bash
systemctl disable --now linux-tty@tty1.service
rm -f /etc/systemd/system/linux-tty@.service
rm -f /usr/local/sbin/linux-root-shell
rm -f /usr/local/sbin/linux-tty
rm -rf /etc/systemd/system/getty@tty1.service.d
systemctl daemon-reload
```
