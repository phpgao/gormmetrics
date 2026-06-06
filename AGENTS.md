# gormmetrics AI 协作指南

> 本文是注入到所有对话上下文的底层记忆，**只放跨会话恒等的硬约束和强偏好**。

---

## 1. 项目概述

- **项目名称**：gormmetrics
- **仓库地址**：https://github.com/phpgao/gormmetrics
- **主要语言/框架**：Go 1.25 / Prometheus client_golang
- **用途**：GORM 数据库 metrics 采集器（Prometheus exporter），支持 MySQL/PostgreSQL

---

## 2. 常用命令

```bash
# 运行所有单元测试（不需要真实数据库，使用 sqlmock）
go test ./...

# 运行单个测试
go test -run TestCollectEmitsSamplesAndMeta -v ./...

# 运行集成测试（需要真实 MySQL/PostgreSQL）
GORMMETRICS_MYSQL_DSN='user:pass@tcp(host:3306)/dbname' \
GORMMETRICS_POSTGRES_DSN='host=host port=5432 user=u password=p dbname=d sslmode=disable' \
  go test -v ./tests/...

# 构建和 vet
go build ./...
go vet ./...

# 带覆盖率运行测试
go test -cover ./...
```

根模块 `github.com/phpgao/gormmetrics`（Go 1.25）。子模块 `tests/` 有自己的 `go.mod`（`replace github.com/phpgao/gormmetrics => ../`）。GORM 插件在独立仓库 `github.com/phpgao/gormplugin`。

---

## 3. 模块布局

```
gormmetrics/               # module: github.com/phpgao/gormmetrics
├── go.mod                  # deps: prometheus/client_golang, client_model, go-sqlmock
├── *.go                    # core: collector, cache, meta, sample, scraper, logger, doc
├── *_test.go               # collector_test, cache_test, meta_test
├── mysql/                  # MySQL scrapers (SHOW STATUS, perf_schema)
├── postgres/               # PG scrapers (pg_stat_*, pg_locks)
├── userdef/                # SQLGauge/SQLCounter/SQLLabeled/SQLHistogram/FuncScraper
└── tests/                  # separate module (replace => ../)
    └── integration_test.go # requires real MySQL/PostgreSQL + env vars
```

GORM 插件在 `github.com/phpgao/gormplugin`（两个独立子模块）：
- `github.com/phpgao/gormplugin/comment` — SQL comment injection via `gorm.io/hints`
- `github.com/phpgao/gormplugin/metrics` — per-statement latency histogram via GORM callbacks

---

## 4. 架构

### 两条数据路径

**Pull 路径（被动）**：Prometheus scrape `/metrics` → `Collector.Collect()` → ping DB → 对每个启用的 `Scraper`，检查 per-scraper TTL 缓存 → 缓存未命中时调用 `Scraper.Scrape(ctx, db)` → 将 `Sample` 结构体转换为 `prometheus.Metric`，同时输出自动生成的元指标（`gormmetrics_up`、`gormmetrics_scrape_success` 等）。如果 ping 失败，所有 scraper 短路并记录 `up=0` + `connectivity` 错误类——这避免了在死数据库上浪费 N×scrapeTimeout 的 DB 调用。

**Push 路径（事件驱动）**：`gormplugin/metrics` 注册 GORM Before/After 回调以计时每条 SQL 语句，并输出 `gormmetrics_sql_duration_seconds` 直方图（按 `operation`、`dialector`、`status` 分区）。`gormplugin/comment` 通过 `gorm.io/hints` 注入 SQL 注释用于追踪。

### 核心类型

- **`Scraper` 接口** — `Name() string` + `Scrape(ctx context.Context, db *sql.DB) ([]Sample, error)`。指标采集的基本单元。错误不会中断其他 scraper；部分结果会被保留。
- **`ProbingScraper` 接口** — 独立接口，定义 `Probe(ctx, db) error`。在 `New()` 期间通过类型断言检查 `Scraper` 是否也实现了 `ProbingScraper`。Probe 失败则永久禁用该 scraper，并显示 `gormmetrics_scraper_disabled{scraper, reason}`。
- **`Sample` 结构体** — 包含 `Name`、`Help`、`Type`（Gauge/Counter/Histogram）、`Value`、`Labels`，以及可选的 `HistogramBuckets/Count/Sum`。在 `Collect()` 期间即时转换为 `prometheus.Metric`。
- **`metaMetrics`** — 始终启用的遥测 gauge：`up`、`scrape_success`、`scrape_duration_seconds`、`scrape_errors`（按错误类）、`scrape_samples`、`scraper_disabled`。
- **`scraperCache`** — per-scraper TTL 缓存，使用双重检查锁定和 single-flight 语义（只有一个 goroutine 执行缓存未命中窗口的 scrape）。
- **`Logger`** — 简单日志接口（`Infof` + `Debugf`），默认写入 stderr，也提供 `NopLogger`。通过 `WithLogger()` 设置。

### 错误分类

`classifyErr()` 将错误字符串匹配为 `timeout`、`canceled`、`permission_denied`、`connectivity`、`query`、`other`。用户可通过 `WithErrorClassifier()` 覆盖。自定义分类器优先；如果返回 `""`，则回退到 `classifyErr`。

### Scraper 包

- **`mysql/`** — 五个 scraper：`ConnectionsScraper`、`TrafficScraper`、`InnoDBScraper`（ProbingScraper）、`ReplicationScraper`（ProbingScraper）、`QueryLatencyScraper`（ProbingScraper）。三个预设：`MinimalPack()` / `StandardPack()` / `FullPack()`。
- **`postgres/`** — 六个 scraper：`ActivityScraper`、`SizeScraper`、`DatabaseScraper`、`LocksScraper`、`ReplicationScraper`（ProbingScraper）、`TableStatScraper`。相同的预设结构。
- **`userdef/`** — 五种自定义指标构建器：`SQLGauge`、`SQLCounter`、`SQLLabeled`、`SQLHistogram`、`FuncScraper`。`ToFloat()` 将 `database/sql` 驱动值转换为 `float64`。

### 命名约定

- Meta：`gormmetrics_*`（配置了 namespace 时加前缀）
- MySQL：`mysql_*`（如 `mysql_threads_connected`、`mysql_commands_total{command}`）
- PG：`postgres_*`（如 `postgres_backend_connections{state}`）
- GORM：`gormmetrics_sql_duration_seconds{operation,dialector,status}`
- 缓冲池页面通过一个指标名 + `state` 标签展开

### 标签

Collector 级别的 const labels（`WithLabels()`）应用于每个指标。Scraper 的 per-sample 标签在冲突时覆盖 const labels。命名空间前缀通过 `WithNamespace()` 设置。

### 测试模式

- 单元测试使用 `github.com/DATA-DOG/go-sqlmock`，配合 `QueryMatcherRegexp`；`collector_test.go` 额外使用 `MonitorPingsOption(true)`。
- `collector_test.go` 使用 `fakeScraper`/`fakeProbingScraper` 做端到端 Collector 测试。
- 集成测试在 `tests/` 中，需要环境变量，环境变量缺失时调用 `t.Skip()`。
- GORM 插件测试（`gormplugin` 仓库）使用 `sqlite.Open(":memory:")` + `DryRun: true`。

---

## 5. 个人偏好

- 优先使用 `go-sqlmock` 进行单元测试，不依赖真实数据库
- 使用 table-driven 测试模式
- 错误处理遵循 Go 惯用法，不忽略错误
- 提交信息遵循 Conventional Commits 规范

---

## 6. Git 操作约定

- 分支：`main` 为主分支
- 提交规范：Conventional Commits（`feat:`、`fix:`、`refactor:`、`docs:`、`chore:` 等）
- Tag：语义化版本（`vX.Y.Z`）
