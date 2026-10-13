# cetnos-tty

**SSCLOUD CentOS7-TTY** —— 面向无显示器（headless）Linux 设备的物理控制台菜单。

单文件 Go 静态二进制，开机直接运行在物理终端 `tty1` 上，**无需登录**，按键即可配置网络、Ping、查看磁盘、重启或关机。需要进入系统时按 `7` 切换到标准 getty 登录界面（需账号密码），`logout` 后自动回到菜单。

## 菜单功能

| 按键 | 功能 |
|------|------|
| 1 | 静态 IP 配置（选网卡，交互式输入 IP/前缀/网关/DNS，自动备份原 ifcfg） |
| 2 | DHCP 配置（选网卡，写入 `BOOTPROTO=dhcp`，自动备份） |
| 3 | 数据盘列表（自动排除系统盘，显示容量/类型/总线/型号/序列号/挂载点） |
| 4 | Ping 测试（ping 223.6.6.6，4 包） |
| 5 | 重启（需二次确认） |
| 6 | 关机（需二次确认） |
| 7 | 退出菜单，进入系统登录（getty，需账号密码） |

- 任意输入框中按 `Ctrl+C` 或 `ESC` 直接返回主菜单。
- 系统盘通过 `/proc/mounts` + `/sys/block` 回溯识别（含 LVM/dm），不依赖 `lsblk`。

## 编译

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o cetnos-tty .
```

纯静态编译，无任何外部依赖（不依赖 C 库、网络工具），目标平台 CentOS 7+ x86_64，体积约 2.4MB。

## 部署

### 1. 安装二进制

```bash
install -m 0700 cetnos-tty /usr/local/sbin/cetnos-tty
```

### 2. 安装 systemd 服务（菜单）

```bash
cat > /etc/systemd/system/cetnos-tty@.service <<'EOF'
[Unit]
Description=SSCLOUD CentOS7-TTY Console
After=systemd-user-sessions.service plymouth-quit-wait.service
Before=getty.target
Conflicts=getty@%i.service

[Service]
ExecStart=/usr/local/sbin/cetnos-tty
Restart=always
RestartSec=0
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/%i
KillMode=process

[Install]
WantedBy=multi-user.target
EOF
```

### 3. 配置 getty 退出后自动回到菜单

通过 drop-in 覆盖，使 `getty` 在用户 `logout` 退出后自动拉起 cetnos-tty：

```bash
mkdir -p /etc/systemd/system/getty@tty1.service.d
cat > /etc/systemd/system/getty@tty1.service.d/cetnos-tty.conf <<'EOF'
[Service]
ExecStopPost=/usr/bin/systemctl start cetnos-tty@tty1.service
EOF
```

### 4. 关闭 SysRq 并启用

```bash
echo 'kernel.sysrq = 0' > /etc/sysctl.d/99-cetnos-tty.conf
sysctl -w kernel.sysrq=0

systemctl daemon-reload
systemctl enable --now cetnos-tty@tty1.service
```

## 工作原理（菜单 ↔ 登录切换）

开机后 systemd 在 `tty1` 上运行 cetnos-tty，与系统自带的 `getty@tty1` 通过 `Conflicts=` 互斥，二者不会同时抢占终端：

```
开机
  └─► cetnos-tty 菜单（tty1，无需登录）
          按 7  ──► systemctl start getty@tty1
                     （Conflicts 自动停止 cetnos-tty）
                        └─► getty 登录（需账号密码）
                               └─► shell
                                    └─► logout
                                         └─► getty 退出
              ExecStopPost 自动 start cetnos-tty  ◄──┘
                        └─► 回到菜单
```

## 安全

- 二进制权限 `0700 root:root`，仅 root 可执行。
- 程序必须以 root 运行（配置网络、重启/关机需要权限）。
- 按 `7` 进入系统仍需经过 getty 的账号密码校验，**不存在免密 shell**。
- 菜单本身不提供 shell，仅提供受限的网络/磁盘/电源操作。
- 运行时自动关闭内核 SysRq 魔术键。

## 终端

- 启动后自动进入 cbreak 模式（逐字符读取、程序自行回显），支持退格。
- 终端类型自动探测，优先 `linux`，回退 `vt100`。
- 顶部标题读取 `/etc/cetnos-release`，缺省显示 `SSCLOUD CentOS7-TTY`。

## 目录结构

```
.
├── src/main.go        全部源码（单文件）
├── cetnos-tty         编译产物（Linux amd64 静态二进制）
└── README.md
```

## 卸载

```bash
systemctl disable --now cetnos-tty@tty1.service
rm -f /etc/systemd/system/cetnos-tty@.service
rm -rf /etc/systemd/system/getty@tty1.service.d
rm -f /usr/local/sbin/cetnos-tty
systemctl daemon-reload
```
