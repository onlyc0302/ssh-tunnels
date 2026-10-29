# kitty-tunnels 0.2.0 · Linux / WSL

发行日期：2026-10-29（Asia/Shanghai）。

## 交付内容

- `kitty-tunnels_0.2.0_linux_amd64.tar.gz`：Linux x86-64，多数 Intel / AMD Windows 电脑的 WSL 使用此包。
- `kitty-tunnels_0.2.0_linux_arm64.tar.gz`：Linux ARM64。
- `SHA256SUMS`：两个压缩包的 SHA-256 校验值。

每个包包含 `tunnels`、Linux/WSL 安装说明、完整 README、本发布记录和第三方许可证。无需安装 Go。系统依赖是 OpenSSH、procps 和 lsof。

构建参数：`CGO_ENABLED=0 GOOS=linux`；amd64 使用 `GOAMD64=v1`，arm64 使用 `GOARM64=v8.0`；二进制为静态链接的 ELF，开启 `-trimpath` 与 `-ldflags='-s -w'`。

## 本次验证

| 环境 | 结果 |
| --- | --- |
| macOS ARM64 | 回归测试与 `go test -race`、`go vet` 通过；两个仅适用于 Linux/实际发行包的测试在此环境跳过 |
| Debian 12 / Linux ARM64 容器 | 实际发行包运行，21 个顶层测试全部通过；普通用户 UID 10001 |
| Linux x86-64 二进制 | 在同一 Linux ARM64 容器中经 OrbStack 的架构转译运行，21 个顶层测试全部通过；不是物理 x86-64 机器验收 |
| Linux 终端交互 | ARM64 发行包打开、按 R 刷新、退出并再次打开成功 |
| Windows WSL 实机 | 尚未验证；需要用户在实际 WSL 发行版及 Windows 数据库客户端环境确认 |

Linux 测试环境为基于本机已有官方 `node:24.15`（Debian 12 ARM64）镜像构建的独立测试镜像，OpenSSH 9.2p1、procps 4.0.2、lsof 4.95.0。测试容器关闭外部网络，只挂载待发布二进制和测试程序为只读；SSH 服务、密钥、echo 目标和配置均临时生成在容器内。

验证覆盖：

- 手动执行 `ssh -qTfnN … -L …` 后的发现、映射与监听读取。
- 中文语言环境；刷新不打断已有连接。
- 外部连接接管及认证预检失败时保留旧连接。
- 真实 SSH 转发数据、断线后的新进程重连。
- 主动停止不会被自动拉起；端口冲突与认证错误停止重试。
- 实际发行二进制启动独立后台，CLI 退出与重新打开不替换连接 PID。
- Linux 配置目录、依赖缺失提示、输入解析、权限和终端布局边界。

测试过的二进制 SHA-256（解压后的 `tunnels`，不是压缩包）：

```text
66a494857eb63a27203b227ef2dddcb871ccf9b91f90d183898151d617bbf184  linux_amd64/tunnels
91a5efb50416d365958cc686c68c7739ad935580437cd3f8ddba85903f82dbd7  linux_arm64/tunnels
```

## 使用边界

本版管理同一 Linux / WSL 发行版、同一本机用户下的 Linux SSH 进程，不扫描 Windows 的 `ssh.exe`。仅托管 TCP `-L`，自动认证使用密钥或 SSH agent；没有密码/OTP 桥接和登录启动服务。WSL 关闭或 `wsl --shutdown` 会终止后台连接。Windows 客户端通过 localhost 访问 WSL 端口需在目标环境验证。

本次发布没有替换当前 Mac 的 `bin/tunnels` 或停止本机现有后台/SSH 隧道。源码已更新为 0.2.0；Mac 二进制仍保留当前已安装版本，后续可自行构建更新。
