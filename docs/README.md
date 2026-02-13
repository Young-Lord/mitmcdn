# MitmCDN Cache Server

一个基于 Go 语言编写的高性能代理服务器，通过中间人（MITM）技术拦截发往特定 CDN 的 HTTPS/HTTP 流量。当检测到目标大文件（如视频）请求时，服务器会"激进地"在后台下载整个文件，并向客户端流式传输数据。

## 功能特性

- **MITM 流量拦截**：自动生成自签名根证书，拦截 HTTPS 握手，解密流量
- **激进全量缓存**：客户端请求文件片段时，后台自动下载完整文件
- **边下边播**：使用 `io.TeeReader` 实现数据同时写入缓存和流式传输给客户端
- **下载优先级调度**：高优先级文件优先下载，低优先级任务可暂停
- **断点续传**：支持 HTTP Range 请求，支持恢复未完成的下载
- **文件去重**：支持基于文件名或完整 URL 的去重策略
- **LRU 缓存淘汰**：自动清理过期和最少使用的文件
- **多种代理模式**：支持 HTTP/SOCKS5 代理和 URL 路径代理

## 快速开始

### 1. 安装依赖

```bash
go mod download
```

### 2. 配置

复制示例配置文件并修改：

```bash
cp config.toml.example config.toml
```

编辑 `config.toml`，配置 CDN 域名、缓存路径等。

### 3. 安装根证书

首次运行时会自动生成根证书到 `~/.mitmproxy/mitmproxy-ca-cert.pem`。

**重要**：必须在系统或浏览器中安装并信任此根证书，否则 HTTPS 拦截将失败。

#### Linux (Firefox)
```bash
# 将证书添加到系统信任存储
sudo cp ~/.mitmproxy/mitmproxy-ca-cert.pem /usr/local/share/ca-certificates/mitmcdn.crt
sudo update-ca-certificates
```

#### macOS
```bash
# 双击证书文件，在 Keychain Access 中设置为"始终信任"
open ~/.mitmproxy/mitmproxy-ca-cert.pem
```

#### Windows
```bash
# 双击证书文件，在证书导入向导中选择"将所有的证书都放入下列存储"，选择"受信任的根证书颁发机构"
```

### 4. 运行服务器

```bash
go run main.go -config config.toml -db mitmcdn.db
```

或编译后运行：

```bash
go build -o mitmcdn
./mitmcdn -config config.toml -db mitmcdn.db
```

## 配置说明

### 代理模式

- `http`: 仅 HTTP 代理模式
- `socks5`: 仅 SOCKS5 代理模式
- `url_path`: URL 路径代理模式（如 `http://server:8081/https://cdn.com/file.exe`）
- `all`: 同时启用所有模式

### CDN 规则

```toml
[[cdn_rules]]
domain = "origin.cdn.com"
match_pattern = "\\.(mp4|exe|zip)$"  # URL 正则表达式
dedup_strategy = "filename_only"     # 去重策略：full_url 或 filename_only
```

### 缓存配置

```toml
[cache]
cache_dir = "/var/lib/mitmcdn/data"
max_file_size = "5G"       # 单个文件大小限制
max_total_size = "100G"    # 缓存池总大小限制
ttl = "72h"                # 缓存过期时间
```

## 使用方式

### 模式 A：HTTP/SOCKS5 代理

1. 配置系统或浏览器代理指向服务器地址（如 `127.0.0.1:8081`）
2. 正常访问 CDN 资源，服务器自动拦截和缓存

### 模式 B：URL 路径代理

直接访问：
```
http://mitmcdn.httpbin.org:8081/https://httpbin.org/get?a=1
```

## 架构说明

### 核心组件

- **config**: 配置管理（TOML 解析）
- **database**: 数据库模型和操作（GORM + SQLite）
- **cache**: 缓存管理器（文件去重、LRU 淘汰）
- **download**: 下载调度器（优先级队列、断点续传）
- **proxy**: 代理服务器（MITM、SOCKS5、HTTP 反向代理）

### 数据流

1. 客户端请求 → 代理服务器
2. SNI/Host 检测 → 匹配 CDN 规则
3. 查询缓存 → 如果存在且完整，直接返回
4. 启动下载 → 高优先级任务，边下边播
5. 后台缓存 → 客户端断开后继续下载

## 注意事项

1. **证书信任**：必须安装根证书，否则 HTTPS 拦截失败
2. **磁盘空间**：确保有足够的磁盘空间用于缓存
3. **网络稳定性**：弱网环境下建议配置上游代理
4. **性能**：大文件下载会占用较多带宽和磁盘 I/O

## 开发计划

- [ ] 完整的 HTTP Range 请求解析
- [ ] 更完善的边下边播实现（使用 channels）
- [ ] 集成 go-mitmproxy 库以获得更好的 MITM 支持
- [ ] Web 管理界面
- [ ] 下载速度限制和带宽控制
- [ ] 更详细的日志和监控

## 许可证

MIT License
