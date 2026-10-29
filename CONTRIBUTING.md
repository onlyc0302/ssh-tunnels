# 参与开发

仓库：<https://github.com/onlyc0302/ssh-tunnels>。程序命令为 `tunnels`，配置目录和发行包名称暂时保留 `kitty-tunnels`。

## 获取代码和运行

需要 Go 1.24+、OpenSSH 和 lsof。Linux / WSL 需要 procps 提供的完整 `ps`，不能替换为 BusyBox ps。Windows 开发者目前应在 WSL 中构建、测试和运行。

```sh
git clone https://github.com/onlyc0302/ssh-tunnels.git
cd ssh-tunnels
./build.sh
./bin/tunnels doctor
./bin/tunnels
```

用独立状态目录调试，避免干扰日常托管连接：

```sh
TUNNELS_HOME=/tmp/tunnels-dev ./bin/tunnels
```

即使配置目录隔离，外部扫描仍能发现同一用户的 SSH 进程。调试时不要接管或停止工作中的真实连接；自动集成测试仅创建本机回环 SSH 服务和临时密钥。

## 提交修改

已有仓库写权限的同事，使用功能分支并提交 Pull Request：

```sh
git switch main
git pull --ff-only
git switch -c feature/your-change

# 修改代码后执行检查
./build.sh test
./build.sh vet

git add <本次修改的文件>
git commit -m "说明这次修改解决的问题"
git push -u origin feature/your-change
```

随后在 GitHub 发起合并到 `main` 的 Pull Request。没有写权限时，可在仓库允许的情况下 Fork 后发起 Pull Request，或者请仓库所有者授予协作权限。克隆地址本身不会授予推送权限。

提交前检查 `git diff --cached`，不要提交真实服务器地址、SSH 私钥、密码、日志或个人连接配置。`bin/`、`dist/`、`release-tests/` 是生成产物，已被忽略。

## 代码结构

| 文件 | 职责 |
| --- | --- |
| `main.go`、`api.go` | 命令行入口、前后台通信、后台生命周期 |
| `discovery.go`、`process_*.go` | 扫描 SSH、读取进程参数、验证进程身份 |
| `parser.go`、`model.go` | SSH 命令解析、数据结构、配置持久化 |
| `supervisor.go` | 托管 SSH 启停、认证预检、重连 |
| `platform.go` | macOS / Linux 工具定位、依赖诊断 |
| `ui.go`、`ui_view.go` | TUI 操作与布局 |
| `*_test.go` | 单元测试与本机 SSH 集成测试 |
| `scripts/`、`tools/notices/` | 发行包、测试镜像、许可证收集 |

修改时保留这些行为：

- `R` 只刷新外部隧道列表，不自动接管或重启。
- 用户主动停止后，自动重连不能把连接重新拉起。
- 停止外部进程前核对进程身份，并明确整个进程的影响范围。
- 接管前先验证无人值守认证；失败时保留原连接。
- 显示本地监听和 SSH 状态，不把它们当作数据库协议可用性证明。

## Linux / WSL 构建和测试

```sh
./build.sh release-linux
./build.sh test-binaries-linux
```

发行包位于 `dist/`。在真正的 Linux 环境或隔离容器中运行跨平台测试，不能把交叉编译成功当作运行验证。

`TestReleaseBinaryBackgroundLifecycle` 需要通过 `TUNNELS_TEST_BINARY` 指定实际构建的程序；未指定时会跳过。可先构建，再运行：

```sh
TUNNELS_TEST_BINARY="$PWD/bin/tunnels" ./build.sh test
```

修改版本时同步更新 `main.go`、发行说明和验证记录。包内的二进制校验值需要重新生成并验证，不能沿用旧记录。测试报告应说明平台、CPU 架构、是否经过架构转译，以及没有完成的 WSL 实机验证。
