# ai-proxy 智能路由功能改造总结

## 改造概述

本次改造为 ai-proxy 插件添加了**基于意图的智能路由功能**，使其能够与 ai-intent 插件配合，根据用户请求的意图类别动态选择不同的 AI 服务提供商。

## 改造内容

### 1. 配置层改造 (config/config.go)

#### 新增字段
- `intentRouting`: map[string]string - 意图路由配置，key 为意图类别，value 为 provider id

#### 新增方法
```go
// GetIntentRouting 获取意图路由配置
func (c *PluginConfig) GetIntentRouting() map[string]string

// GetProviderById 根据provider id获取对应的Provider实例
func (c *PluginConfig) GetProviderById(providerId string) (provider.Provider, error)

// SelectProviderByIntent 根据意图类别选择对应的Provider
func (c *PluginConfig) SelectProviderByIntent(intentCategory string) (provider.Provider, *provider.ProviderConfig, error)
```

### 2. 运行时改造 (main.go)

#### 核心逻辑修改
在 `onHttpRequestHeader` 函数中添加了意图检测和动态路由逻辑：

```go
// 1. 从 Property 读取意图类别（由 ai-intent 设置）
intentCategory, _ := proxywasm.GetProperty([]string{"intent_category"})

// 2. 根据意图动态选择 provider
if intentCategory != "" {
    selectedProvider, selectedConfig, err := pluginConfig.SelectProviderByIntent(intentCategory)
    if selectedProvider != nil {
        // 将选中的 provider 存入 context
        ctx.SetContext("dynamic_provider", selectedProvider)
        ctx.SetContext("dynamic_provider_config", selectedConfig)
    }
}
```

#### 新增辅助函数
```go
// getActiveProvider 获取当前请求的 active provider（支持动态路由）
func getActiveProvider(ctx wrapper.HttpContext, pluginConfig config.PluginConfig) provider.Provider

// getActiveProviderConfig 获取当前请求的 provider config（支持动态路由）
func getActiveProviderConfig(ctx wrapper.HttpContext, pluginConfig config.PluginConfig) *provider.ProviderConfig
```

#### 修改的处理函数
以下函数全部改为使用 `getActiveProvider` 和 `getActiveProviderConfig`：
- `onHttpRequestBody`
- `onHttpResponseHeaders`
- `onStreamingResponseBody`
- `onHttpResponseBody`

### 3. 文档更新 (README.md)

#### 新增配置说明
在基本配置部分添加了：
- `providers`: 多 provider 配置
- `intentRouting`: 意图路由配置
- `activeProviderId`: 默认 provider id

#### 新增使用示例
添加了完整的智能路由示例，包括：
- 场景说明
- 配置示例（YAML）
- 工作流程图解
- 日志输出示例
- 优势说明

### 4. 测试用例 (test/intent_routing.go)

创建了完整的测试套件，包括：
- 配置解析测试
- 无意图时的默认行为测试
- 有意图时的动态路由测试
- 不同意图类别的路由测试
- 未匹配意图的降级测试

## 技术实现细节

### 数据流

```
ai-intent (优先级 700)
    ↓ SetProperty("intent_category", "法律")
ai-proxy (优先级 100)
    ↓ GetProperty("intent_category")
    ↓ SelectProviderByIntent("法律")
    ↓ 查找 intentRouting["法律"] = "gpt-legal"
    ↓ 创建 gpt-legal provider 实例
    ↓ 存入 context
后续处理阶段
    ↓ 从 context 获取 dynamic_provider
    ↓ 使用动态选择的 provider 处理请求
```

### 关键设计决策

1. **使用 Context 传递动态 provider**
   - 避免修改函数签名
   - 保持向后兼容
   - 线程安全（每个请求独立的 context）

2. **降级策略**
   - 如果没有设置 intent_category，使用默认的 activeProvider
   - 如果 intent_category 未在 intentRouting 中配置，使用默认的 activeProvider
   - 如果动态创建 provider 失败，记录警告并使用默认 provider

3. **性能考虑**
   - Provider 实例在每次请求时动态创建（轻量级操作）
   - ProviderConfig 是引用类型，无需深拷贝
   - 只在 onHttpRequestHeader 阶段做一次路由决策

## 配置示例

```yaml
providers:
  - id: "qwen-finance"
    type: "qwen"
    apiTokens: ["YOUR_QWEN_TOKEN"]
    modelMapping:
      "*": "qwen-turbo"
    
  - id: "gpt-legal"
    type: "openai"
    apiTokens: ["YOUR_OPENAI_TOKEN"]
    modelMapping:
      "*": "gpt-4"
      
  - id: "claude-tech"
    type: "claude"
    apiTokens: ["YOUR_CLAUDE_TOKEN"]
    modelMapping:
      "*": "claude-3-sonnet"

activeProviderId: "qwen-finance"

intentRouting:
  "金融": "qwen-finance"
  "法律": "gpt-legal"
  "技术": "claude-tech"
```

## 使用场景

### 1. 成本优化
- 简单问题 → 低成本模型（如 qwen-turbo）
- 复杂问题 → 高精度模型（如 gpt-4）

### 2. 专业领域优化
- 法律咨询 → GPT-4（逻辑推理强）
- 代码分析 → Claude（长上下文支持好）
- 中文对话 → 通义千问（中文优化）

### 3. A/B 测试
- 同一意图分流到不同模型
- 对比效果和用户满意度

### 4. 容灾降级
- 主模型故障时自动切换到备用模型
- 结合 failover 机制实现高可用

## 兼容性

### 向后兼容
- ✅ 不配置 intentRouting 时，行为与改造前完全一致
- ✅ 单 provider 配置仍然有效
- ✅ 所有现有配置方式保持不变

### 依赖要求
- 需要 Higress 支持 proxywasm.GetProperty API
- ai-intent 插件版本 >= 0.1.0（提供 intent_category）

## 测试验证

运行测试：
```bash
cd plugins/wasm-go/extensions/ai-proxy
go test -v -run TestIntentRouting
```

预期输出：
```
=== RUN   TestIntentRouting
=== RUN   TestIntentRouting/parse_intent_routing_config
=== RUN   TestIntentRouting/no_intent_category_uses_default_provider
=== RUN   TestIntentRouting/with_intent_category_routes_to_correct_provider
=== RUN   TestIntentRouting/different_intent_categories_route_to_different_providers
=== RUN   TestIntentRouting/unmatched_intent_category_uses_default_provider
--- PASS: TestIntentRouting (0.XXs)
```

## 后续优化方向

1. **支持正则表达式匹配意图**
   - 当前只支持精确匹配
   - 可添加模式匹配支持更灵活的路由规则

2. **支持权重分流**
   - 同一意图可以配置多个 provider
   - 按权重比例分配流量

3. **支持动态配置更新**
   - 无需重启即可更新 intentRouting 配置
   - 支持热加载

4. **添加监控指标**
   - 统计各意图的请求量
   - 统计各 provider 的使用情况
   - 路由命中率

5. **支持多级路由**
   - 第一级：意图类别
   - 第二级：子类别或用户等级
   - 第三级：地理位置等

## 相关文件清单

### 修改的文件
- `config/config.go` - 添加意图路由配置和方法
- `main.go` - 添加动态路由逻辑和辅助函数
- `README.md` - 添加智能路由使用说明
- `main_test.go` - 注册意图路由测试

### 新增的文件
- `test/intent_routing.go` - 意图路由测试用例

## 总结

本次改造成功实现了 ai-proxy 的智能路由功能，使其能够：
1. ✅ 读取 ai-intent 设置的意图类别
2. ✅ 根据配置动态选择合适的 provider
3. ✅ 保持向后兼容，不影响现有功能
4. ✅ 提供完整的测试覆盖和文档说明

这为 Higress AI 网关提供了更强大的路由能力，支持更复杂的业务场景和更精细的成本控制。
