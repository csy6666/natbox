# Natbox 下一阶段交接

更新日期：2026-09-23

## 交接目标

继续独立开发 Natbox：面向单台 Linux VPS，以 Incus 或 LXD 管理轻量 Alpine NAT 容器。当前优先级是把已有管理能力做成可验证、可安全升级、出问题可恢复的产品，再改善日常操作体验。不要转去开发或维护 3x-ui；Natbox 不依赖 3x-ui，也不应把其代码合并进 3x-ui 面板。

## 当前基线

- 仓库：`agenProxy paneSimplified proxy panel/natbox`
- 当前分支：`main`；检查时工作树干净。
- 当前 HEAD / 最新标签：`cb7e6ed` / `v0.1.3`（Serialize mutations and add optional auto repair）。开始工作前重新检查分支、状态和远端更新。
- 项目是独立 Go 服务，内嵌单页中文 UI；SQLite 保存 Natbox 管理元数据和审计记录；运行时操作封装在 `internal/incus`。
- README 记录曾在 Ubuntu 24.04、LXD 5.21.7、NAT bridge、Alpine amd64 上做过 smoke test。它不等于完整的 Incus/LXD 兼容矩阵或端到端验收。
- 仓库有 CI 的 `go test`、`go vet`、gofmt 检查和 Linux amd64/arm64 构建；Release workflow 生成二进制和校验和。
- 本机存在父级 `go.work`，其中列出的另一个模块缺失。Windows 本机验证须设 `GOWORK=off`，或运行 `build-linux.ps1`；不要因此擅自改写/删除父级 workspace。
- 本次已验证：`GOWORK=off go test ./...`、`GOWORK=off go test -race ./...`、`GOWORK=off go vet ./...` 均通过。
- README、安全策略、systemd unit、安装/升级脚本、API 和 UI 已存在。编码前先逐项查代码，避免重复实现。

## 已有功能，不要重复造

- Incus/LXD 自动探测、能力/主机容量读取、容器列表/详情和 Alpine 容器创建。
- 单台及最多 32 台批量创建、重复名称预检、容量预检、创建失败回滚、模板、CPU/内存/磁盘配置、开机自启。
- 启停/重启/删除、批量生命周期操作、IPv4 等待、公网 TCP/UDP 转发、全局端口重复检查、SSH 公钥配置和 SSH 端口自动分配。
- SQLite 管理记录、期望状态、操作审计、分页读取、JSON 元数据备份/恢复、定时 SQLite 快照及保留数。
- 运行时对账、缺失/未纳管/状态漂移/端口问题报告；可选自动修复仅对已纳管容器启停，不删除容器。
- 每分钟流量配额/过期策略检查；达到策略时停止容器，不删除数据。
- 可选管理员 Argon2id 密码登录、HttpOnly session cookie、CSRF 校验、登录限速；同时支持 Bearer token 自动化。
- Prometheus 指标、运行诊断、版本化 `/api/v1` 路由、稳定错误码、安全响应头、TLS 配置和加固的 systemd service。
- 在线安装、离线/本机构建安装、版本升级脚本、SHA256 校验、Linux amd64/arm64 发布工作流。

## 下一步优先级

### P0：先完成发布与真实运行时验收

1. **补足隔离的 Incus/LXD 集成测试。** 用临时实例/网络/存储池，禁止触碰用户真实容器。验证创建、IPv4 等待、SSH、公网 TCP/UDP 转发、重启后转发仍有效、失败回滚、停止/启动、删除；分别记录 Incus 与 LXD 的实际版本和差异。默认 CI 继续保持非特权；集成测试可作为手动或专用 runner workflow。
2. **端到端验证配额和到期策略。** 在隔离容器上实际传输已知流量，观察接收/发送计数、重启/计数器归零、运行时 stats 暂时失败、达到阈值后停止及 audit 事件。当前实现每分钟采样，流量可超过限额后才被发现；必须将其明确标为软限额，量化超额窗口，再决定是否增加可配置轮询间隔或改用其他机制。不要宣称精确硬限额。
3. **修正并测试升级脚本故障路径。** `upgrade.sh` 当前下载 `natbox` 与 `natbox-hash`，但校验和只核对 Natbox 主程序；应同时校验两个二进制。用假的 systemd/health checker 做成功、下载校验失败、服务启动失败、health 失败测试，证明旧二进制恢复且临时文件清理。审查初装脚本重复运行时的覆盖/恢复语义，文档明确 install 与 upgrade 的用途区别。
4. **形成可重复的发布检查清单。** 测试、vet、gofmt、amd64/arm64 构建、工件校验和、upgrade/rollback smoke test、API 兼容与 release notes。检查 stable API、旧 `/api` 兼容路由和 OpenAPI 文档同步。

验收底线：任何真实容器修改都只允许在明确隔离、可销毁的测试环境进行；测试须证明失败时没有遗留半创建实例、重复公网端口或无法恢复的 Natbox 服务。

### P1：收紧运行与安全边界

1. **策略和调度器失效行为。** `policy.go` 在周期性 stats 失败时会记录错误并跳过当轮，因此容器可能继续运行。补测试和诊断状态，决定 fail-open/fail-closed 产品语义；默认不能在没有审慎说明与恢复路径时因一次计数失败误停用户容器。
2. **容量预估准确性。** 现在 Linux 主机容量来源是 `/proc/meminfo` 和根文件系统 `statfs`，建议值按请求内存/磁盘折算。检查 Incus/LXD 实际 storage pool、镜像/快照开销、容器固定开销、cgroup/宿主可用资源与并发增长；UI 把估算明确标为建议值，不承诺可以创建的保证数量。
3. **凭据、会话和审计生命周期。** 为内存登录失败表、session 清理和 audit 表增长设边界/清理策略并测试；确认反向代理场景下客户端 IP 只信任显式配置的可信代理，不盲信任任意 `X-Forwarded-For`。不在审计详情、日志或错误响应写入 Bearer token、密码、公钥原文、私钥或敏感配置。
4. **备份真正可恢复。** 自动 SQLite 快照目前只存在本地目录，JSON 恢复只替换 Natbox 管理声明，不创建/删除/改动运行时容器。增加可执行的恢复演练说明和数据库快照完整性检查；异地备份作为明确可选配置，不默认上传任何用户数据。
5. **外部冲突与兼容矩阵。** 对宿主已有监听、不同 proxy device 格式、IPv6/多网卡地址、运行时 API 失败做诊断和集成测试。不要为“检查端口空闲”引入会误报的单次 socket 探测；真正创建失败需返回可操作错误并保持配置一致。

### P1：提升日常控制台可用性

当前主 UI 在 `web/index.html`：具备总览、创建、批量操作、详情、端口、策略、审计、诊断和备份恢复；但重要输入大量使用浏览器原生 `prompt()`/`confirm()`，缺少一致的表单校验、逐项进度/结果和完整键盘/屏幕阅读器交互。

建议先做一个聚焦的 UI 阶段：以对话框/表单替换密码、公钥、配额、到期时间等 `prompt()`；危险操作使用显示准确目标和影响的确认 UI；批量动作展示每台结果和失败原因；错误可复制但默认脱敏；补窄屏、键盘焦点、表单错误与登录过期流程测试。保留现有紧凑的运维控制台风格，不做营销页或无关大改。

### P2：按实际运维需要扩展

- 受控接管/导入已有运行时容器的工作流：预览、明确管理边界、确认后才纳管；不能自动把所有 LXD/Incus 容器变成 Natbox 所有。
- 历史资源/流量图表和告警。目前 UI 汇总读取当前运行时计数，指标端点并不等于已经有持久化历史或告警系统。
- 审计筛选/导出、备份/恢复向导和诊断报告导出。
- 多主机、租户/角色、计费、协议服务安装、容器内 3x-ui provisioning 暂不纳入当前范围。除非用户另行明确，不要把 Natbox 扩成多租户代理面板。

## 推荐交付顺序

1. 先为 `upgrade.sh` 加脚本级测试并补全两个二进制的校验和/回滚覆盖。
2. 增加 Incus 与 LXD 可手动运行的隔离集成测试及操作文档，执行真实生命周期和转发验收。
3. 专门完成配额/到期行为测试，明确一分钟采样的软限制语义及可观测性。
4. 做安全和备份恢复演练，再更新 OpenAPI、README、Release checklist。
5. 最后做 UI 原生 prompt/confirm 替换和批量结果体验。

每阶段保持小 PR/小提交，先读 dirty 状态与已有实现；不要重置或覆盖用户改动。完成后跑：

```powershell
$env:GOWORK = 'off'
go test ./...
go test -race ./...
go vet ./...
```

Linux 发布构建可用 `build-linux.ps1`，或运行 CI 对应的 amd64/arm64 命令。Shell 改动增加 `sh -n`/ShellCheck（如 CI 已安装）验证。

## 下一位 AI 的操作约束

- 只在独立 Natbox 仓库工作。另一个 `3x-ui-s` 工作树含未提交的 Incus 页面/API 实验，不要清理、覆盖、合并或把它当作 Natbox 唯一实现来源。
- 开始先运行 `git status --short --branch`、查看最近提交、搜索实现和测试；不要只依赖本文件。
- 本地 mock/单测通过不代表真实网络转发、流量执法、宿主入站或重启恢复通过。最终报告按证据区分。
- 不把 VPS 密码、SSH 私钥、Bearer token、管理员 hash、SQLite 数据库、生产备份、真实公网地址或节点连接信息写入文档、测试夹具或 commit。
- 任何真实主机部署/升级/删除容器，先确认当前授权范围和具体测试容器；备份配置与数据、记录回滚步骤；不触碰 Natbox 外的服务和 Docker 容器。
- 不做未经请求的发布、tag、推送、服务器升级或生产策略变更。

## 参考代码入口

- HTTP 路由、容器 API、容量/端口预检、并发互斥：`main.go`
- Incus/LXD 命令封装和兼容性：`internal/incus/client.go`
- SQLite schema、声明/策略/审计、备份恢复：`internal/store/store.go`
- 策略执法：`policy.go`
- 运行时对账和自动修复：`reconcile.go`
- Web 登录、session、CSRF、限速：`admin_auth.go`
- 周期性数据库快照：`backup_scheduler.go`
- 安装/升级/权限边界：`install-online.sh`、`install.sh`、`upgrade.sh`、`natbox.service`
- UI：`web/index.html`
- API 契约和运维边界：`openapi.yaml`、`README.md`、`SECURITY.md`
