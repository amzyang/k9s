# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 仓库定位

这是 derailed/k9s 的自用定制 fork（origin: amzyang/k9s，upstream: derailed/k9s），定期从 upstream 同步，不以给上游提 PR 为主。提交信息沿用上游的 Conventional Commits 风格（`feat:`、`fix(scope):` 等）。

## 常用命令

- 测试：`make test`（会先 `go clean --testcache`，再 `go test ./...`）
- 覆盖率：`make cover`
- 构建：`make build`（CGO_ENABLED=0、`-tags=netgo`，输出 `execs/k9s`）
- Lint：`golangci-lint run`（无 Makefile target；CI 使用 golangci-lint v2.6，配置在 `.golangci.yml`）

改动验证以 `make test` 为准，测试不依赖真实集群，无需连接 Kubernetes 跑 TUI。

## 强制代码风格（lint 会拦截）

- 用 `any`，不用 `interface{}`（gofmt rewrite 规则强制）。
- 日志用 `log/slog`，sloglint 严格模式：只允许 key-value 形式，key 必须用 `internal/slogs` 包里的命名常量（camelCase），禁用 `time`/`level`/`msg`/`source` 作为 key。
- depguard 禁止引入：`sirupsen/logrus`（用 `log/slog`）、`pkg/errors`（用标准库 `errors`）、`instana/testify`（用 `stretchr/testify`）。
- 每个源文件以 SPDX 头开始：
  ```go
  // SPDX-License-Identifier: Apache-2.0
  // Copyright Authors of K9s
  ```
- 上限：函数 60 语句（funlen）、圈复杂度 35（gocyclo）、行宽 170（lll）；godox 会标记 `FIXME` 注释。

## 测试约定

- `stretchr/testify` 的 `assert`/`require`；测试使用外部测试包（`package xxx_test`）。
- fixtures 放在各包的 `testdata/` 目录。

## 项目结构

- `main.go` / `cmd/` — Cobra 入口；`cmd/root.go:run()` 完成配置目录解析、slog 初始化、`loadConfiguration()`（建连 + 加载 config.yaml）、`view.NewApp` 启动。
- `internal/client/` — K8s 连接层：`APIClient`（懒拨号 + LRU 缓存 + `CanI` RBAC 检查）、`GVR` 类型。
- `internal/watch/` — informer 工厂：每 namespace 一个 dynamic shared informer factory（resync 10m），List/Get 走缓存不打 API。
- `internal/dao/` — 数据访问层，按能力拆分的细粒度接口 + GVR 注册表。
- `internal/model/` — 有状态模型（轮询 DAO、观察者推送）；`internal/model1/` — 纯表格数据原语（Header/Row/delta diff），两者是有意分开的两个包。
- `internal/render/` — Renderer 接口实现，资源对象 → 表格行 + 行着色。
- `internal/view/` — 高层视图（Browser、资源视图、Log、Xray、Pulses 等）与命令分派；`internal/ui/` — 底层 tview 控件库（Table/Menu/Prompt/Flash/dialog 等）。
- `internal/config/` — 配置系统（config.yaml、aliases、hotkeys、plugins、views、skins、jumps），带 JSON-schema 校验（`config/json/schemas/`）。
- 其他：`internal/xray/`（依赖树）、`internal/port/`（端口转发）、`internal/vul/`（Grype/Syft 镜像扫描）、`internal/tchart/`（终端图表）。
- 非代码资产：`plugins/`（约 50 个插件 YAML 示例）、`skins/`（约 34 个主题）、`change_logs/`（每版本发布说明）。

## 核心架构与数据流

数据管道（打开一个资源视图的完整链路）：

```
dao（informer 缓存 List/Get）→ model.Table（轮询 + TableListener 观察者）
  → model1.TableData（delta diff）→ render.Xxx（Renderer，并行 Hydrate）
  → view.Browser（TableDataChanged → QueueUpdateDraw）→ ui.Table（tview 绘制）
```

关键机制：

- **GVR 指针驻留是全应用的骨架**：`client.NewGVR()` 从包级缓存返回规范指针，所有注册表都是 `map[*client.GVR]...`，用 `==` 比较（预定义单例在 `client/gvrs.go`，如 `client.PodGVR`）。
- **DAO 能力接口**：`dao/types.go` 定义 `Accessor`/`Nuker`/`Loggable`/`Describer`/`Scalable`/`Restartable`/`Controller` 等细粒度接口，视图用类型断言探测资源能力；`dao/registry.go` 的全局 `MetaAccess` 维护 GVR → APIResource 元数据（含 k9s/helm/crd 合成资源）。
- **模型刷新**：每个活跃 `model.Table` 一个 updater goroutine，首刷 300ms 后按 refreshRate（默认 2s）轮询，原子 CAS 防重入，指数退避容错。
- **context.Context 作为依赖载体**：`internal/keys.go` 的 ContextKey 把 factory/GVR/labels/metrics 标志穿透到 DAO 与 Renderer，而非构造器注入。
- **线程模型**：tview 单线程 draw loop 是唯一 UI 变更点；其他 goroutine 必须经 `QueueUpdateDraw`（`ui/app.go` 内部再包一层 `go func` 防阻塞）。共享状态用 RWMutex/atomic 保护。
- 自定义资源视图（Enter 行为、列渲染）需要同时接触三处注册表：`model.Registry`（DAO+Renderer）、`view/registrar.go`（视图构造器）、`dao/accessor.go`（Accessor 映射）。

## TUI 设计

- **双层 App**：`ui.App`（`internal/ui/app.go`，包 `tview.Application`、header 控件、`:` 命令缓冲）⊂ `view.App`（`internal/view/app.go`，加 `Content *PageStack` 内容栈、`Command` 路由、`watch.Factory`）。TUI 栈是 fork 的 `derailed/tview` + `derailed/tcell/v2`。
- **导航 = 页面栈**：`model.Stack`（LIFO + StackListener 事件）+ `ui.Pages` + `view.PageStack`（Push→Start+聚焦，Pop→Stop）。组件实现 `model.Component`（Init/Start/Stop/Name/Hints），经 `App.inject(c, clearStack)` 入栈。
- **视图组合用装饰器而非继承**：资源视图 = extender 链包裹通用 `Browser`，如 Pod 视图是 `NewPortForwardExtender(NewOwnerExtender(NewVulnerabilityExtender(NewImageExtender(NewLogsExtender(NewBrowser(gvr))))))`；未注册 GVR 默认 `ScaleExtender(OwnerExtender(Browser))`。
- **命令系统**：`view/cmd/interpreter.go` 解析 `:` 命令行（命令、参数、label selector、`@context`），`view/command.go` 分派——特殊命令（ctx/ns/xray/pulses/helm/dir/can/dir/cow）走专用 handler，其余经 alias 解析成 GVR 后构造视图。
- **按键模型**：`ui.KeyActions`（`map[tcell.Key]KeyAction`，Opts 含 Visible/Shared/Plugin/Dangerous）。合并顺序：基础 table 键 → extender 的 bindKeysFn → 动态 refreshActions（按 RBAC verbs 加 Edit/Delete）→ 插件 → 热键；`Hints()` 驱动顶部 F-key Menu 显示（Shared 的不显示）。
- **样式/皮肤**：`config.Styles` + StyleListener 观察者广播；皮肤 YAML 优先级 `K9S_SKIN` env > per-context > 全局 `UI.Skin`；颜色值 `default` 表示终端透明背景。`UI.Reactive` 开启时 fsnotify 热重载皮肤/自定义视图。

## UI/UX 交互约定

- `:` 进入命令模式、`/` 进入过滤模式（两个独立 `model.FishBuff`，带防抖自动补全与建议循环：Tab 接受、上下键切换）；ESC/q 统一返回上一级（页面栈 Pop）；`[`/`]`/`-` 在命令历史间前进/后退/切换。
- Space 标记行、Ctrl-Space 范围标记，批量操作作用于标记集；Shift-N/A/S 按列排序（重复按切换方向）、Shift-O 排序当前选中列、Shift-←/→ 选列；数字键 0-9 快切收藏 namespace（0 = 全部）。
- 危险操作（删除、shell、kill 等）标记 `Dangerous`，read-only 模式下自动隐藏；删除有确认对话框（`ui/dialog/`），并会警告被过滤器隐藏的已标记项。
- 底部 Flash 显示状态消息，顶部 7 行 header（ClusterInfo | Menu | Logo）可 Ctrl-E 折叠，headless 模式压缩为单行 statusIndicator。

## 插件设计

- 定义在 `plugins.yaml`（全局 + per-context + XDG `k9s/plugins/**` 递归加载），schema：`shortCut`、`description`、`scopes`、`command`、`args`、`confirm`、`background`、`dangerous`、`overwriteOutput`、`inputs`（表单输入：string/number/bool/dropdown）。结构体在 `internal/config/plugin.go`。
- **scopes 匹配**：`"all"` 或视图 GVR 的任意别名/短名（`view/actions.go:pluginActions`，在 refreshActions 时绑定按键）；read-only 会话跳过 `dangerous` 插件。
- **args 模板**（`view/env.go`）：`$NAMESPACE`、`$NAME`、`$CONTEXT`、`$CLUSTER`、`$USER`、`$KUBECONFIG`、`$FILTER`、`$COL-<列名>`（选中行任意列值）、`$INPUT_<name>`（inputs 表单值）；支持 `$!XXX` 布尔取反。
- **执行**（`view/exec.go`）：`background: true` 内联执行不退 TUI；否则 `Halt → Suspend → Resume` 退出 alt-screen 把终端交给外部命令；`pipes` 支持命令管道；`overwriteOutput` 读取 `[output]` 前缀行作为 Flash 消息。

## 其他扩展机制

- **aliases**（`aliases.yaml`）：命令别名 → GVR，支持链式解析；内建别名在 `config/alias.go:loadDefaultAliases`。
- **hotkeys**（`hotkeys.yaml`）：一键跳转到任意命令/视图，绑定为 Shared 动作。
- **自定义列/视图**（`views.yaml`，`internal/config/views.go` + `render/cust_col.go`）：按 GVR（可带 `@namespace` 或 `@正则`）定制列，列语法 `NAME:<表达式>|<标志>`——表达式可为 JSONPath 或 jq，标志 N(数字)/T(时间)/W(宽列)/S(显示)/L/R(对齐)/H(隐藏)。
- **custom jumps**（`jumps.yaml`）：按源 GVR 定义 Enter 跳转规则（目标 GVR + label/field selector，支持 Go template 取源对象字段）。
- **NodeShell**：`FeatureGates.NodeShell` + `ShellPod` 配置，起特权调试 Pod 进入节点。

## 配置系统

- 目录解析：`K9S_CONFIG_DIR` env 优先，否则 XDG（config → `~/.config/k9s`，日志/dump → state 目录，集群数据 → data 目录）。
- per-context 配置在 `clusters/<cluster>/<context>/` 下（config、aliases、hotkeys、plugins、benchmarks），每 context 记住活跃 namespace/视图/收藏。
- 所有配置 YAML 加载前经 `internal/config/json/schemas/*.json` 校验；改配置结构时需同步更新对应 schema。

## 注意事项

- 版本号（`cmd.version`/`cmd.commit`/`cmd.date`）只在 `make build`/goreleaser 时经 ldflags 注入，代码里默认为 `dev`，不要硬编码。
- CI 以 GitHub Actions（`.github/workflows/`）为准；`.travis.yml` 和 `.semaphore/` 已废弃。
- `.golangci.yml` 中有大量指向不存在路径的排除规则（借自 golangci-lint 自身仓库），是无害噪音，不要顺手"修复"。

## 发版（fork 专属）

- 触发方式：推送形如 `v<上游版本>-amz.N` 的 tag（如 `v0.51.0-amz.1`），`.github/workflows/release.yml` 只匹配 `v*-amz.*`，误推上游 tag 不会触发。
- 发版配置在 `.goreleaser.fork.yaml`，与上游 `.goreleaser.yml` 分离，避免同步 upstream 时冲突；只构建 darwin/linux × amd64/arm64。
- formula 由 goreleaser 写入 amzyang/homebrew-tap 的 `Formula/k9s.rb`，仓库需配置 secret `HOMEBREW_TAP_GITHUB_TOKEN`（可写 homebrew-tap 的 PAT）。
- 安装：`brew install amzyang/tap/k9s`。
- 本地干跑：`goreleaser release --snapshot --clean --skip=publish --config .goreleaser.fork.yaml`。
