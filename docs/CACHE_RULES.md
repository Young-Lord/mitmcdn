# 缓存规则（Expr）

MitmCDN 使用 expr-lang/expr 作为规则引擎，通过目录内的单文件规则定义缓存行为。

## 配置文件概览

主配置 `config.toml` 中指定规则目录：

```toml
cache_rules_dir = "./config/rules.d"
```

每个规则对应一个 TOML 文件，加载顺序为文件名排序，但最终生效顺序由 `priority` 决定。

## 规则文件结构

```toml
name = "video-cache"
scope = "request_only"         # request_only | response_info | response_data
expr = 'host contains "example.com" && path matches "\\.(mp4|mkv)$"'
action = "cache"               # cache | bypass | deny
dedup_strategy = "full_url"    # full_url | filename_only | hash_expr
hash_expr = ''                  # 当 dedup_strategy=hash_expr 时必须，返回 string
ttl_override = "24h"            # 可选：覆盖全局 TTL
priority = 100                  # 越大优先级越高
max_size = "2G"                 # 可选：单文件上限
```

### 字段说明
- `name`：规则名称，便于排查
- `scope`：规则使用的上下文范围
  - `request_only`：只使用请求信息，零额外探测（默认）
  - `response_info`：允许读取响应头与状态码（会发送一次 HEAD/Range 探测）
  - `response_data`：允许读取响应头与响应片段（会发送 Range 探测，默认最多 64KB）
- `expr`：Expr 表达式，返回布尔值
- `action`：命中后的动作
  - `cache`：进入缓存流程
  - `bypass`：只转发，不缓存
  - `deny`：直接拒绝（HTTP 403）
- `dedup_strategy`：去重策略
  - `full_url`：完整 URL
  - `filename_only`：URL path 的文件名
  - `hash_expr`：使用 `hash_expr` 计算结果作为缓存 key
- `hash_expr`：当 `dedup_strategy=hash_expr` 时必须，返回 string（内部会对该值做哈希作为文件键）
- `ttl_override`：可选，覆盖全局 TTL
- `priority`：按降序排序，首个命中生效
- `max_size`：可选，单文件大小上限（超出将终止下载）

## Expr 可用变量

### 请求相关（所有模式可用）
- `method`：HTTP 方法
- `url`：完整 URL
- `host`：域名（含端口）
- `path`：URL path
- `scheme`：http/https
- `query`：RawQuery
- `headers`：请求头（map[string]string）
- `cookies`：Cookies（map[string]string）
- `client_ip`：客户端 IP

### 响应相关（`response_info`/`response_data`）
- `status`：HTTP 状态码
- `content_type`：响应 Content-Type
- `content_length`：响应 Content-Length
- `response_headers`：响应头（map[string]string）

### 响应体片段（仅 `response_data`）
- `body_bytes`：响应体前 N 字节（默认 64KB）
- `body_text`：UTF-8 文本片段（无法解析时为空）

## Expr 可用函数与操作符

内置函数：
- `match(value, pattern)`：正则匹配
- `contains(s, substr)`：包含判断
- `hasPrefix(s, prefix)`
- `hasSuffix(s, suffix)`
- `lower(s)` / `upper(s)`

常用操作符：
- `contains`：字符串包含
- `matches`：正则匹配

示例：

```toml
expr = 'host contains "httpbin.org" && method == "GET"'
```

```toml
expr = 'path matches "\\.(mp4|mkv)$"'
```

## 示例规则

### 1) 按域名缓存

`config/rules.d/httpbin-cache.toml`

```toml
name = "httpbin-cache"
scope = "request_only"
expr = 'host contains "httpbin.org"'
action = "cache"
dedup_strategy = "full_url"
priority = 100
```

### 2) HTML 直通，视频缓存

`config/rules.d/bypass-html.toml`

```toml
name = "bypass-html"
scope = "response_info"
expr = 'content_type contains "text/html"'
action = "bypass"
priority = 200
```

`config/rules.d/cache-video.toml`

```toml
name = "cache-video"
scope = "request_only"
expr = 'path matches "\\.(mp4|mkv)$"'
action = "cache"
dedup_strategy = "filename_only"
priority = 100
```

### 3) 自定义 cache key

`config/rules.d/key-by-host-and-path.toml`

```toml
name = "key-by-host-and-path"
scope = "request_only"
expr = 'host contains "example.com"'
action = "cache"
dedup_strategy = "hash_expr"
hash_expr = 'host + ":" + path'
priority = 100
```

## 运行时行为说明

- 规则按 `priority` 降序排序，首个命中生效。
- `response_info`/`response_data` 会对上游做一次探测请求（HEAD 或 Range）。
- `max_size` 生效于下载阶段，超过限制会终止下载并标记失败。
- `ttl_override` 写入文件元数据，用于清理过期文件时覆盖全局 TTL。
