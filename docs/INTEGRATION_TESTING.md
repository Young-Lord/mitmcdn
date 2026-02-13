# 集成测试文档

本文档描述完整的集成测试套件，覆盖所有4个入口点的完整流程。

## 测试入口点

### 1. HTTP Proxy (TestHTTPProxy)
测试标准 HTTP 代理模式：
- 客户端通过 HTTP 代理发送请求
- 代理拦截并处理请求
- 验证缓存功能

```bash
go test -v -run TestHTTPProxy
```

### 2. HTTPS Proxy (TestHTTPSProxy)
测试 HTTPS 代理模式（CONNECT 方法）：
- 客户端通过 HTTP 代理发送 HTTPS CONNECT 请求
- 代理进行 MITM 拦截
- 验证证书生成和使用

```bash
go test -v -run TestHTTPSProxy
```

### 3. SOCKS5 Proxy (TestSOCKS5Proxy)
测试 SOCKS5 代理模式：
- 客户端通过 SOCKS5 代理连接
- 验证代理服务器监听和接受连接

```bash
go test -v -run TestSOCKS5Proxy
```

### 4. HTTP Reverse Proxy (TestHTTPReverseProxy)
测试 URL 路径代理模式：
- 客户端直接访问 `http://server/https://target.com/file`
- 服务器解析 URL 路径中的目标地址
- 验证反向代理功能

```bash
go test -v -run TestHTTPReverseProxy
```

### 5. HTTPS Server (TestHTTPSServer)
测试 HTTPS 服务器模式：
- 客户端通过 HTTPS 访问服务器
- 服务器使用自签名证书
- 验证 TLS 连接

```bash
go test -v -run TestHTTPSServer
```

## 功能测试

### 证书生成 (TestCertificateGeneration)
测试证书生成和重用：
- 生成根 CA 证书
- 为不同主机生成证书
- 验证证书缓存

```bash
go test -v -run TestCertificateGeneration
```

### 完整流程 (TestFullFlow)
测试完整的请求-缓存-下载流程：
1. 发送 HTTP 请求
2. 验证文件被缓存到数据库
3. 验证文件下载到磁盘
4. 验证第二次请求使用缓存

```bash
go test -v -run TestFullFlow
```

### CDN 规则匹配 (TestCDNRuleMatching)
测试 CDN 规则匹配逻辑：
- 验证匹配的域名被拦截
- 验证不匹配的域名不被拦截

```bash
go test -v -run TestCDNRuleMatching
```

## 运行所有集成测试

```bash
go test -v -run "TestHTTPProxy|TestHTTPSProxy|TestSOCKS5Proxy|TestHTTPReverseProxy|TestHTTPSServer|TestCertificateGeneration|TestFullFlow|TestCDNRuleMatching" -timeout 60s
```

## 测试架构

### 测试服务器设置

每个测试使用 `setupTestServer()` 函数创建：
- 临时数据库（SQLite）
- 临时缓存目录
- HTTP 代理服务器（随机端口）
- SOCKS5 代理服务器（随机端口）
- HTTP 反向代理服务器（随机端口）
- HTTPS 服务器（随机端口）

### 测试客户端

测试使用以下客户端配置：
- **HTTP 客户端**: 配置代理，信任所有证书（`InsecureSkipVerify: true`）
- **超时设置**: 10-15 秒
- **测试 URL**: `https://httpbin.org/get?a=1` 或 `http://httpbin.org/get?a=1`

### 证书处理

- 测试环境自动生成根 CA 证书
- 为每个主机动态生成证书
- 客户端配置为信任所有证书（仅测试环境）

## 测试数据流

```
客户端请求
    ↓
入口点（HTTP/SOCKS5/Reverse/HTTPS）
    ↓
MITM 代理拦截
    ↓
CDN 规则匹配
    ↓
缓存管理器（查询/创建文件记录）
    ↓
下载调度器（启动下载任务）
    ↓
HTTP 客户端（从上游下载）
    ↓
文件缓存（写入磁盘）
    ↓
响应客户端
```

## 验证点

每个测试验证：
1. ✅ 服务器成功启动
2. ✅ 客户端可以连接
3. ✅ 请求被正确处理
4. ✅ 文件记录在数据库中
5. ✅ 缓存目录存在
6. ✅ 证书正确生成

## 注意事项

1. **网络依赖**: 测试需要访问 `httpbin.org`，需要网络连接
2. **证书信任**: 测试环境使用 `InsecureSkipVerify`，生产环境不应使用
3. **超时设置**: 某些测试可能需要较长时间，设置了 60 秒超时
4. **临时文件**: 所有测试使用临时目录，测试后自动清理

## 故障排除

### 测试失败：连接被拒绝
- 检查服务器是否成功启动
- 验证端口是否被占用
- 增加服务器启动等待时间

### 测试失败：证书错误
- 确保使用 `InsecureSkipVerify: true`
- 检查证书生成函数是否正常工作

### 测试失败：数据库只读
- 确保每个测试使用独立的数据库文件
- 检查文件权限

### 测试超时
- 增加超时时间：`-timeout 120s`
- 检查网络连接
- 验证目标 URL 是否可访问
