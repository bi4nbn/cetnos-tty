# MegaRAID 阵列卡管理工具对照表(Dell / 通用 LSI / HPE)

> 适用:本仓库产出的 CentOS 7.9 自动安装盘(当前 **Ver1.0.86**)
> 所有“实测”结论来自真机 `192.168.80.151`(CentOS 7.9.2009 / kernel `5.4.119-19-0009.3` / VMware 虚机)
> 未实测的部分均已标注,不要当成已验证事实使用。

---

## 1. 厂商 → 工具

| 厂商 | 工具 | 说明 |
|---|---|---|
| Dell | `perccli` | Dell 专属,**本质是 storcli 的 OEM 版**,语法与 storcli 基本一致 |
| Lenovo / 浪潮 / 华为 / 超微 / Fujitsu / NEC / Oracle | `storcli` | 通用工具,覆盖 LSI/Broadcom MegaRAID 及各家 OEM 贴牌 |
| HPE | `ssacli` | **HPE 专属,Smart Array 架构与 LSI 完全不兼容**,只能用这个 |
| Microchip / Adaptec(SmartRAID 等) | `arcconf` | 又是另一套;**本盘尚未包含**(需要 RPM) |

> `perccli` 与 `storcli` 属于同一套 LSI/Broadcom 代码,命令语法互通;
> 装 `storcli` 也能管 Dell 的 PERC,`perccli` 保留只是为了与 Dell 文档/现场习惯一致。

---

## 2. 盘里装了什么(Ver1.0.86 起)

载荷位置:ISO 内 `files/tmp/*.rpm`(安装时由 `%post --nochroot` 拷到目标机 `/tmp/`,装完即删)

| 包 | 大小 | 声明依赖 | 离线可装 | 实测版本输出 |
|---|---|---|---|---|
| `perccli-007.1623.0000.0000-1.noarch` | 2.1 MB | 仅 `/bin/sh` + `rpmlib(*)` | ✅ | `PercCli SAS Customization Utility Ver 007.1623.0000.0000 May 17, 2021` |
| `storcli-007.3811.0000-1.noarch` | 2.3 MB | 仅 `/bin/sh` + `rpmlib(*)` | ✅ | `StorCli SAS Customization Utility Ver 007.3811.0000 July 22, 2026` |
| `ssacli-5.10-44.0.x86_64` | 16.4 MB | `libc/libdl/libm/libpthread/librt/libstdc++/libgcc_s`(基础 glibc,**实测全部已满足**) | ✅ | `SSACLI Version: 5.10.44.0 2021-01-20` |

目标机安装后占用:`perccli` 7.4 MB · `storcli` 8.5 MB · `ssacli` **42.5 MB**(合计约 58 MB)。

---

## 3. 落地路径与软链(核心)

| 工具 | 包内目录 | 包内文件 | 实测落盘情况 | PATH 入口 | 我们在 `%post` 里补的软链 |
|---|---|---|---|---|---|
| `perccli` | `/opt/MegaRAID/perccli/` | `perccli`、`perccli64` | 实测**两个都在** | ❌ 无 | `/opt/MegaRAID/perccli/perccli` → `perccli64`(无害补链)<br>`/usr/local/bin/perccli64` → `/opt/MegaRAID/perccli/perccli64` |
| `storcli` | `/opt/MegaRAID/storcli/` | `storcli`、`storcli64` | 实测**只有 `storcli64`**,`storcli` 缺失 | ❌ 无 | `/opt/MegaRAID/storcli/storcli` → `storcli64`(**必须补**)<br>`/usr/local/bin/storcli64` → `/opt/MegaRAID/storcli/storcli64` |
| `ssacli` | `/opt/smartstorageadmin/ssacli/bin/` | `ssacli`、`ssascripting`、`mklocks.sh`、`rmstr`、license、txt | 正常 | ✅ `/usr/sbin/ssacli`(包自带软链) | 无需处理 |

补充事实(实测):
- `ssacli` 还会写 `/usr/sbin/ssascripting`、`/usr/share/smartupdate/ssacli/component.xml`、`/usr/man/man8/ssacli.8.gz`;
- `perccli` / `storcli` **默认都不在 PATH**,必须全路径或依赖我们建的 `/usr/local/bin` 软链;
- 包清单里的 `perccli` / `storcli`(不带 64)在磁盘上可能缺失,`rpm -V` 会报 `missing …`;
  补了软链后功能正常,但 `rpm -V` 仍可能对“软链 vs 文件”报差异(仅提示,无功能影响)。

---

## 4. 常用命令对照

### Dell `perccli` / 通用 `storcli`(语法互通)

```bash
perccli64 -v                       # 版本
perccli64 show                     # 控制器数量与概要(Status = Success 即正常)
perccli64 show ctrlcount           # 控制器个数
perccli64 /c0 show                 # 0 号控制器详情
perccli64 /c0 show all             # 控制器 + 全部子对象(VD/PD/BBU…)
perccli64 /c0/vall show            # 所有虚拟盘(VD)
perccli64 /c0/eall/sall show all   # 所有物理盘(PD)
perccli64 /c0/v0 show all          # 指定 VD
perccli64 /c0/e252/s0 show all     # 指定 PD(enclosure 252 = 直连)
perccli64 /c0 show termlog         # 控制器日志
```
把 `perccli64` 换成 `storcli64` 即可(两者参数一致)。

### HPE `ssacli`(语法完全不同)

```bash
ssacli version                                  # 版本
ssacli ctrl all show                            # 所有控制器
ssacli ctrl all show config                     # 控制器 + 配置概览
ssacli ctrl slot=0 show detail                  # 指定槽位详情
ssacli ctrl slot=0 ld all show                  # 逻辑盘
ssacli ctrl slot=0 pd all show                  # 物理盘
ssacli ctrl slot=0 pd all show detail           # 物理盘详情
```

> ⚠️ 虚机 / 无对应阵列卡的机器上:`perccli64 show` 返回 `Number of Controllers = 0`(Status 仍是 Success);
> `/c0 show` 返回 `Controller 0 not found`。**这是正常现象,不代表工具坏了。**

---

## 5. 离线安装与签名(为什么不用安装源直接装)

- 三个包都是**厂商签名**,盘里没有它们的公钥:
  - `perccli` → Broadcom Key ID `53bd6b28cb529165`
  - `ssacli` → HPE Key ID `26c2b797`
  - `storcli` → Broadcom(其发行目录里自带 `pubKey.asc`)
- 本盘的安装源是 **`gpgcheck=1`**,所以**不能直接把这三个包塞进 `Packages/` 用 `@包名` 安装**(会被校验拦下);
- 采用的办法(已实测可行):**放 `files/tmp/` 载荷 → `%post` 里 `rpm -ivh` 本地文件**,rpm 对本地文件默认不校签;
- 如果将来要求“GPG 干净”,可以同时把对应公钥放进 `etc/pki/rpm-gpg/` 并在 `%post` 里 `rpm --import` 后再从源安装。

---

## 6. ISO 里的 `%post` 做了什么

`isoroot/isolinux/ks.cfg`(由 `rebuild.sh` 生成 BIOS/UEFI 两份),日志落在目标机 `/var/log/ks-post-raidtools.log`:

```sh
%post --log=/var/log/ks-post-raidtools.log
installed=""
for R in /tmp/perccli-*.rpm /tmp/storcli-*.rpm /tmp/ssacli-*.rpm; do
    [ -f "$R" ] || continue          # 有就装,没有就跳过(缺包不会导致安装失败)
    rpm -ivh "$R" 2>&1 | tail -2
    installed="$installed $(basename "$R")"
done
# 补包清单里可能缺的“不带 64”名字
[ -x /opt/MegaRAID/perccli/perccli64 ] && ln -sf /opt/MegaRAID/perccli/perccli64 /opt/MegaRAID/perccli/perccli
[ -x /opt/MegaRAID/storcli/storcli64 ] && ln -sf /opt/MegaRAID/storcli/storcli64 /opt/MegaRAID/storcli/storcli
# 给现场留 PATH 入口(这两个包默认不进 PATH)
[ -x /opt/MegaRAID/perccli/perccli64 ] && ln -sf /opt/MegaRAID/perccli/perccli64 /usr/local/bin/perccli64
[ -x /opt/MegaRAID/storcli/storcli64 ] && ln -sf /opt/MegaRAID/storcli/storcli64 /usr/local/bin/storcli64
# 打印安装结果与完整性,然后删掉 RPM(不在目标机留垃圾)
for p in perccli storcli ssacli; do
    rpm -q "$p" >/dev/null 2>&1 && printf ">>> %-8s %s | rpm -V: %s\n" "$p" "$(rpm -q "$p")" "$(rpm -V "$p" 2>&1 | tr '\n' ' ')"
done
/opt/MegaRAID/perccli/perccli64 -v | sed -n 2p
/opt/MegaRAID/storcli/storcli64 -v | sed -n 2p
/usr/sbin/ssacli version | sed -n 2p
rm -f /tmp/perccli-*.rpm /tmp/storcli-*.rpm /tmp/ssacli-*.rpm
%end
```

---

## 7. 装机后 30 秒自检

```bash
rpm -q perccli storcli ssacli                # 三个都应有版本输出
ls -l /opt/MegaRAID/perccli/ /opt/MegaRAID/storcli/   # 每目录应有 64 版 + 不带 64 的软链
ls -l /usr/local/bin/perccli64 /usr/local/bin/storcli64
ls /tmp/*.rpm 2>/dev/null                    # 应为空(装完已删)
sed -n '1,40p' /var/log/ks-post-raidtools.log # 三个版本号与 rpm -V 结果
perccli64 show ; storcli64 show ; ssacli ctrl all show   # 有卡则显示控制器,无卡则 Controllers = 0
```

---

## 8. 已知缺口 / 待办

| 项 | 说明 |
|---|---|
| `arcconf`(Microchip/Adaptec) | **尚未包含**;若现场有 SmartRAID/Adaptec 卡,需提供 RPM,按本文第 5、6 节同一套办法加入 |
| `ssacli` 体积 | 安装后 42.5 MB,是三者中最大的;若现场没有 HPE 机器,可从 ISO 撤掉以省空间 |
| `megacli`(老卡) | 老一代 MegaRAID 用 `megacli`/`MegaCli64`,本盘未包含;新机器一般不需要 |
| 真机验证 | 目前只在**虚机**上验证了“工具能运行”;**在带 PERC / Smart Array 的物理机上还没跑过真实查询**,建议上线前找一台实机跑 `show` / `/c0 show all` 确认 |
