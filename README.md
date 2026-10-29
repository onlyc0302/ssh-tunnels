# ssh-tunnels

在终端中查看和管理 SSH 本地端口转发的工具，支持 macOS 与 Linux / WSL，底层使用系统 OpenSSH。

命令名为 `tunnels`。现有发行包与配置目录继续沿用 `kitty-tunnels`，以保持兼容。

## 从源码开始

需要 Go 1.24+、OpenSSH、procps（Linux）和 lsof。Ubuntu / Debian / WSL 可先执行 `sudo apt-get install openssh-client procps lsof`。

```sh
git clone https://github.com/onlyc0302/ssh-tunnels.git
cd ssh-tunnels
./build.sh
./bin/tunnels doctor
./bin/tunnels
```

源码仓库不包含本机二进制、连接配置、密钥或日志。参与开发和提交修改请看 [CONTRIBUTING.md](CONTRIBUTING.md)。

### 0.2.0 · Linux / WSL 发行版

- 提供 Linux amd64 和 arm64 静态二进制包；不要求使用者安装 Go。
- Linux 状态目录改为 `~/.config/kitty-tunnels`，支持绝对路径的 `XDG_CONFIG_HOME`。
- Linux 从 PATH 查找 `ssh`、`ps`、`lsof`；macOS 保持系统工具路径。
- 增加 `tunnels doctor`，只读检查依赖和进程读取能力，不连接远端。
- 构建发行包：`./build.sh release-linux`；结果位于 `dist/`，含使用说明、依赖许可与 SHA-256 校验文件。
- 安装与 WSL 边界详见 [README-LINUX.md](README-LINUX.md)。

### 0.1.1 更新

- 修复中文 kitty 环境下扫描为空：扫描用的 `ps` 固定为 `LC_ALL=C`，避免中文启动时间格式导致身份校验失败。
- 读取进程详情失败但发现实际监听时，保留只读条目并显示原因，不再静默隐藏。
- 列表改为对齐表格，自适应终端宽度；选中项详情、操作提示固定在底部。`?` 查看完整快捷键，`Esc` 清除搜索。
- 客户端检查后台版本。没有运行中或期望运行的托管连接时可自动更新空闲后台；有托管连接则明确提示，避免升级中断连接。外部 SSH 不受后台更新影响。

## 运行

在项目目录构建后运行：

```sh
./bin/tunnels
```

首次打开会启动用户级后台进程并扫描当前本机用户的 SSH 隧道。无需管理员权限。退出面板或关闭 kitty，托管隧道继续运行。

后台只在打开工具时按需启动，**初版未安装登录启动项**。注销或重启电脑后，再次打开工具会恢复配置中期望运行的连接。`tunnels daemon-stop` 停止后台及托管连接；下次启动恢复期望运行状态。若不想恢复某连接，先在面板里停止该连接。

## 快捷键

| 按键 | 操作 |
| --- | --- |
| `R` / Shift+r | 重新扫描外部 SSH 进程和监听端口；不会重启或接管连接 |
| `r` | 重启选中的托管连接 |
| 空格 | 托管连接启停；外部连接停止前显示影响范围并确认 |
| `a` | 粘贴单行 SSH 命令，设置名称和分组，保存为未启动配置 |
| `i` | 外部连接纳入托管，确认后停止原进程并重建 |
| Enter | 查看详情和最近错误 |
| `l` | 查看托管连接日志 |
| `d` | 删除已经停止的托管配置 |
| `/` | 按名称、分组、主机、映射搜索 |
| `q` | 退出面板，保留后台连接 |

添加表单使用 Tab / Shift+Tab 切换字段，Ctrl+U 清空当前字段。输入框支持粘贴和尾部删除；初版没有完整的行内光标编辑器。

## 已有隧道

例如先运行：

```sh
ssh -qTfnN root@your-host -L 3308:127.0.0.1:3306
```

然后打开工具，或在已打开的面板里按 `R`，即可发现该进程及实际监听的端口。`root@your-host` 是远端用户；发现范围按本机进程所属用户判断。

外部列表只在首次打开与 `R` 时刷新。托管状态仍每秒刷新，自动重连不依赖界面。一个 SSH 进程的多个映射显示在同一行；停止整个进程会影响全部映射和可能共享的会话。

外部连接的 `监听中` 只表示本地存在监听 socket，不能证明远端数据库可用。如果映射来自 SSH 配置文件或后续动态增加，可能只能发现监听端口，无法可靠还原目标；此时显示“目标待确认”，仅允许查看。初版不根据当前配置文件猜测历史进程的启动配置。

自动接管只接受参数完整、具有 `-N`、不含未支持选项的 TCP `-L` 进程。接管前先建立一个无转发、无远端命令的临时连接，检查密钥/agent 是否能完成无人值守认证；失败时保留原隧道运行。停止前再次验证 PID、启动时间、参数摘要与用户。接管会短暂断开原连接，数据库客户端需重新连接。配置先保存为停止状态，即使认证或停止原进程失败也保留配置供检查；不要在原端口仍被占用时重复启动。

## 新建托管连接

按 `a` 粘贴现有命令，或使用 CLI：

```sh
./bin/tunnels add --name test-mysql --group test \
  --command 'ssh -qTfnN root@your-host -L 3308:127.0.0.1:3306'
./bin/tunnels start test-mysql
./bin/tunnels list
./bin/tunnels logs test-mysql
./bin/tunnels stop test-mysql
```

名称不唯一时使用 `list` 中的 ID。`list --json` 和 `reload --json` 提供机器可读输出。`list` 读取快照；`reload` 才扫描外部进程。

支持 `-L` 的组合/独立写法、IPv4、方括号 IPv6、多个映射、`-p`、`-i`、`-J`、`-l`、`-F` 和有限的 `-o` 连接选项。不执行粘贴命令中的 shell 管道、重定向或命令替换。未知选项会明确拒绝自动导入。

工具调用系统 OpenSSH（macOS 为 `/usr/bin/ssh`，Linux 从 PATH 查找 `ssh`），可以使用主机别名、密钥、SSH agent 和跳板机。SSH 配置文件本身仍由 OpenSSH 读取，包括原有的 ProxyCommand / Match exec 等行为；只应使用自己信任的 SSH 配置。配置中额外的 LocalForward 也可能生效，建议为工具使用没有额外转发的主机条目。

## 自动重连与认证

- 托管连接使用独立控制 socket，不复用已有交互连接。
- 每 15 秒保活，连续 3 次无响应由 SSH 退出；后台以约 1、2、4、8、16、30 秒加随机抖动重试。
- 主动停止会保存“期望停止”，不会自动拉起。
- 认证失败、主机密钥校验失败、端口冲突进入“需要处理”，不无限重试。处理后按 `r` 重试。
- 网络切换/睡眠唤醒后，依靠 SSH 保活检测和重试恢复，检测存在等待时间，不保证立即恢复。
- 第一版后台使用 `BatchMode=yes`，**自动连接需密钥或已解锁的 SSH agent**。不保存密码，不提供密码/OTP 输入桥接。
- 首次连接陌生服务器，请先用普通 `ssh` 确认指纹；加密私钥请先 `ssh-add`。工具不会自动接受未知或变化的主机密钥。
- 客户端会把可用的 SSH_AUTH_SOCK 告知后台，不把 agent socket 路径写入连接配置。若 agent 地址变化，重新打开面板后按 `r` 重试。
- `已建立` 表示 SSH 控制连接与本地监听已检查；没有执行 MySQL/Redis 协议探测或业务 SQL。

后台异常退出后，重新启动时会检查它记录的专属 SSH 子进程，验证身份后结束旧进程并重建期望运行的连接。恢复会造成短暂断开。初版不会自动重启崩溃的后台服务；再次打开面板会启动它。

## 配置和日志

macOS 默认位于下列目录；Linux 默认使用 `~/.config/kitty-tunnels/`，支持绝对路径的 `XDG_CONFIG_HOME`：

```text
~/Library/Application Support/kitty-tunnels/
  config.json           连接定义与期望状态
  api.sock              当前用户专用本地通信 socket
  daemon.lock           后台单实例锁
  <id>.sock             独立 SSH 控制 socket
  <id>.pid.json         崩溃恢复使用的进程身份
  logs/<id>.log         最近 SSH 错误与重连记录
```

目录权限 0700，配置、日志与通信 socket 权限 0600。连接日志滚动保留当前及前一份，每份约 512 KiB。原始 SSH 错误可能包含用户名、主机名与本机路径，分享前请检查。

使用 `./bin/tunnels paths` 查看实际路径。测试可通过 `TUNNELS_HOME=/tmp/my-tunnels` 隔离状态；请使用短路径以满足 macOS Unix socket 长度限制。不要在后台运行时直接编辑 config.json，初版不提供配置热重载；`R` 只扫描进程。

## kitty 快捷键（可选）

在自己的 kitty.conf 中添加以下一行，路径按本机项目位置调整：

```conf
map ctrl+shift+u launch --type=tab --tab-title 隧道 /absolute/path/to/ssh-tunnels/bin/tunnels
```

也可将 `--type=tab` 改为 `--type=overlay`。该快捷键不需要开启 kitty 的全局 remote control。本项目不会自动修改 kitty.conf 或 shell 配置。

## 构建与测试

需要 Go 1.24+、系统 OpenSSH 和 lsof。

```sh
./build.sh
./build.sh test
./build.sh vet
```

构建脚本从实际 Go 可执行文件推导 GOROOT，避免继承旧工具链路径。只为当前构建进程设置官方 GOPROXY / GOSUMDB，不修改全局 Go 配置；可以通过 TUNNELS_GOPROXY / TUNNELS_GOSUMDB 覆盖。

构建时先生成新的临时二进制，再原子替换 `bin/tunnels`。构建失败会保留原程序；已经运行的后台继续使用原进程，不会因为替换文件而停止。

### macOS 启动时显示 killed

`killed` 说明进程被系统终止，单凭这条信息不能判断原因。可先检查：

```sh
codesign --verify --verbose=4 ./bin/tunnels
xattr -l ./bin/tunnels
```

本机曾出现系统日志 `load code signature error 2`，同时文件带有来源为 WeChat 的 `com.apple.quarantine` 标记。磁盘签名检查通过并不代表系统会允许执行。对于本项目在本机维护的可信源码，可运行 `./build.sh` 重新构建，然后执行 `./bin/tunnels version` 和重新打开面板验证。

本地 Go 构建使用临时签名，不等于 Apple Developer ID 签名或公证。把二进制通过聊天软件分享给同事后，接收方仍可能遇到系统拦截；需要免去这类首次运行阻碍的 macOS 分发包，应另行准备 Developer ID 签名与 Apple 公证流程。

端到端测试启动仅监听回环地址的临时 SSH 服务与 echo 服务，生成临时密钥，用真实 OpenSSH 验证手动隧道发现、刷新不打断连接、接管、数据转发、断线重连、手动停止、端口冲突及认证失败。不连接真实数据库或 Redis。

## 初版边界

- macOS 与 Linux 通过独立的平台测试；Windows WSL 的终端与跨系统网络路径仍需实际用户环境确认。
- 不托管 `-R` / `-D` / Unix socket 转发。
- 外部进程无法完整还原时只查看，不自动结束或猜测配置。
- 接管前需确认整个 SSH 进程的影响范围；不支持无缝接管。
- 暂无密码/OTP 交互、登录启动项、连接配置编辑表单和数据库健康检查。
