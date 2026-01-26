# 测试文档

本项目包含完整的单元测试套件，覆盖所有核心模块。

## 运行测试

### 运行所有测试
```bash
go test ./...
```

### 运行特定包的测试
```bash
go test ./config -v
go test ./cache -v
go test ./database -v
go test ./proxy -v
go test ./download -v
```

### 运行测试并查看覆盖率
```bash
go test ./... -cover
```

### 运行测试并生成详细覆盖率报告
```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## 集成测试

完整的集成测试覆盖所有4个入口点：

### 入口点测试

- ✅ **TestHTTPProxy**: 测试 HTTP 代理模式
- ✅ **TestHTTPSProxy**: 测试 HTTPS 代理模式（CONNECT 方法）
- ✅ **TestSOCKS5Proxy**: 测试 SOCKS5 代理模式
- ✅ **TestHTTPReverseProxy**: 测试 HTTP 反向代理模式（URL 路径）
- ✅ **TestHTTPSServer**: 测试 HTTPS 服务器模式
- ✅ **TestCertificateGeneration**: 测试证书生成和重用
- ✅ **TestFullFlow**: 测试完整流程（请求 -> 缓存 -> 下载）
- ✅ **TestCDNRuleMatching**: 测试 CDN 规则匹配

运行集成测试：
```bash
go test -v -run "TestHTTPProxy|TestHTTPSProxy|TestSOCKS5Proxy|TestHTTPReverseProxy|TestHTTPSServer|TestCertificateGeneration|TestFullFlow|TestCDNRuleMatching" -timeout 60s
```

**测试文件**: `integration_test.go`

## 单元测试覆盖

### config 包
- ✅ `ParseSize`: 测试各种大小格式解析（B, K, M, G, T）
- ✅ `ParseDuration`: 测试持续时间解析
- ✅ `LoadConfig`: 测试配置文件加载
- ✅ `LoadConfigDefaults`: 测试默认值设置
- ✅ `LoadConfigNotFound`: 测试文件不存在错误处理

**测试文件**: `config/config_test.go`

### cache 包
- ✅ `ComputeFileHash`: 测试文件哈希计算（filename_only 和 full_url 策略）
- ✅ `MatchCDNRule`: 测试 CDN 规则匹配
- ✅ `NewManager`: 测试缓存管理器初始化
- ✅ `GetOrCreateFile`: 测试文件获取和创建
- ✅ `GetOrCreateFileDifferentStrategies`: 测试不同去重策略

**测试文件**: `cache/manager_test.go`

### database 包
- ✅ `InitDB`: 测试数据库初始化和表创建
- ✅ `FileModel`: 测试文件模型 CRUD 操作
- ✅ `LogModel`: 测试日志模型操作
- ✅ `FileUniqueConstraint`: 测试文件哈希唯一性约束

**测试文件**: `database/models_test.go`

### proxy 包
- ✅ `GenerateRootCA`: 测试根 CA 证书生成
- ✅ `LoadOrCreateRootCA`: 测试根 CA 证书加载和创建
- ✅ `GenerateCertificate`: 测试主机证书生成
- ✅ `GenerateCertificateMultipleHosts`: 测试多个主机证书生成

**测试文件**: `proxy/cert_test.go`

### download 包
- ✅ `NewScheduler`: 测试下载调度器初始化
- ✅ `StartDownload`: 测试下载任务启动
- ✅ `PauseLowPriorityTasks`: 测试低优先级任务暂停
- ✅ `SchedulerTaskManagement`: 测试任务管理

**测试文件**: `download/scheduler_test.go`

## 测试统计

运行 `go test ./...` 应该看到类似输出：

```
ok  	mitmcdn/cache	0.009s
ok  	mitmcdn/config	0.002s
ok  	mitmcdn/database	0.009s
ok  	mitmcdn/download	0.007s
ok  	mitmcdn/proxy	0.475s
```

## 测试最佳实践

1. **使用临时文件和目录**: 所有测试使用 `t.TempDir()` 和临时文件，自动清理
2. **隔离测试**: 每个测试独立运行，不依赖其他测试的状态
3. **错误检查**: 所有可能失败的操作都进行错误检查
4. **表驱动测试**: 使用表驱动测试模式提高可维护性

## 持续集成

建议在 CI/CD 流程中运行：
```bash
go test ./... -v -race -coverprofile=coverage.out
```

其中：
- `-v`: 详细输出
- `-race`: 检测数据竞争
- `-coverprofile`: 生成覆盖率报告

## 添加新测试

添加新测试时，请遵循以下规范：

1. 测试函数名以 `Test` 开头
2. 使用 `t.Run()` 进行子测试
3. 使用 `t.Helper()` 标记辅助函数
4. 使用 `t.Cleanup()` 进行资源清理
5. 测试文件以 `_test.go` 结尾

示例：
```go
func TestMyFunction(t *testing.T) {
    tests := []struct {
        name string
        input string
        want string
    }{
        {"case1", "input1", "output1"},
        {"case2", "input2", "output2"},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := MyFunction(tt.input)
            if got != tt.want {
                t.Errorf("MyFunction() = %q, want %q", got, tt.want)
            }
        })
    }
}
```
