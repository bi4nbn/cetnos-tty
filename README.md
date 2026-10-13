# cetnos-tty

无头 Linux 设备控制台菜单。单文件 Go 静态二进制，直接跑在 tty1 上，无需登录，按键即可配网、Ping、查看磁盘、重启或关机。

## 功能

| 按键 | 功能 |
|------|------|
| 1 | 静态 IP 配置（选择网卡，交互式输入 IP/前缀/网关，自动备份原 ifcfg） |
| 2 | DHCP 配置（选择网卡，写入 BOOTPROTO=dhcp） |
| 3 | Ping 测试（223.6.6.6，4 包） |
| 4 | 数据盘列表（自动排除系统盘，显示类型/总线/型号/序列号/挂载点） |
| 5 | 重启（需确认 y） |
| 6 | 关机（需确认 y） |
| 7 | 退出，交还 getty 登录提示 |

## 编译

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o cetnos-tty .
```

静态编译，无外部依赖，目标平台 CentOS 7+ x86_64，体积约 2.4MB。

## 部署

```bash
# 二进制
install -m 0700 cetnos-tty /usr/local/sbin/cetnos-tty

# 666 启动器
cat > /usr/local/bin/666 <<'EOF'
#!/bin/bash
exec /usr/local/sbin/cetnos-tty start
EOF
chmod 750 /usr/local/bin/666

# systemd 服务模板
cat > /etc/systemd/system/cetnos-tty@.service <<'EOF'
[Unit]
Description=Console menu (%I)
Conflicts=getty@%i.service

[Service]
Type=idle
ExecStart=/usr/local/sbin/cetnos-tty
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/%I
TTYReset=yes
Restart=on-failure

[Install]
WantedBy=getty.target
EOF

# sysrq 关闭
echo 'kernel.sysrq = 0' > /etc/sysctl.d/99-cetnos-tty.conf
sysctl -w kernel.sysrq=0

# 启用并启动
systemctl daemon-reload
systemctl enable --now cetnos-tty@tty1.service
```

## 权限

- 二进制 `0700 root:root`，仅 root 可执行
- 运行时自动进入 cbreak 模式（逐字符读取、本地回显），支持退格和 Ctrl-C 回主菜单
- 系统盘通过 `/proc/mounts` + `/sys/block` 回溯识别，不依赖 lsblk

## 文件

```
src/main.go   全部源码（单文件）
```
