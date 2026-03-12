# 开发与调试指南

本文档用于本项目日常开发，包含：
- Go 环境准备（`1.26.0`）
- 编译与启动
- Debug 模式编译与调试启动
- 常用开发命令

## 1. Go 环境准备

项目 `go.mod` 要求：

```text
go 1.26.0
```

先确认当前 Go 版本：

```bash
go version
```

推荐至少满足以下状态之一：
- 输出为 `go1.26.0`（或更高）
- 或明确使用该 toolchain

可查看当前环境：

```bash
go env GOROOT GOPATH GOTOOLCHAIN
```

如果你已经安装了 Go 1.26.0，但命令行仍不是该版本，请先修正 PATH 或 toolchain 配置。

## 1.1 发布版本号管理（`.version`）

项目根目录新增 `.version` 文件，发布版本统一从该文件读取，例如：

```text
msm-v5.0.7
```

发版时只需要修改 `.version`，无需再改 workflow 或 `main.go` 里的版本常量。

## 2. 拉取依赖

```bash
go mod tidy
go mod download
```

## 3. 编译

在项目根目录执行：

```bash
go build -o ./bin/mosdns .
```

说明：
- 可执行文件输出到 `./bin/mosdns`
- 入口是项目根目录 `main.go`

## 4. 启动（开发常用）

本项目常用启动参数：

```bash
./bin/mosdns start --dir config
```

等价显式写法（同时指定主配置）：

```bash
./bin/mosdns start -c config/config.yaml --dir config
```

参数说明：
- `--dir` / `-d`: 工作目录
- `--config` / `-c`: 主配置文件路径
- `--cpu`: 设置 `GOMAXPROCS`

### 4.1 常驻运行与基础可用性验证

建议用独立终端窗口或 `tmux` session 启动，保持进程常驻：

```bash
./bin/mosdns start --dir /Users/doumao/code/github/mosdns/config
```

在另一个会话验证 DNS 与 HTTP 管理接口：

```bash
# DNS 监听验证（53）
dig @127.0.0.1 -p 53 example.com A +time=2 +tries=1

# HTTP 管理接口验证（默认 9099）
curl -sS http://127.0.0.1:9099/api/v1/audit/capacity
```

该接口现在返回审计存储设置和当前磁盘占用，而不是单一“容量”数字。

### 4.2 压测（stress dns）

`stress dns` 是 mosdns 内置子命令，不是独立 `stress` 程序。

示例（与当前仓库验证口径一致）：

```bash
./bin/mosdns stress dns \
  --server 127.0.0.1:53 \
  --domains-file '/Users/doumao/code/github/mosdns/config/unpack/geosite_geolocation-!cn.txt' \
  --count 8000 \
  --unique-count 2000 \
  --concurrency 100 \
  --qps 350 \
  --tcp-sample 0
```

更大样本量可用：

```bash
./bin/mosdns stress dns \
  --server 127.0.0.1:53 \
  --domains-file '/Users/doumao/code/github/mosdns/config/unpack/geosite_geolocation-!cn.txt' \
  --count 16000 \
  --unique-count 2000 \
  --concurrency 100 \
  --qps 350 \
  --tcp-sample 0
```

默认输出：
- `stress-report.json`
- `stress-failures.ndjson`

## 5. Debug 模式编译

为了方便断点调试，关闭优化和内联：

```bash
go build -gcflags "all=-N -l" -o ./bin/mosdns-debug .
```

然后正常启动：

```bash
./bin/mosdns-debug start --dir config
```

## 6. 使用 Delve 调试启动

先安装 Delve（如未安装）：

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
```

方式 1：调试已编译的 debug 二进制

```bash
dlv exec ./bin/mosdns-debug -- start --dir config
```

方式 2：让 Delve 直接编译并启动

```bash
dlv debug . -- start --dir config
```

进入 Delve 后常用命令：

```text
b github.com/IrineSistiana/mosdns/v5/coremain.NewServer
b github.com/IrineSistiana/mosdns/v5/coremain.handleConfigUpdateFromURL
c
n
s
p sf
bt
```

## 7. 常用开发命令

全量测试：

```bash
go test ./...
```

仅测核心模块：

```bash
go test ./coremain
```

静态检查：

```bash
go vet ./...
```

格式化：

```bash
gofmt -w ./coremain ./plugin ./pkg ./tools
```

## 8. 常见问题

### 8.1 报错：`go.mod requires go >= 1.26.0`

说明当前 `go` 版本不满足要求。处理方式：
- 切换到已安装的 Go 1.26.0
- 再次执行 `go version` 确认

### 8.2 启动后找不到配置

确认启动目录与配置文件：

```bash
./bin/mosdns start -c config/config.yaml --dir config
```

并检查 `config/config.yaml` 是否存在、路径是否相对当前目录正确。

### 8.3 需要更多运行日志

将配置中的日志级别调为 `debug` 后重启，或结合 Delve/pprof 进行定位。
