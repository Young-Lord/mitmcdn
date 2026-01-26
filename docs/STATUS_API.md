# 状态 API 和状态页面

MitmCDN 提供了状态 API 和 HTML 状态页面，用于监控服务器运行状态、缓存信息和下载进度。

## 端点

### 1. `/api/status` - JSON API

返回服务器状态的 JSON 数据。

**请求示例**:
```bash
curl http://127.0.0.1:8081/api/status
```

**响应示例**:
```json
{
  "version": "1.0.0",
  "uptime": "2h 30m 15s",
  "uptime_seconds": 9015.5,
  "cache": {
    "total_files": 42,
    "complete_files": 38,
    "downloading_files": 2,
    "total_size": 10737418240,
    "total_size_human": "10.00 GB",
    "cache_dir": "/var/lib/mitmcdn/data"
  },
  "downloads": {
    "active_tasks": 2,
    "completed_tasks": 38,
    "failed_tasks": 2,
    "total_downloaded": 10737418240,
    "total_downloaded_human": "10.00 GB"
  },
  "files": [
    {
      "hash": "abc123...",
      "url": "https://cdn.com/video.mp4",
      "filename": "video.mp4",
      "size": 104857600,
      "size_human": "100.00 MB",
      "status": "complete",
      "downloaded": 104857600,
      "downloaded_human": "100.00 MB",
      "progress": 100.0,
      "created_at": "2026-01-26T10:00:00Z",
      "last_accessed": "2026-01-26T12:30:00Z"
    }
  ]
}
```

### 2. `/status` - HTML 状态页面

返回美观的 HTML 状态页面，包含所有状态信息。

**访问方式**:
- 浏览器访问: `http://127.0.0.1:8081/status`
- HTTPS: `https://127.0.0.1:8081/status` (需要信任自签名证书)

**页面功能**:
- 📊 实时统计信息（缓存文件数、大小、下载任务等）
- 📁 文件列表（最近访问的50个文件）
- 📈 下载进度条
- 🔄 自动刷新按钮

## 状态信息说明

### 版本信息
- `version`: 服务器版本号（从 `version.json` 读取，默认 "1.0.0"）

### 运行时长
- `uptime`: 人类可读的运行时长（如 "2h 30m 15s"）
- `uptime_seconds`: 运行秒数（浮点数）

### 缓存统计
- `total_files`: 总文件数（包括完成、下载中、失败的）
- `complete_files`: 已完成的文件数
- `downloading_files`: 正在下载的文件数
- `total_size`: 总缓存大小（字节）
- `total_size_human`: 人类可读的大小（如 "10.00 GB"）
- `cache_dir`: 缓存目录路径

### 下载统计
- `active_tasks`: 当前活跃的下载任务数
- `completed_tasks`: 已完成的任务数
- `failed_tasks`: 失败的任务数
- `total_downloaded`: 总下载字节数
- `total_downloaded_human`: 人类可读的总下载量

### 文件列表
每个文件包含：
- `hash`: 文件哈希（去重标识）
- `url`: 原始 URL
- `filename`: 文件名
- `size`: 文件大小（字节）
- `size_human`: 人类可读的大小
- `status`: 状态（complete, downloading, failed, pending）
- `downloaded`: 已下载字节数
- `downloaded_human`: 人类可读的已下载量
- `progress`: 下载进度百分比（0-100）
- `created_at`: 创建时间
- `last_accessed`: 最后访问时间

## 使用场景

### 监控脚本
```bash
#!/bin/bash
# 获取缓存统计
curl -s http://127.0.0.1:8081/api/status | jq '.cache'
```

### 健康检查
```bash
# 检查服务器是否运行
curl -f http://127.0.0.1:8081/api/status > /dev/null && echo "OK" || echo "FAIL"
```

### 自动化监控
```python
import requests
import json

response = requests.get('http://127.0.0.1:8081/api/status')
status = response.json()

print(f"Version: {status['version']}")
print(f"Uptime: {status['uptime']}")
print(f"Cache: {status['cache']['total_files']} files, {status['cache']['total_size_human']}")
print(f"Active Downloads: {status['downloads']['active_tasks']}")
```

## 版本文件

服务器会尝试从 `version.json` 读取版本信息：

```json
{
  "version": "1.0.0",
  "build_date": "2026-01-26",
  "description": "MitmCDN Cache Server"
}
```

如果文件不存在或无法读取，默认使用版本 "1.0.0"。

## 注意事项

1. **性能**: 状态页面会查询数据库，对于大量文件可能较慢
2. **限制**: 文件列表最多显示最近访问的50个文件
3. **实时性**: 状态信息是实时查询的，每次请求都会重新计算
4. **安全性**: 状态端点没有认证，在生产环境可能需要添加访问控制
