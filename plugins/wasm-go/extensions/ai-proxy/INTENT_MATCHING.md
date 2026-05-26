# 意图匹配规则说明（前缀 + 包含匹配）

## 📊 功能概述

ai-proxy 插件支持**基于前缀和包含的智能意图匹配**，无需精确匹配即可正确路由请求。

## 🎯 三级匹配策略

```
用户意图输入
    ↓
1️⃣ 精确匹配 (Exact Match)
   ├─ 完全一致 → ✅ 直接路由
   └─ 不一致 ↓
   
2️⃣ 前缀匹配 (Prefix Match)
   ├─ 意图以配置的 key 开头 → ✅ 路由
   └─ 不匹配 ↓
   
3️⃣ 包含匹配 (Contains Match)
   ├─ 意图包含配置的 key → ✅ 路由
   └─ 不匹配 ↓
   
4️⃣ 默认 Provider (Fallback)
   └─ 使用 activeProviderId 指定的 provider
```

## 📝 匹配规则详解

### 1. 精确匹配（优先级最高）

**规则**：意图类别与配置的 key 完全一致

**示例**：
```yaml
intentRouting:
  "法律": "gpt-legal"

# 匹配情况
"法律" → ✅ 精确匹配 → gpt-legal
```

### 2. 前缀匹配（次优先）

**规则**：意图类别以配置的 key 开头

**示例**：
```yaml
intentRouting:
  "法律": "gpt-legal"
  "金融": "qwen-finance"

# 匹配情况
"法律咨询" → ✅ 前缀匹配 → gpt-legal
"法律服务" → ✅ 前缀匹配 → gpt-legal
"金融分析" → ✅ 前缀匹配 → qwen-finance
"金融服务" → ✅ 前缀匹配 → qwen-finance
```

**应用场景**：
- LLM 输出带有后缀的意图（如"法律咨询"、"金融分析"）
- 不同细分领域共享同一个 provider

### 3. 包含匹配（最后尝试）

**规则**：意图类别包含配置的 key

**示例**：
```yaml
intentRouting:
  "法律": "gpt-legal"
  "金融": "qwen-finance"

# 匹配情况
"关于法律的问题" → ✅ 包含匹配 → gpt-legal
"我需要金融方面的帮助" → ✅ 包含匹配 → qwen-finance
"寻求技术支持" → ✅ 包含匹配 → claude-tech
```

**应用场景**：
- LLM 输出自然语言形式的意图
- 意图描述较为冗长

### 4. 默认 Provider

**规则**：以上三种匹配都失败时使用

**示例**：
```yaml
activeProviderId: "qwen-finance"

# 匹配情况
"医疗" → ❌ 无匹配 → qwen-finance (默认)
"天气预报" → ❌ 无匹配 → qwen-finance (默认)
```

## 🔍 完整示例

### 配置
```yaml
pluginConfig:
  providers:
    - id: "qwen-finance"
      type: "qwen"
      apiTokens: ["YOUR_QWEN_TOKEN"]
    
    - id: "gpt-legal"
      type: "openai"
      apiTokens: ["YOUR_OPENAI_TOKEN"]
    
    - id: "claude-tech"
      type: "claude"
      apiTokens: ["YOUR_CLAUDE_TOKEN"]
  
  activeProviderId: "qwen-finance"
  
  intentRouting:
    "金融": "qwen-finance"
    "法律": "gpt-legal"
    "技术": "claude-tech"
```

### 匹配结果

| ai-intent 输出 | 匹配类型 | 匹配到的 key | 路由到 | 说明 |
|----------------|----------|--------------|--------|------|
| "金融" | 精确匹配 | "金融" | qwen-finance | 完全一致 |
| "金融分析" | 前缀匹配 | "金融" | qwen-finance | 以"金融"开头 |
| "金融服务" | 前缀匹配 | "金融" | qwen-finance | 以"金融"开头 |
| "我需要金融帮助" | 包含匹配 | "金融" | qwen-finance | 包含"金融" |
| "法律" | 精确匹配 | "法律" | gpt-legal | 完全一致 |
| "法律咨询" | 前缀匹配 | "法律" | gpt-legal | 以"法律"开头 |
| "关于法律的问题" | 包含匹配 | "法律" | gpt-legal | 包含"法律" |
| "技术" | 精确匹配 | "技术" | claude-tech | 完全一致 |
| "技术支持" | 前缀匹配 | "技术" | claude-tech | 以"技术"开头 |
| "寻求技术支持" | 包含匹配 | "技术" | claude-tech | 包含"技术" |
| "医疗" | 无匹配 | - | qwen-finance | 使用默认 provider |
| "客服" | 无匹配 | - | qwen-finance | 使用默认 provider |

## ⚙️ 最佳实践

### 1. 使用简短明确的类别名

✅ **推荐**：
```yaml
intentRouting:
  "金融": "qwen-finance"
  "法律": "gpt-legal"
  "技术": "claude-tech"
```

❌ **不推荐**：
```yaml
intentRouting:
  "金融理财投资": "qwen-finance"  # 太长，难以前缀/包含匹配
  "法律法务咨询": "gpt-legal"     # 冗余
```

### 2. 避免重叠的配置

❌ **可能导致歧义**：
```yaml
intentRouting:
  "金融": "qwen-finance"
  "金融服务": "gpt-legal"  # "金融服务" 会先被 "金融" 前缀匹配捕获
```

✅ **正确做法**：
```yaml
intentRouting:
  "金融": "qwen-finance"
  # 如果需要区分，使用不同的关键词
  "银行": "gpt-legal"
```

### 3. 配合标准化的意图输出

在 ai-intent 的 Prompt 中要求输出简洁的意图类别：

```yaml
scene:
  prompt: |
    你是一个智能类别识别助手。请从以下预设类别中选择最匹配的一个：
    - 金融
    - 法律
    - 技术
    - 客服
    
    用户问题：'%s'
    
    请直接返回类别名称，不要添加任何额外文字。如果不确定，返回最接近的类别。
```

### 4. 监控和优化

通过日志观察匹配情况：

```bash
# 查看成功路由的请求
kubectl logs -n higress-system -l higress=higress-system-higress-gateway | \
  grep "Intent Routing" | grep "Routed"

# 查看使用默认 provider 的请求（可能需要优化）
kubectl logs -n higress-system -l higress=higress-system-higress-gateway | \
  grep "Intent Routing" | grep "default provider"
```

根据日志调整：
- 如果很多请求使用了默认 provider，考虑添加新的意图类别
- 如果发现匹配错误，检查是否有重叠的配置
- 分析常见的未匹配意图，添加到 intentRouting 中

## 🧪 测试方法

### 单元测试

运行意图路由测试：

```bash
cd plugins/wasm-go/extensions/ai-proxy
go test -v -run TestIntentRouting
```

### 手动测试

```bash
# 测试 1: 精确匹配
curl -X POST http://ai-gateway.example.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"法律问题"}]}'

# 预期：路由到 gpt-legal

# 测试 2: 前缀匹配
curl -X POST http://ai-gateway.example.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"法律咨询"}]}'

# 预期：路由到 gpt-legal（前缀匹配）

# 测试 3: 包含匹配
curl -X POST http://ai-gateway.example.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"关于法律的问题"}]}'

# 预期：路由到 gpt-legal（包含匹配）
```

## 💡 常见问题

### Q1: 前缀匹配和包含匹配有什么区别？

**A**: 
- **前缀匹配**：意图必须以配置的 key 开头
  - `"法律咨询"` 匹配 `"法律"` ✅
  - `"关于法律"` 不匹配 `"法律"` ❌

- **包含匹配**：意图只要包含配置的 key 即可
  - `"法律咨询"` 匹配 `"法律"` ✅
  - `"关于法律"` 匹配 `"法律"` ✅

### Q2: 如果一个意图同时匹配多个配置怎么办？

**A**: 按优先级处理：
1. 精确匹配优先于前缀匹配
2. 前缀匹配优先于包含匹配
3. 同一级别中，先遍历到的配置优先

例如：
```yaml
intentRouting:
  "金融": "provider-a"
  "金融服务": "provider-b"

# "金融服务" 会匹配 "金融"（前缀匹配），不会继续匹配 "金融服务"
```

### Q3: 如何确保某个意图总是精确匹配？

**A**: 确保该意图在 intentRouting 中有完全一致的 key：

```yaml
intentRouting:
  "法律": "gpt-legal"        # 精确匹配
  "法律咨询": "other-provider" # 这个不会被 "法律" 捕获
```

但注意：由于是 map 遍历，顺序不确定，建议避免这种重叠配置。

### Q4: 性能影响大吗？

**A**: 非常小。前缀和包含匹配都是字符串操作，对于短字符串（< 50 字符）：
- 单次匹配：< 1 微秒
- 遍历所有配置（假设 10 个）：< 10 微秒
- **对整体延迟的影响**: 可忽略不计 (< 0.01ms)

### Q5: 如果想禁用前缀/包含匹配怎么办？

**A**: 目前无法单独禁用，但可以：
1. 只配置精确匹配的 key
2. 在 ai-intent 中确保输出标准化的意图类别

或者修改代码，注释掉前缀/包含匹配的逻辑。

## 📈 优势总结

✅ **优点**：
- 容错性强，能处理各种形式的意图输出
- 无需精确匹配，降低配置难度
- 性能好，计算开销极小
- 逻辑简单，易于理解和调试

⚠️ **注意事项**：
- 避免配置重叠的 key
- 使用简短明确的类别名
- 定期监控日志，优化配置

🎯 **最佳实践**：
1. 在 ai-intent 中输出标准化的意图类别
2. 使用简短明确的类别名称（2-4 个字）
3. 避免重叠的配置
4. 监控日志，持续优化配置

## 🔄 与相似度匹配的对比

| 特性 | 前缀+包含匹配 | 相似度匹配 |
|------|---------------|------------|
| **实现复杂度** | 简单 | 复杂（需要编辑距离算法） |
| **性能** | 极快 | 较快（O(n×m)） |
| **灵活性** | 中等 | 高 |
| **可预测性** | 高（规则明确） | 中（依赖阈值） |
| **适用场景** | 结构化意图 | 非结构化意图 |
| **配置难度** | 低 | 中（需要调阈值） |

**结论**：前缀+包含匹配更适合大多数场景，特别是当 ai-intent 能够输出相对标准化的意图时。
