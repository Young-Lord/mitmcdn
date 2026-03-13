# 快速开始指南

## 1. 编译项目

```bash
go build -o mitmcdn
```

## 2. 创建配置文件

```bash
cp config.toml.example config.toml
```

编辑 `config.toml`，至少需要配置：

```toml
listen_address = "0.0.0.0:8081"
proxy_mode = "all"
cache_rules_dir = "./config/rules.d"

[cache]
cache_dir = "./data"
max_file_size = "5G"
max_total_size = "100G"
ttl = "72h"
```

在 `config/rules.d` 下新增规则文件，例如 `config/rules.d/cdn.toml`：

```toml
name = "cdn-cache"
scope = "request_only"
expr = 'host contains "your-cdn-domain.com" && path matches "\\.(mp4|exe|zip)$"'
action = "cache"
dedup_strategy = "filename_only"
```

## 3. 首次运行（生成根证书）

```bash
./mitmcdn -config config.toml -db mitmcdn.db
```

首次运行会在 `~/.mitmproxy/` 目录生成根证书 `mitmproxy-ca-cert.pem`。

## 4. 安装根证书

**必须步骤**：在系统或浏览器中安装并信任根证书，否则 HTTPS 拦截将失败。

### Linux (Firefox)
```bash
sudo cp ~/.mitmproxy/mitmproxy-ca-cert.pem /usr/local/share/ca-certificates/mitmcdn.crt
sudo update-ca-certificates
```

### macOS
```bash
# 双击打开证书文件
open ~/.mitmproxy/mitmproxy-ca-cert.pem
# 在 Keychain Access 中设置为"始终信任"
```

### Windows
```bash
# 双击证书文件，导入到"受信任的根证书颁发机构"
```

## 5. 使用代理

### 方式 A：HTTP/SOCKS5 代理

1. 配置系统代理：
   - HTTP 代理：`127.0.0.1:8081`
   - SOCKS5 代理：`127.0.0.1:8082`（如果启用）

2. 或配置浏览器代理设置

3. 正常访问 CDN 资源，服务器自动拦截和缓存

### 方式 B：URL 路径代理

直接访问：
```
http://127.0.0.1:8083/https://your-cdn.com/video.mp4
```

## 6. 验证

1. 访问匹配 CDN 规则的文件
2. 检查 `./data` 目录是否有缓存文件
3. 检查 `mitmcdn.db` 数据库中的文件记录

## 故障排除

### 证书错误
- 确保已安装并信任根证书
- 检查证书文件是否存在：`~/.mitmproxy/mitmproxy-ca-cert.pem`

### 连接失败
- 检查防火墙设置
- 确认监听地址和端口正确
- 检查上游代理配置（如果使用）

### 缓存不工作
- 检查规则是否命中目标域名/路径
- 检查规则 `expr` 是否正确
- 查看日志输出

## 高级配置

### 配置上游代理

```toml
upstream_proxy = "socks5://127.0.0.1:1080"
```

### 多个规则文件

`config/rules.d/cdn1.toml`

```toml
name = "cdn1"
scope = "request_only"
expr = 'host contains "cdn1.httpbin.org" && path matches "\\.mp4$"'
action = "cache"
dedup_strategy = "filename_only"
```

`config/rules.d/cdn2.toml`

```toml
name = "cdn2"
scope = "request_only"
expr = 'host contains "cdn2.httpbin.org" && path matches "\\.(exe|zip)$"'
action = "cache"
dedup_strategy = "full_url"
```
