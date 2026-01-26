# 统一服务器说明

## 概述

MitmCDN 现在支持**统一服务器模式**，所有4个服务可以在**同一个地址和端口**上运行，通过自动协议检测来区分不同的协议。

## 支持的协议

在 `listen_address` 指定的地址上，服务器会自动检测并处理以下协议：

### 1. HTTP Proxy
- **协议检测**: HTTP 请求（GET, POST, CONNECT 等）
- **使用方式**: 配置系统/浏览器 HTTP 代理为 `http://listen_address`
- **示例**: `http://127.0.0.1:8081`

### 2. SOCKS5 Proxy
- **协议检测**: SOCKS5 握手（第一个字节为 0x05）
- **使用方式**: 配置系统/浏览器 SOCKS5 代理为 `socks5://listen_address`
- **示例**: `socks5://127.0.0.1:8081`

### 3. HTTP Reverse Proxy (URL Path 模式)
- **协议检测**: HTTP 请求，路径以 `/http://` 或 `/https://` 开头
- **使用方式**: 直接访问 `http://listen_address/https://target.com/file`
- **示例**: `http://127.0.0.1:8081/https://example.com/video.mp4`

### 4. HTTPS Server
- **协议检测**: TLS 握手（第一个字节为 0x16）
- **使用方式**: 通过 HTTPS 访问 `https://listen_address`
- **示例**: `https://127.0.0.1:8081/https://example.com/file`

## 协议检测机制

服务器通过检查连接的第一个字节来识别协议：

```
第一个字节 = 0x05  → SOCKS5 Proxy
第一个字节 = 0x16  → HTTPS (TLS 握手)
其他 ASCII 字符    → HTTP (GET, POST, CONNECT 等)
```

对于 HTTP 请求，服务器会进一步检查路径：
- 路径以 `/http://` 或 `/https://` 开头 → HTTP Reverse Proxy
- 其他路径 → HTTP Proxy

## 配置示例

```toml
listen_address = "0.0.0.0:8081"
proxy_mode = "all"
```

启动后，所有服务都在 `0.0.0.0:8081` 上监听。

## 使用示例

### HTTP Proxy
```bash
# 配置浏览器代理
export http_proxy=http://127.0.0.1:8081
export https_proxy=http://127.0.0.1:8081

# 或使用 curl
curl -x http://127.0.0.1:8081 https://example.com/
```

### SOCKS5 Proxy
```bash
# 配置浏览器 SOCKS5 代理
export ALL_PROXY=socks5://127.0.0.1:8081

# 或使用 curl
curl --socks5 127.0.0.1:8081 https://example.com/
```

### HTTP Reverse Proxy
```bash
# 直接访问
curl http://127.0.0.1:8081/https://example.com/video.mp4
```

### HTTPS Server
```bash
# 通过 HTTPS 访问（需要信任自签名证书）
curl -k https://127.0.0.1:8081/https://example.com/file
```

## 优势

1. **简化配置**: 只需配置一个地址和端口
2. **节省端口**: 不需要为每个服务分配不同端口
3. **自动识别**: 客户端无需指定协议，服务器自动检测
4. **统一管理**: 所有服务共享同一个监听器，便于管理和监控

## 注意事项

1. **证书信任**: HTTPS Server 使用自签名证书，客户端需要配置信任
2. **协议冲突**: 理论上不同协议不会冲突，因为检测机制可靠
3. **性能**: 协议检测开销很小，对性能影响可忽略

## 技术实现

统一服务器使用 `peekConn` 包装原始连接，允许在不消耗数据的情况下检测协议：

1. 读取第一个字节（peek）
2. 根据字节值判断协议类型
3. 将已读取的字节放回缓冲区
4. 将连接传递给相应的处理器

这种实现确保了：
- 数据不会丢失
- 协议检测准确
- 性能开销最小
