# Gitrieve Web UI 设计文档

## 1. 项目概述

### 1.1 背景
Gitrieve 是一个基于 Go 的 Git 仓库备份工具，目前主要通过命令行界面操作。为了提升用户体验和易用性，需要添加一个 Web UI，提供可视化的任务管理、监控和配置功能。

### 1.2 目标
- 提供直观的 Web 界面管理备份任务
- 实时监控任务执行状态和日志
- 支持任务的增删改查操作
- 提供配置导入导出功能
- 保持简单易用的部署方式

## 2. 架构设计

### 2.1 整体架构
采用嵌入式架构，将 HTTP 服务器集成到现有的 daemon 进程中：

```
gitrieve server
├── HTTP 服务器 (cmd/server)
│   ├── REST API 路由
│   ├── 中间件 (认证、日志)
│   └── 静态文件服务
├── 调度器包装器 (internal/scheduler)
│   ├── 包装 gocron 调度器
│   ├── 任务状态管理
│   └── 任务执行器
├── 配置管理 API (internal/config/api)
│   ├── 配置读写 API
│   ├── 配置验证
│   └── 配置重载
├── 日志系统 (internal/logger)
│   ├── 内存日志队列
│   ├── 文件日志写入
│   └── 日志查询 API
└── 状态管理 (internal/state)
    ├── 任务运行状态
    ├── 执行历史
    └── 统计信息
```

### 2.2 数据流
```
用户请求 → HTTP 服务器 → 业务逻辑 → 调度器/任务执行器
    ↓                              ↑
前端页面 ← 静态文件服务 ← 状态/日志 API
```

## 3. API 设计

### 3.1 配置管理 API

#### 获取所有配置
- `GET /api/config`
- 返回：`{code: 200, data: Repository[], message: "success"}`
- 错误：`{code: 500, error: "Internal server error"}`

#### 创建新配置
- `POST /api/config`
- 请求体：`{name: string, url: string, cron: string, storage: string[], ...}`
- 返回：`{code: 201, data: Repository, message: "Created"}`

#### 更新配置
- `PUT /api/config/{name}`
- 请求体：`{url?: string, cron?: string, storage?: string[], ...}`
- 返回：`{code: 200, data: Repository, message: "Updated"}`

#### 删除配置
- `DELETE /api/config/{name}`
- 返回：`{code: 200, message: "Deleted"}`

#### 导入配置
- `POST /api/config/import`
- 请求体：文件上传 (multipart/form-data)
- 返回：`{code: 200, data: Repository[], message: "Imported"}`

#### 导出配置
- `GET /api/config/export`
- 返回：YAML 格式的配置文件

### 3.2 任务管理 API

#### 获取所有任务列表
- `GET /api/jobs`
- 查询参数：`?page=1&limit=10&status=running&search=keyword`
- 返回：
```json
{
  "code": 200,
  "data": {
    "jobs": [
      {
        "name": "repo1",
        "url": "https://github.com/user/repo1.git",
        "status": "idle",
        "nextRun": "2024-12-05 02:00:00",
        "lastRun": {
          "status": "success",
          "time": "2024-12-04 02:00:00"
        }
      }
    ],
    "total": 1
  }
}
```

#### 获取任务配置信息
- `GET /api/jobs/{name}`
- 返回：`{code: 200, data: Repository}`

#### 手动执行任务
- `POST /api/jobs/{name}/run`
- 返回：`{code: 202, data: {executionId: "uuid"}, message: "Started"}`

### 3.3 执行历史 API

#### 获取任务执行历史
- `GET /api/jobs/{name}/history`
- 查询参数：`?page=1&limit=20&status=success`
- 返回：
```json
{
  "code": 200,
  "data": {
    "history": [
      {
        "id": "uuid",
        "startTime": "2024-12-04 02:00:00",
        "endTime": "2024-12-04 02:05:00",
        "status": "success",
        "duration": 300
      }
    ],
    "total": 1
  }
}
```

#### 获取所有执行历史
- `GET /api/history`
- 支持分页和过滤

#### 获取执行详情
- `GET /api/history/{id}`
- 返回：完整的执行记录和元数据

### 3.4 日志 API

#### 获取任务日志
- `GET /api/logs/{execution_id}`
- 查询参数：`?limit=1000&offset=0`
- 返回：
```json
{
  "code": 200,
  "data": {
    "logs": [
      {
        "timestamp": "2024-12-04 02:00:01",
        "level": "info",
        "message": "Starting job"
      }
    ]
  }
}
```

#### 实时日志流
- `GET /api/logs/{execution_id}/tail`
- 使用 Server-Sent Events (SSE)
- 返回：`data: {"timestamp": "...", "level": "...", "message": "..."}`

### 3.5 状态监控 API

#### 系统状态
- `GET /api/status`
- 返回：
```json
{
  "code": 200,
  "data": {
    "status": "running",
    "uptime": "1h 30m",
    "version": "1.0.0",
    "jobs": {
      "total": 10,
      "running": 2,
      "idle": 8,
      "error": 0
    }
  }
}
```

#### 统计信息
- `GET /api/stats`
- 返回：成功率、执行次数、错误率等统计信息

### 3.6 认证机制
所有 API 请求需要在 Header 中携带认证 Token：
```
Authorization: Bearer secret-token
```

Token 配置在配置文件中：
```yaml
web:
  auth:
    enabled: true
    token: "your-secret-token"
```

## 4. 前端设计

### 4.1 页面结构
```
web/
├── static/
│   ├── css/
│   │   ├── main.css
│   │   └── job-detail.css
│   ├── js/
│   │   ├── main.js
│   │   ├── job-detail.js
│   │   └── config.js
│   └── images/
└── templates/
    ├── index.html
    ├── job-detail.html
    ├── history-detail.html
    └── config.html
```

### 4.2 页面设计
1. **主页 (`/`)**：任务列表表格，显示任务名、状态、下次执行时间等
2. **任务详情页 (`/jobs/{name}`)**：任务配置信息、执行历史列表、手动执行按钮
3. **执行历史详情页 (`/history/{id}`)**：完整日志输出、执行状态统计
4. **配置管理页 (`/config`)**：配置列表、新增/编辑表单、导入/导出功能

## 5. 数据持久化

### 5.1 数据库设计
```sql
CREATE TABLE executions (
    id TEXT PRIMARY KEY,
    job_name TEXT NOT NULL,
    start_time DATETIME NOT NULL,
    end_time DATETIME,
    status TEXT NOT NULL,
    error_message TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id TEXT NOT NULL,
    timestamp DATETIME NOT NULL,
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    FOREIGN KEY (execution_id) REFERENCES executions(id)
);
```

### 5.2 日志管理
- 内存队列限制大小（1000条）
- 定期清理旧日志文件
- 支持日志轮转

## 6. 核心组件实现

### 6.1 HTTP 服务器
```go
type Server struct {
    router     *gin.Engine
    scheduler  *Scheduler
    configAPI  *ConfigAPI
    loggerAPI  *LoggerAPI
    stateAPI   *StateAPI
}
```

### 6.2 调度器包装器
```go
type Scheduler struct {
    gocron      *gocron.Scheduler
    jobs        map[string]*Job
    mu          sync.RWMutex
    config      *config.Config
}
```

### 6.3 日志系统
```go
type Logger struct {
    mu          sync.Mutex
    memoryQueue []LogEntry
    fileWriter  *os.File
}
```

## 7. 部署设计

### 7.1 构建配置
- 支持 Linux/Windows 多平台构建
- Docker 容器化部署
- systemd 服务管理

### 7.2 配置文件
```yaml
repository:
  - name: "my-repo"
    url: "https://github.com/user/repo.git"
    cron: "0 2 * * *"
    storage: ["local"]
    useCache: true
    downloadReleases: true

storage:
  - name: "local"
    type: "file"
    path: "/var/lib/gitrieve/backups"
```

## 8. 安全考虑

### 8.1 认证和授权
- Token 认证：所有 API 请求需要 Bearer Token
- Token 配置：在配置文件中设置 web.auth.token
- 中间件实现：统一验证请求头中的 Token

### 8.2 输入验证
- 配置验证：验证 URL 格式、Cron 表达式、存储名称等
- 参数验证：API 参数类型和范围检查
- SQL 注入防护：使用参数化查询

### 8.3 安全措施
- 配置文件权限：600，仅所有者可读写
- 日志文件权限：644，避免敏感信息泄露
- HTTPS 支持：可选配置生产环境使用 HTTPS
- CORS 配置：限制跨域请求

### 8.4 错误处理

#### 错误码定义
- `200`: 成功
- `201`: 创建成功
- `400`: 请求参数错误
- `401`: 未认证
- `403`: 权限不足
- `404`: 资源不存在
- `500`: 服务器内部错误

#### 错误响应格式
```json
{
  "code": 400,
  "error": "Invalid parameter",
  "details": "Cron expression is invalid"
}
```

#### 常见错误场景
1. **认证失败**：返回 401，提示无效 Token
2. **任务不存在**：返回 404，提示任务未找到
3. **配置验证失败**：返回 400，列出具体错误项
4. **任务执行失败**：返回 500，包含错误详情
5. **存储不可用**：返回 503，提示服务暂时不可用

#### 错误日志记录
- 所有错误记录到文件和系统日志
- 包含时间戳、请求信息、错误堆栈
- 关键错误触发告警

## 9. 性能优化

### 9.1 缓存策略
- 配置信息缓存
- 任务状态缓存
- HTTP 响应缓存

### 9.2 并发控制
- 限制同时运行的任务数
- 使用工作池模式
- 优化数据库查询

## 10. 监控和运维

### 10.1 日志管理
- 结构化日志输出
- 日志轮转
- 错误追踪

### 10.2 健康检查
- HTTP 健康检查端点
- 系统状态监控
- 性能指标收集

## 11. 测试策略

### 11.1 单元测试
- 调度器测试
- API 测试
- 日志系统测试

### 11.2 集成测试
- HTTP 服务器测试
- 数据库集成测试
- 端到端测试

## 12. 扩展性设计

### 12.1 插件系统
- 支持自定义通知插件
- 支持自定义存储插件
- 插件热加载

### 12.2 主题定制
- CSS 变量主题
- 多语言支持
- 可配置的 UI 组件

## 13. 实现计划

### 13.1 第一阶段（MVP）
- 基础 HTTP 服务器
- 任务列表页面
- 基本的 API 接口
- 简单的日志查看

### 13.2 第二阶段
- 完整的任务管理功能
- 实时日志流
- 配置管理页面
- 执行历史查看

### 13.3 第三阶段
- 高级功能（任务依赖、模板）
- 监控和告警
- 性能优化
- 文档完善

## 14. 总结

这个 Web UI 设计采用了嵌入式架构，保持了 Gitrieve 简单易用的特点，同时提供了丰富的管理功能。设计遵循了 Go 生态的最佳实践，确保了代码的可维护性和扩展性。通过分阶段实施，可以快速交付核心功能，然后逐步完善高级特性。