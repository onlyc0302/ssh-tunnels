# tunnels 0.2.0 · Linux / WSL 使用说明

终端中查看 SSH 本地端口映射、手动扫描已有连接、启停托管连接，以及断线重连。无需安装 Go，不依赖 kitty；WSL 可在 Windows Terminal 的 Ubuntu 标签页运行。

## 1. 选择发行包

在 Linux / WSL 终端执行 `uname -m`：

| 输出 | 发行包 |
| --- | --- |
| `x86_64` | `kitty-tunnels_0.2.0_linux_amd64.tar.gz`（多数 Intel / AMD Windows 电脑） |
| `aarch64` / `arm64` | `kitty-tunnels_0.2.0_linux_arm64.tar.gz` |

这些是 Linux ELF 二进制，不是 Windows `.exe`。请在 WSL 中解压并运行，不能直接在 PowerShell 中执行二进制。

## 2. 安装依赖

建议 Ubuntu 22.04+、Debian 12+ 或 WSL 2 的 Ubuntu 22.04+。在发行版内部执行：

```sh
sudo apt-get update
sudo apt-get install -y openssh-client procps lsof
```

需要 Linux OpenSSH 客户端（建议 8.9+）、procps 的 `ps` 与 `lsof`。程序本身为 `CGO_ENABLED=0` 静态构建，无需 Go 运行时或特定 glibc 版本；上述系统工具仍需安装。其他发行版可通过自己的包管理器安装同类依赖，但未逐一验证。

## 3. 校验、解压与运行

把发行包与 `SHA256SUMS` 放在同一目录。假设下载的是 amd64 包：

```sh
sha256sum --check --ignore-missing SHA256SUMS
mkdir -p ~/apps
tar -xzf kitty-tunnels_0.2.0_linux_amd64.tar.gz -C ~/apps
cd ~/apps/kitty-tunnels_0.2.0_linux_amd64
./tunnels doctor
./tunnels
```

ARM64 用户把以上文件名和目录名中的 `amd64` 替换为 `arm64`。

WSL 建议安装到 Linux 文件系统的 `~/apps`，不要把运行状态目录放在 `/mnt/c`。解压后如果可执行权限丢失，执行 `chmod +x ./tunnels`。程序应以普通用户运行，无需 `sudo ./tunnels`。

校验和可检测传输损坏；请从可信同事处一并获取校验文件。

## 4. 发现现有隧道

先在同一个 Linux / WSL 发行版、同一个用户下启动：

```sh
ssh -qTfnN root@your-server -L 3308:127.0.0.1:3306
```

再运行 `./tunnels`，或在面板按大写 `R`（Shift+r）。刷新不会中断连接。

| 按键 | 功能 |
| --- | --- |
| `R` | 重新扫描已有 SSH 隧道 |
| `a` | 粘贴 SSH 命令，新建未启动的托管配置 |
| 空格 | 启动 / 停止 |
| `r` | 重启选中的托管连接 |
| `i` | 外部隧道纳入托管；确认后重建，会短暂断开 |
| Enter | 查看完整映射与进程详情 |
| `l` | 查看托管日志 |
| `/` | 搜索；Esc 清除 |
| `?` | 完整帮助 |
| `q` | 退出面板，后台连接继续运行 |

托管自动重连使用密钥 / SSH agent。首次主机指纹确认和密钥解锁，请先通过普通 `ssh` / `ssh-add` 完成。后台暂不支持密码或 OTP 交互。接管前会检查是否能无人值守认证，失败时保留原连接。

## 5. WSL 的边界

- 隧道必须由同一 WSL 发行版中的 Linux `ssh` 启动，可通过 `command -v ssh` 检查。不要调用 Windows `ssh.exe`。
- 不能用此 Linux 版扫描 PowerShell/CMD 中启动的 Windows SSH，或其他 WSL 发行版里的进程。
- 关闭面板后后台可继续运行；`wsl --shutdown`、发行版终止、Windows 重启会结束其中的后台及隧道。再次打开工具时恢复期望运行的托管连接。
- Windows 上的数据库客户端通常可以连接 `127.0.0.1:3308` 访问 WSL 中的转发端口，仍取决于 WSL 的 localhost 转发、网络模式、防火墙及端口占用情况。此版本没有修改 Windows 网络或防火墙设置。
- 如果 Windows 客户端连不上，先在 WSL 内确认隧道与目标服务可用，再检查 Windows 访问路径。列表“已建立”不代表数据库协议检测成功。

参考：[Microsoft WSL 网络说明](https://learn.microsoft.com/en-us/windows/wsl/networking)。

## 6. 配置与后台

Linux 默认目录：`~/.config/kitty-tunnels/`；若设置了绝对路径的 `XDG_CONFIG_HOME`，则使用 `$XDG_CONFIG_HOME/kitty-tunnels/`。配置、日志、socket 和进程状态在这个目录内；`TUNNELS_HOME` 可以覆盖整个目录。

```sh
./tunnels paths
./tunnels list
./tunnels reload
./tunnels logs <ID或唯一名称>
./tunnels stop <ID或唯一名称>
./tunnels daemon-stop
```

`daemon-stop` 停止后台及托管连接，但保留期望运行状态；下次打开工具会恢复。希望某条连接保持关闭，应先用 `stop`。未安装 systemd 服务或开机启动项。

升级前先停止需要保持关闭的托管连接，退出面板，再替换二进制。有运行中托管连接的旧后台不会被新客户端自动中断。

## 7. 验证范围

本次实际发行包在 Linux 容器中通过 21 个顶层测试：ARM64 原生运行，amd64 经架构转译运行。详见包内 `RELEASE-NOTES.md`。真实 OpenSSH 测试覆盖进程发现、接管、数据转发、重连、停止、认证失败和独立后台生命周期。**Linux 容器验证不等于 Windows WSL 实机验证**；WSL 终端交互与 Windows 客户端到 WSL 的端口访问仍需在同事电脑上确认。

第三方依赖许可证见包内 `THIRD_PARTY_NOTICES.txt`。完整功能说明见 `README.md`。
