# cetnos-tty

**SSCLOUD CentOS7-TTY** —— 面向无显示器（headless）Linux 设备的物理控制台菜单。

单文件 Go 静态二进制，开机直接运行在物理终端 `tty1` 上，**无需登录**，按键即可配置网络、Ping、查看磁盘、重启或关机。需要进入系统时按 `7` 切换到标准 getty 登录界面（需账号密码），`logout` 后自动回到菜单；另有一个隐藏入口 `666` 可直接免密进入 root shell（见下文）。

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
| `666` | **隐藏入口**：直接免密进入 root shell（菜单不显示，仅 VT 控制台可用） |

- 任意输入框中按 `Ctrl+C` 或 `ESC` 直接返回主菜单。
- 系统盘通过 `/proc/mounts` + `/sys/block` 回溯识别（含 LVM/dm），不依赖 `lsblk`。

## 编译

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o cetnos-tty .
```

纯静态编译，无任何外部依赖（不依赖 C 库、网络工具），目标平台 CentOS 7+ x86_64，体积约 2.4MB。

## 部署

部署所需的 systemd 单元与脚本均在仓库 `deploy/` 目录，可直接使用。

### 1. 安装二进制

```bash
install -m 0700 cetnos-tty /usr/local/sbin/cetnos-tty
```

### 2. 安装菜单服务（开机自启）

```bash
install -m 0644 deploy/cetnos-tty@.service /etc/systemd/system/cetnos-tty@.service
```

> `cetnos-tty@.service` 固定运行在 `tty1`，并通过 `Conflicts=` 与 `getty@tty1`、`cetnos-root@tty1` 互斥，三者不会同时抢占终端。

### 3. 配置 getty 退出后自动回到菜单（选项 7）

通过 drop-in 覆盖，使 `getty` 在用户 `logout` 退出后自动拉起 cetnos-tty：

```bash
mkdir -p /etc/systemd/system/getty@tty1.service.d
install -m 0644 deploy/getty@tty1.service.d/override.conf \
  /etc/systemd/system/getty@tty1.service.d/override.conf
```

### 4. 安装隐藏 666 免密入口（可选）

安装 root 自动登录的模板服务与登录包装脚本：

```bash
install -m 0644 deploy/cetnos-root@.service /etc/systemd/system/cetnos-root@.service
install -m 0700 deploy/cetnos-root-shell  /usr/local/sbin/cetnos-root-shell
```

该服务不需要 `enable`，由菜单在输入 `666` 时按需 `systemctl start cetnos-root@tty1` 拉起。

### 5. 关闭 SysRq 并启用

```bash
echo 'kernel.sysrq = 0' > /etc/sysctl.d/99-cetnos-tty.conf
sysctl -w kernel.sysrq=0

systemctl daemon-reload
systemctl enable --now cetnos-tty@tty1.service
```

## 工作原理（菜单 ↔ 系统切换）

开机后 systemd 在 `tty1` 上运行 cetnos-tty，它与系统自带的 `getty@tty1`、隐藏的 `cetnos-root@tty1` 通过 `Conflicts=` 互斥。**冲突只在 cetnos-tty 一侧声明**，这样在 getty/root shell 退出后用 `ExecStopPost` 拉起菜单时不会形成双向冲突死锁。

```
开机
  └─► cetnos-tty 菜单（tty1，无需登录）
        ├─ 按 7 ─► getty@tty1（Conflicts 停止菜单）
        │           └─► 账号密码登录 ─► shell ─► logout ─► getty 退出
        │                 ExecStopPost start cetnos-tty ─► 回到菜单
        │
        └─ 输入 666 ─► cetnos-root@tty1（agetty --autologin root，
                          --login-program 为 cetnos-root-shell）
                          └─► 免密 root shell ─► exit ─► wrapper 退出
                                ExecStopPost start cetnos-tty ─► 回到菜单
```

`cetnos-root-shell` 的关键作用：`agetty` 会 `exec()` 替换为 `--login-program`，脚本运行一次登录 shell `/bin/bash -l`，shell 退出后脚本结束 → 模板服务停止 → `ExecStopPost` 重新拉起菜单，因此**只登录一次、不会循环自动登录**。

## 安全

- 二进制权限 `0700 root:root`，仅 root 可执行。
- 程序必须以 root 运行（配置网络、重启/关机需要权限）。
- 按 `7` 进入系统仍需经过 getty 的账号密码校验。
- `666` 是**隐藏的免密 root 入口**：物理控制台任何人输入 `666` 即可获得 root 权限，菜单上不显示该选项。请仅在受信任的物理环境、且确认控制台物理访问受控的设备上启用（即部署第 4 步）；不需要时删除 `cetnos-root@.service` 与 `cetnos-root-shell` 即可彻底关闭，不影响选项 7。
- 菜单本身不提供 shell，仅提供受限的网络/磁盘/电源操作。
- 运行时自动关闭内核 SysRq 魔术键。

## 终端

- 启动后自动进入 cbreak 模式（逐字符读取、程序自行回显），支持退格。
- 终端类型自动探测，优先 `linux`，回退 `vt100`。
- 顶部标题读取 `/etc/cetnos-release`，缺省显示 `SSCLOUD CentOS7-TTY`。

## 目录结构

```
.
├── src/main.go                              全部源码（单文件）
├── src/cetnos-tty                           编译产物（Linux amd64 静态二进制）
├── deploy/
│   ├── cetnos-tty@.service                  菜单服务（tty1，开机自启）
│   ├── cetnos-root@.service                 隐藏 666 免密 root 模板服务
│   ├── cetnos-root-shell                    666 登录包装脚本（只登录一次）
│   └── getty@tty1.service.d/override.conf   getty 退出后自动回菜单
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
