# cetnos-tty

**SSCLOUD CentOS7-TTY** —— 面向无显示器（headless）Linux 设备的物理控制台菜单。

单文件 Go 静态二进制，开机直接跑在物理终端 `tty1` 上，**无需登录**，按键即可配置网络、Ping、查看磁盘、重启或关机。

## 功能菜单

| 按键 | 功能 |
|------|------|
| 1 | 静态 IP（选网卡，交互输入 IP/前缀/网关/DNS，自动备份原 ifcfg） |
| 2 | DHCP（选网卡，写 `BOOTPROTO=dhcp`，自动备份） |
| 3 | 数据盘列表（自动排除系统盘，显示容量/类型/总线/型号/序列号/挂载点） |
| 4 | Ping 测试（223.6.6.6，4 包） |
| 5 | 重启（二次确认） |
| 6 | 关机（二次确认） |
| 7 | 退出菜单，进入系统登录（getty，需账号密码） |
| `666` | **隐藏入口**：直接免密进入 root shell（菜单不显示，仅物理 VT 控制台） |

- 任意输入框按 `Ctrl+C` 或 `ESC` 直接回主菜单。
- 系统盘经 `/proc/mounts` + `/sys/block` 回溯识别（含 LVM/dm），不依赖 `lsblk`。

## 编译

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o cetnos-tty .
```

纯静态，无 C 库/外部工具依赖，CentOS 7+ x86_64，约 2.4 MB。

## 部署

把编好的二进制传到目标机（如 `scp src/cetnos-tty root@目标:/usr/local/sbin/cetnos-tty`），然后 **root** 在目标机粘贴下面整段：

```bash
set -e

# 1. 二进制权限
install -m 0700 /usr/local/sbin/cetnos-tty /usr/local/sbin/cetnos-tty

# 2. 菜单服务（开机自启 tty1）
cat > /etc/systemd/system/cetnos-tty@.service <<'EOF'
[Unit]
Description=SSCLOUD CentOS7-TTY Console
After=systemd-user-sessions.service plymouth-quit-wait.service
Before=getty.target
Conflicts=getty@tty1.service cetnos-root@tty1.service

[Service]
ExecStart=/usr/local/sbin/cetnos-tty
Restart=always
RestartSec=0
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/tty1
KillMode=process

[Install]
WantedBy=multi-user.target
EOF

# 3. 选项7：getty 退出后自动回菜单
mkdir -p /etc/systemd/system/getty@tty1.service.d
cat > /etc/systemd/system/getty@tty1.service.d/override.conf <<'EOF'
[Service]
ExecStopPost=/usr/bin/systemctl start cetnos-tty@tty1.service
EOF

# 4. 隐藏 666：免密 root 入口（不需要就删掉这两段）
cat > /etc/systemd/system/cetnos-root@.service <<'EOF'
[Unit]
Description=Root shell autologin (hidden)
After=systemd-user-sessions.service

[Service]
ExecStart=-/sbin/agetty --autologin root --login-program /usr/local/sbin/cetnos-root-shell --noclear %I linux
Restart=no
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/%I
KillMode=process
ExecStopPost=/usr/bin/systemctl start cetnos-tty@%i.service

[Install]
WantedBy=multi-user.target
EOF

cat > /usr/local/sbin/cetnos-root-shell <<'EOF'
#!/bin/bash
/bin/bash -l
EOF
chmod 700 /usr/local/sbin/cetnos-root-shell

# 5. 关闭 SysRq 并启用
echo 'kernel.sysrq = 0' > /etc/sysctl.d/99-cetnos-tty.conf
sysctl -w kernel.sysrq=0
systemctl daemon-reload
systemctl enable --now cetnos-tty@tty1.service
```

验证：

```bash
systemctl status cetnos-tty@tty1     # active (running)
systemctl is-enabled cetnos-tty@tty1 # enabled
```

切到物理 tty1 应直接显示菜单：输入 `666` 回车进 root shell，`exit` 回菜单；按 `7` 走账号密码登录，退出也回菜单。

## 工作原理

开机后 systemd 在 `tty1` 跑 cetnos-tty，它与 `getty@tty1`、`cetnos-root@tty1` 互斥（`Conflicts` 只在 cetnos-tty 一侧声明，避免退出时 `ExecStopPost` 拉起菜单形成双向死锁）。

```
开机
  └─► cetnos-tty 菜单（tty1，无需登录）
        ├─ 按 7 ─► getty@tty1（Conflicts 停菜单）
        │           └─► 账号密码登录 ─► shell ─► logout
        │                 ExecStopPost start cetnos-tty ─► 回菜单
        │
        └─ 输入 666 ─► cetnos-root@tty1（agetty --autologin root）
                          └─► 免密 root shell ─► exit
                                ExecStopPost start cetnos-tty ─► 回菜单
```

`cetnos-root-shell` 被 agetty 经 `--login-program` `exec()` 调用，脚本里只跑一次 `/bin/bash -l`；shell 退出脚本即结束、模板服务停止，由 `ExecStopPost` 拉回菜单，**只登录一次、不循环自动登录**。

## 安全

- 二进制 `0700 root:root`，仅 root 可执行；程序必须 root 运行（配网络/重启关机）。
- 按 `7` 进系统仍需 getty 账号密码校验。
- `666` 是隐藏免密 root 入口：物理控制台任何人输入即得 root，菜单不显示。**仅在物理访问受控的设备上启用**；不需要时删掉 `/etc/systemd/system/cetnos-root@.service` 和 `/usr/local/sbin/cetnos-root-shell` 再 `daemon-reload` 即可彻底关闭，不影响选项 7。
- 运行时自动关闭内核 SysRq 魔术键。

## 终端

- 启动即进入 cbreak 模式（逐字符读取、自行回显），支持退格。
- 终端类型自动探测，优先 `linux`，回退 `vt100`。
- 顶部标题读取 `/etc/cetnos-release`，缺省 `SSCLOUD CentOS7-TTY`。

## 目录结构

```
.
├── src/main.go                              全部源码（单文件）
├── src/cetnos-tty                           编译产物（Linux amd64 静态二进制）
├── deploy/                                  与上方脚本等价的 unit/脚本参考
│   ├── cetnos-tty@.service
│   ├── cetnos-root@.service
│   ├── cetnos-root-shell
│   └── getty@tty1.service.d/override.conf
└── README.md
```

## 卸载

```bash
systemctl disable --now cetnos-tty@tty1.service
systemctl stop cetnos-root@tty1.service 2>/dev/null
rm -f /etc/systemd/system/cetnos-tty@.service
rm -f /etc/systemd/system/cetnos-root@.service
rm -f /usr/local/sbin/cetnos-root-shell
rm -rf /etc/systemd/system/getty@tty1.service.d
rm -f /usr/local/sbin/cetnos-tty
systemctl daemon-reload
```
