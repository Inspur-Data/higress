package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	logs "github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/tidwall/gjson"
)

// ============ 常量定义 ============

const (
	// APICallTimeout is the timeout for external API calls in milliseconds
	APICallTimeout = 5000

	// DefaultSimilarityThreshold is used when rule doesn't specify a threshold
	DefaultSimilarityThreshold = 0.85

	// GRPCStatusNotUsed indicates gRPC status is not used in HTTP response
	GRPCStatusNotUsed = -1
)

// ============ 数据模型 ============

// RedlineRule 红线知识库规则
type RedlineRule struct {
	ID           int64     `json:"id"`
	Question     string    `json:"question"`
	Answer       string    `json:"answer"`
	Threshold    float64   `json:"threshold"`
	Enabled      bool      `json:"enabled"`
	Vector       []float64 `json:"vector,omitempty"`       // 预计算的向量
	UseEmbedding bool      `json:"use_embedding,omitempty"` // 是否使用嵌入模型
}

// SecurityVocabulary 安全词表规则
type SecurityVocabulary struct {
	ID           int64  `json:"id"`
	RegexPattern string `json:"regex_pattern"`
	Answer       string `json:"answer"`
	Enabled      bool   `json:"enabled"`
}

// PromptAttackRule 提示词攻击规则
type PromptAttackRule struct {
	ID           int64  `json:"id"`
	RegexPattern string `json:"regex_pattern"`
	Description  string `json:"description"`
	Enabled      bool   `json:"enabled"`
}

// DataMaskingRule 数据脱敏规则
type DataMaskingRule struct {
	ID           int64  `json:"id"`
	Type         string `json:"type"`
	RegexPattern string `json:"regex_pattern"`
	MaskPattern  string `json:"mask_pattern"`
	Enabled      bool   `json:"enabled"`
}

// RulesCache 规则缓存
type RulesCache struct {
	Redline      []RedlineRule        `json:"redline"`
	Vocabularies []SecurityVocabulary `json:"vocabularies"`
	Attacks      []PromptAttackRule   `json:"attacks"`
	Masking      []DataMaskingRule    `json:"masking"`
	Version      int                  `json:"cache_version"`
	LastUpdated  time.Time            `json:"last_updated"`
	TTL          time.Duration
	ExpiresAt    time.Time
}

// PluginConfig 插件配置 - 适配 Higress 扁平化配置格式
type PluginConfig struct {
	// Higress 服务发现配置
	APIPath       string `json:"api_path,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	ServiceName   string `json:"service_name,omitempty"`
	ServicePort   int    `json:"service_port,omitempty"`
	ServiceSource string `json:"service_source,omitempty"` // "dns" or "static"
	Timeout       int    `json:"timeout,omitempty"`        // 毫秒

	// 业务配置
	CheckRequest   bool   `json:"check_request,omitempty"`
	CheckResponse  bool   `json:"check_response,omitempty"`
	DenyCode       int    `json:"deny_code,omitempty"`
	DenyMessage    string `json:"deny_message,omitempty"`
	CacheTTL       int    `json:"cache_ttl,omitempty"`
	CacheMaxSize   int    `json:"cache_max_size,omitempty"`
	SimilarityMode string `json:"similarity_mode,omitempty"` // "regex", "embedding", "hybrid"

	// 规则服务配置（兼容旧格式）
	RuleAPIServiceDomain string `json:"rule_api_service_domain,omitempty"`
	RuleAPIServicePort   int    `json:"rule_api_service_port,omitempty"`
	RuleAPIServicePath   string `json:"rule_api_service_path,omitempty"`

	// 嵌入服务配置（兼容旧格式）
	EmbeddingEnabled  bool   `json:"embedding_enabled,omitempty"`
	EmbeddingDomain   string `json:"embedding_domain,omitempty"`
	EmbeddingPort     int    `json:"embedding_port,omitempty"`
	EmbeddingAPIPath  string `json:"embedding_api_path,omitempty"`
	EmbeddingModel    string `json:"embedding_model,omitempty"`
	EmbeddingTimeout  int    `json:"embedding_timeout,omitempty"`
}

// ============ 全局变量 ============

var (
	config        PluginConfig
	rulesCache    *RulesCache
	cacheMutex    sync.RWMutex
	regexCache    map[string]*regexp.Regexp
	regexMutex    sync.RWMutex
	vectorCache   map[string][]float64 // 向量缓存：文本 -> 向量
	vectorMutex   sync.RWMutex
)

// ============ 配置辅助方法 ============

// getRuleServiceDomain 获取规则服务域名
func (c *PluginConfig) getRuleServiceDomain() string {
	if c.RuleAPIServiceDomain != "" {
		return c.RuleAPIServiceDomain
	}
	// 兼容 Higress 风格配置
	if c.ServiceName != "" && c.Namespace != "" {
		return fmt.Sprintf("%s.%s.svc.cluster.local", c.ServiceName, c.Namespace)
	}
	return "localhost"
}

// getRuleServicePort 获取规则服务端口
func (c *PluginConfig) getRuleServicePort() int {
	if c.RuleAPIServicePort > 0 {
		return c.RuleAPIServicePort
	}
	if c.ServicePort > 0 {
		return c.ServicePort
	}
	return 8080
}

// getRuleServicePath 获取规则服务路径
func (c *PluginConfig) getRuleServicePath() string {
	if c.RuleAPIServicePath != "" {
		return c.RuleAPIServicePath
	}
	if c.APIPath != "" {
		return c.APIPath
	}
	return "/api/v1/rules"
}

// isEmbeddingEnabled 检查是否启用嵌入服务
func (c *PluginConfig) isEmbeddingEnabled() bool {
	return c.EmbeddingEnabled
}

// getEmbeddingDomain 获取嵌入服务域名
func (c *PluginConfig) getEmbeddingDomain() string {
	if c.EmbeddingDomain != "" {
		return c.EmbeddingDomain
	}
	return "embedding-service.ai.svc.cluster.local"
}

// getEmbeddingPort 获取嵌入服务端口
func (c *PluginConfig) getEmbeddingPort() int {
	if c.EmbeddingPort > 0 {
		return c.EmbeddingPort
	}
	return 8000
}

// getEmbeddingAPIPath 获取嵌入 API 路径
func (c *PluginConfig) getEmbeddingAPIPath() string {
	if c.EmbeddingAPIPath != "" {
		return c.EmbeddingAPIPath
	}
	return "/v1/embeddings"
}

// getEmbeddingModel 获取模型名称
func (c *PluginConfig) getEmbeddingModel() string {
	if c.EmbeddingModel != "" {
		return c.EmbeddingModel
	}
	return "bge-large-zh"
}

// getEmbeddingTimeout 获取嵌入服务超时
func (c *PluginConfig) getEmbeddingTimeout() int {
	if c.EmbeddingTimeout > 0 {
		return c.EmbeddingTimeout
	}
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 3000
}

// ============ 插件初始化 ============

func main() {}

func init() {
	wrapper.SetCtx(
		"llmsec-plugin",
		wrapper.ParseConfigBy(parseConfig),
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
		wrapper.ProcessRequestBodyBy(onHttpRequestBody),
		wrapper.ProcessResponseBodyBy(onHttpResponseBody),
	)
}

// parseConfig 解析插件配置
func parseConfig(json gjson.Result, config *PluginConfig, log logs.Log) error {
	// 解析基础配置
	config.CheckRequest = json.Get("check_request").Bool()
	config.CheckResponse = json.Get("check_response").Bool()
	config.DenyCode = int(json.Get("deny_code").Int())
	config.DenyMessage = json.Get("deny_message").String()
	config.CacheTTL = int(json.Get("cache_ttl").Int())
	config.CacheMaxSize = int(json.Get("cache_max_size").Int())
	config.SimilarityMode = json.Get("similarity_mode").String()

	// Higress 风格的服务发现配置
	config.APIPath = json.Get("api_path").String()
	config.Namespace = json.Get("namespace").String()
	config.ServiceName = json.Get("service_name").String()
	config.ServicePort = int(json.Get("service_port").Int())
	config.ServiceSource = json.Get("service_source").String()
	config.Timeout = int(json.Get("timeout").Int())

	// 规则服务配置（兼容两种格式）
	config.RuleAPIServiceDomain = json.Get("rule_api_service_domain").String()
	config.RuleAPIServicePort = int(json.Get("rule_api_service_port").Int())
	config.RuleAPIServicePath = json.Get("rule_api_service_path").String()

	// 嵌入服务配置
	config.EmbeddingEnabled = json.Get("embedding_enabled").Bool()
	config.EmbeddingDomain = json.Get("embedding_domain").String()
	config.EmbeddingPort = int(json.Get("embedding_port").Int())
	config.EmbeddingAPIPath = json.Get("embedding_api_path").String()
	config.EmbeddingModel = json.Get("embedding_model").String()
	config.EmbeddingTimeout = int(json.Get("embedding_timeout").Int())

	// 设置默认值
	if config.DenyCode == 0 {
		config.DenyCode = 403
	}
	if config.DenyMessage == "" {
		config.DenyMessage = "请求被安全策略拦截"
	}
	if config.CacheTTL == 0 {
		config.CacheTTL = 300
	}
	if config.SimilarityMode == "" {
		config.SimilarityMode = "hybrid"
	}

	log.Infof("Plugin config loaded: check_request=%v, similarity_mode=%s", 
		config.CheckRequest, config.SimilarityMode)

	return nil
}

// ============ 规则加载 ============

// loadRulesFromAPI 从外部 API 加载规则
func loadRulesFromAPI() error {
	proxywasm.LogInfo("[Rules] Starting to load rules from API...")
	
	// 获取规则服务配置
	domain := config.getRuleServiceDomain()
	port := config.getRuleServicePort()
	path := config.getRuleServicePath()
	
	proxywasm.LogInfof("[Rules] API endpoint: %s:%d%s", domain, port, path)
	
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	// 使用 proxywasm 进行 HTTP 调用
	headers := [][2]string{
		{"Content-Type", "application/json"},
	}

	proxywasm.LogDebug("[Rules] Dispatching HTTP call to rules API...")
	_, err := proxywasm.DispatchHttpCall(
		domain,
		headers,
		nil,
		nil,
		APICallTimeout,
		func(numHeaders, bodySize, numTrailers int) {
			proxywasm.LogDebugf("[Rules] HTTP call completed: headers=%d, body=%d, trailers=%d", 
				numHeaders, bodySize, numTrailers)
			
			respBody, getErr := proxywasm.GetHttpCallResponseBody(0, bodySize)
			if getErr != nil {
				proxywasm.LogErrorf("[Rules] Failed to get response body: %v", getErr)
				return
			}

			proxywasm.LogDebugf("[Rules] Response body size: %d bytes", len(respBody))
			
			// 解析响应
			if err := json.Unmarshal(respBody, rulesCache); err != nil {
				proxywasm.LogErrorf("[Rules] Failed to parse API response: %v", err)
				proxywasm.LogDebugf("[Rules] Raw response: %s", string(respBody[:min(len(respBody), 500)]))
				return
			}

			proxywasm.LogInfof("[Rules] Successfully parsed rules: redline=%d, vocabularies=%d, attacks=%d, masking=%d",
				len(rulesCache.Redline), len(rulesCache.Vocabularies), 
				len(rulesCache.Attacks), len(rulesCache.Masking))

			// 如果启用了嵌入服务，预计算所有规则的向量
			if config.isEmbeddingEnabled() {
				proxywasm.LogInfo("[Rules] Start pre-computing rule vectors...")
				go precomputeRuleVectors()
			}

			rulesCache.ExpiresAt = time.Now().Add(rulesCache.TTL)
			proxywasm.LogInfof("[Rules] Rules cache updated successfully, expires at: %s", 
				rulesCache.ExpiresAt.Format("2006-01-02 15:04:05"))
		},
	)

	if err != nil {
		proxywasm.LogErrorf("[Rules] Failed to dispatch HTTP call: %v", err)
		proxywasm.LogErrorf("[Rules] Please check:")
		proxywasm.LogErrorf("[Rules]   1. Domain/Service name is correct: %s", domain)
		proxywasm.LogErrorf("[Rules]   2. Port is accessible: %d", port)
		proxywasm.LogErrorf("[Rules]   3. Service is running and reachable")
		proxywasm.LogErrorf("[Rules]   4. Network policy allows this connection")
		return fmt.Errorf("failed to dispatch http call: %w", err)
	}

	proxywasm.LogInfo("[Rules] HTTP call dispatched successfully, waiting for callback...")
	return nil
}

// min 辅助函数
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isCacheValid 检查缓存是否有效
func isCacheValid() bool {
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()

	if rulesCache == nil {
		return false
	}
	
	return len(rulesCache.Redline) > 0 && time.Now().Before(rulesCache.ExpiresAt)
}

// refreshCacheIfNeeded 如果需要则刷新缓存
func refreshCacheIfNeeded() {
	// 懒初始化：如果缓存未初始化，先初始化
	cacheMutex.Lock()
	if rulesCache == nil {
		rulesCache = &RulesCache{
			TTL: time.Duration(config.CacheTTL) * time.Second,
		}
		regexCache = make(map[string]*regexp.Regexp)
		vectorCache = make(map[string][]float64)
		proxywasm.LogInfo("[Init] Cache structures initialized")
	}
	cacheMutex.Unlock()

	if !isCacheValid() {
		// 异步加载规则
		go func() {
			cacheMutex.Lock()
			// 双重检查，避免重复加载
			if len(rulesCache.Redline) == 0 {
				cacheMutex.Unlock()
				if err := loadRulesFromAPI(); err != nil {
					proxywasm.LogErrorf("Failed to load rules cache: %v", err)
				}
			} else {
				cacheMutex.Unlock()
			}
		}()
	}
}

// precomputeRuleVectors 预计算所有规则的向量（在规则加载后调用）
func precomputeRuleVectors() {
	cacheMutex.RLock()
	rules := rulesCache.Redline
	cacheMutex.RUnlock()

	if len(rules) == 0 {
		return
	}

	proxywasm.LogInfof("Pre-computing vectors for %d rules...", len(rules))
	computedCount := 0
	skippedCount := 0

	for i := range rules {
		rule := &rules[i]

		// 跳过不需要嵌入的规则
		if !rule.UseEmbedding && rule.Question == "" {
			skippedCount++
			continue
		}

		// 如果已经有预计算向量，跳过
		if len(rule.Vector) > 0 {
			skippedCount++
			continue
		}

		// 计算向量并缓存
		vector, err := getEmbedding(rule.Question)
		if err != nil {
			proxywasm.LogWarnf("Failed to compute vector for rule %d: %v", rule.ID, err)
			continue
		}

		// 将向量存储回规则中（需要写锁）
		cacheMutex.Lock()
		rulesCache.Redline[i].Vector = vector
		cacheMutex.Unlock()

		computedCount++
		proxywasm.LogDebugf("Computed vector for rule %d (%d/%d)", rule.ID, computedCount, len(rules))
	}

	proxywasm.LogInfof("Vector pre-computation completed: %d computed, %d skipped, total %d rules",
		computedCount, skippedCount, len(rules))
}

// ============ 安全检查函数 ============

// checkRedlineRules 检查红线知识库规则
func checkRedlineRules(content string) (bool, string) {
	cacheMutex.RLock()
	rules := rulesCache.Redline
	cacheMutex.RUnlock()

	if len(rules) == 0 {
		return false, ""
	}

	// 根据相似度模式选择检测策略
	switch strings.ToLower(config.SimilarityMode) {
	case "regex":
		// 仅使用正则匹配
		return checkRedlineByRegex(content, rules)
	case "embedding":
		// 仅使用语义相似度
		return checkRedlineByEmbedding(content, rules)
	case "hybrid":
		// 混合模式：先正则，后语义
		if blocked, answer := checkRedlineByRegex(content, rules); blocked {
			return true, answer
		}
		return checkRedlineByEmbedding(content, rules)
	default:
		// 默认使用混合模式
		if blocked, answer := checkRedlineByRegex(content, rules); blocked {
			return true, answer
		}
		return checkRedlineByEmbedding(content, rules)
	}
}

// checkRedlineByRegex 基于正则表达式检查红线规则
func checkRedlineByRegex(content string, rules []RedlineRule) (bool, string) {
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}

		// 如果规则指定不使用嵌入模型，则使用正则匹配
		if !rule.UseEmbedding && rule.Question != "" {
			regex, err := compileRegex(rule.Question)
			if err != nil {
				proxywasm.LogWarnf("Invalid regex pattern in redline rule %d: %v", rule.ID, err)
				continue
			}

			if regex.MatchString(content) {
				proxywasm.LogInfof("Redline rule matched (regex): ID=%d, Answer=%s", rule.ID, rule.Answer)
				return true, rule.Answer
			}
		}
	}

	return false, ""
}

// checkRedlineByEmbedding 基于语义相似度检查红线规则
func checkRedlineByEmbedding(content string, rules []RedlineRule) (bool, string) {
	// 如果嵌入服务未启用，跳过
	if !config.isEmbeddingEnabled() {
		return false, ""
	}

	var bestMatch *RedlineRule
	var bestSimilarity float64 = -1

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}

		// 只检查启用了嵌入模型的规则
		if rule.UseEmbedding || rule.Question != "" {
			matched, similarity, err := checkSemanticSimilarity(content, rule)
			if err != nil {
				proxywasm.LogWarnf("Semantic similarity check failed for rule %d: %v", rule.ID, err)
				continue
			}

			if matched && similarity > bestSimilarity {
				bestSimilarity = similarity
				bestMatch = &rule
			}
		}
	}

	if bestMatch != nil {
		proxywasm.LogInfof("Redline rule matched (embedding): ID=%d, Similarity=%.3f, Answer=%s",
			bestMatch.ID, bestSimilarity, bestMatch.Answer)
		return true, bestMatch.Answer
	}

	return false, ""
}

// checkPromptAttacks 检查提示词攻击
func checkPromptAttacks(content string) (bool, string) {
	cacheMutex.RLock()
	attacks := rulesCache.Attacks
	cacheMutex.RUnlock()

	for _, rule := range attacks {
		if rule.Enabled {
			regex, err := compileRegex(rule.RegexPattern)
			if err != nil {
				continue
			}
			if regex.MatchString(content) {
				return true, fmt.Sprintf("Prompt injection detected: %s", rule.Description)
			}
		}
	}

	return false, ""
}

// checkSecurityVocabularies 检查安全词表
func checkSecurityVocabularies(content string) (bool, string) {
	cacheMutex.RLock()
	vocabs := rulesCache.Vocabularies
	cacheMutex.RUnlock()

	for _, vocab := range vocabs {
		if vocab.Enabled {
			regex, err := compileRegex(vocab.RegexPattern)
			if err != nil {
				continue
			}
			if regex.MatchString(content) {
				return true, vocab.Answer
			}
		}
	}

	return false, ""
}

// maskSensitiveData 脱敏敏感数据
func maskSensitiveData(content string) string {
	cacheMutex.RLock()
	maskingRules := rulesCache.Masking
	cacheMutex.RUnlock()

	result := content
	for _, rule := range maskingRules {
		if rule.Enabled {
			regex, err := compileRegex(rule.RegexPattern)
			if err != nil {
				continue
			}
			result = regex.ReplaceAllString(result, rule.MaskPattern)
		}
	}

	return result
}

// compileRegex 编译并缓存正则表达式
func compileRegex(pattern string) (*regexp.Regexp, error) {
	regexMutex.RLock()
	if regex, ok := regexCache[pattern]; ok {
		regexMutex.RUnlock()
		return regex, nil
	}
	regexMutex.RUnlock()

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	regexMutex.Lock()
	regexCache[pattern] = regex
	regexMutex.Unlock()

	return regex, nil
}

// ============ 向量化与相似度计算 ============

// getEmbedding 获取文本的向量表示
func getEmbedding(text string) ([]float64, error) {
	// 检查缓存
	vectorMutex.RLock()
	if vector, ok := vectorCache[text]; ok {
		vectorMutex.RUnlock()
		proxywasm.LogDebugf("[Embedding] Cache hit for text: %s", text[:min(len(text), 50)])
		return vector, nil
	}
	vectorMutex.RUnlock()

	// 如果嵌入服务未启用，返回错误
	if !config.isEmbeddingEnabled() {
		return nil, fmt.Errorf("embedding service is not enabled")
	}

	proxywasm.LogInfof("[Embedding] Getting embedding for text (length=%d)", len(text))

	// 构建请求体
	requestBody := map[string]interface{}{
		"model": config.getEmbeddingModel(),
		"input": text,
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 调用嵌入服务
	headers := [][2]string{
		{"Content-Type", "application/json"},
	}

	domain := config.getEmbeddingDomain()
	port := config.getEmbeddingPort()
	path := config.getEmbeddingAPIPath()
	timeout := config.getEmbeddingTimeout()

	proxywasm.LogInfof("[Embedding] Calling embedding service: %s:%d%s", domain, port, path)

	var resultVector []float64
	var callErr error

	// 使用通道等待异步回调完成
	done := make(chan bool, 1)

	_, err = proxywasm.DispatchHttpCall(
		domain,
		headers,
		bodyBytes,
		nil,
		uint32(timeout),
		func(numHeaders, bodySize, numTrailers int) {
			proxywasm.LogDebugf("[Embedding] HTTP call completed: headers=%d, body=%d", numHeaders, bodySize)
			
			respBody, getErr := proxywasm.GetHttpCallResponseBody(0, bodySize)
			if getErr != nil {
				callErr = fmt.Errorf("failed to get response body: %v", getErr)
				proxywasm.LogErrorf("[Embedding] %v", callErr)
				done <- true
				return
			}

			proxywasm.LogDebugf("[Embedding] Response size: %d bytes", len(respBody))

			// 解析响应 - 假设返回格式为 {"data": [{"embedding": [...]}]}
			var embeddingResp map[string]interface{}
			if err := json.Unmarshal(respBody, &embeddingResp); err != nil {
				callErr = fmt.Errorf("failed to parse embedding response: %v", err)
				proxywasm.LogErrorf("[Embedding] %v", callErr)
				proxywasm.LogDebugf("[Embedding] Raw response: %s", string(respBody[:min(len(respBody), 500)]))
				done <- true
				return
			}

			// 提取向量数据
			if data, ok := embeddingResp["data"].([]interface{}); ok && len(data) > 0 {
				if item, ok := data[0].(map[string]interface{}); ok {
					if embedding, ok := item["embedding"].([]interface{}); ok {
						vec := make([]float64, len(embedding))
						for i, v := range embedding {
							if fval, ok := v.(float64); ok {
								vec[i] = fval
							}
						}
						resultVector = vec
						proxywasm.LogDebugf("[Embedding] Successfully extracted vector (dim=%d)", len(vec))
					} else {
						callErr = fmt.Errorf("invalid embedding format")
						proxywasm.LogErrorf("[Embedding] %v", callErr)
					}
				} else {
					callErr = fmt.Errorf("invalid data format")
					proxywasm.LogErrorf("[Embedding] %v", callErr)
				}
			} else {
				callErr = fmt.Errorf("no embedding data in response")
				proxywasm.LogErrorf("[Embedding] %v", callErr)
			}

			done <- true
		},
	)

	if err != nil {
		proxywasm.LogErrorf("[Embedding] Failed to dispatch HTTP call: %v", err)
		return nil, fmt.Errorf("failed to dispatch embedding call: %w", err)
	}

	// 等待回调完成
	<-done

	if callErr != nil {
		return nil, callErr
	}

	if resultVector == nil {
		return nil, fmt.Errorf("failed to get embedding vector")
	}

	// 缓存向量
	vectorMutex.Lock()
	vectorCache[text] = resultVector
	vectorMutex.Unlock()

	proxywasm.LogDebug("[Embedding] Vector cached successfully")
	return resultVector, nil
}

// cosineSimilarity 计算两个向量的余弦相似度
func cosineSimilarity(vec1, vec2 []float64) float64 {
	if len(vec1) != len(vec2) || len(vec1) == 0 {
		return 0.0
	}

	var dotProduct float64
	var norm1, norm2 float64

	for i := range vec1 {
		dotProduct += vec1[i] * vec2[i]
		norm1 += vec1[i] * vec1[i]
		norm2 += vec2[i] * vec2[i]
	}

	norm1 = math.Sqrt(norm1)
	norm2 = math.Sqrt(norm2)

	if norm1 == 0 || norm2 == 0 {
		return 0.0
	}

	return dotProduct / (norm1 * norm2)
}

// checkSemanticSimilarity 基于语义相似度检查红线规则
func checkSemanticSimilarity(content string, rule RedlineRule) (bool, float64, error) {
	// 获取用户输入的向量
	contentVec, err := getEmbedding(content)
	if err != nil {
		return false, 0, fmt.Errorf("failed to get content embedding: %w", err)
	}

	// 获取规则问题的向量（优先使用预计算的向量）
	var ruleVec []float64
	if len(rule.Vector) > 0 {
		ruleVec = rule.Vector
	} else {
		ruleVec, err = getEmbedding(rule.Question)
		if err != nil {
			return false, 0, fmt.Errorf("failed to get rule embedding: %w", err)
		}
	}

	// 计算余弦相似度
	similarity := cosineSimilarity(contentVec, ruleVec)

	// 判断是否超过阈值
	threshold := rule.Threshold
	if threshold == 0 {
		threshold = DefaultSimilarityThreshold
	}

	return similarity >= threshold, similarity, nil
}

// ============ HTTP 请求处理 ============

func onHttpRequestHeaders(ctx wrapper.HttpContext, config PluginConfig, log logs.Log) types.Action {
	if !config.CheckRequest {
		return types.HeaderContinue
	}

	// 检查缓存是否过期
	refreshCacheIfNeeded()

	return types.HeaderContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config PluginConfig, body []byte, log logs.Log) types.Action {
	if !config.CheckRequest {
		return types.ActionContinue
	}

	bodyStr := string(body)

	// 1. 检查红线知识库（最高优先级）
	if blocked, answer := checkRedlineRules(bodyStr); blocked {
		err := proxywasm.SendHttpResponse(
			uint32(config.DenyCode),
			[][2]string{
				{"Content-Type", "application/json"},
			},
			[]byte(fmt.Sprintf(`{"error": "%s", "reason": "Redline Rule Violated"}`, answer)),
			GRPCStatusNotUsed,
		)
		if err != nil {
			log.Errorf("Failed to send HTTP response: %v", err)
		}
		return types.ActionPause
	}

	// 2. 检查提示词攻击
	if blocked, _ := checkPromptAttacks(bodyStr); blocked {
		err := proxywasm.SendHttpResponse(
			uint32(config.DenyCode),
			[][2]string{
				{"Content-Type", "application/json"},
			},
			[]byte(fmt.Sprintf(`{"error": "%s", "reason": "Prompt Injection Detected"}`, config.DenyMessage)),
			GRPCStatusNotUsed,
		)
		if err != nil {
			log.Errorf("Failed to send HTTP response: %v", err)
		}
		return types.ActionPause
	}

	// 3. 检查安全词表
	if blocked, answer := checkSecurityVocabularies(bodyStr); blocked {
		err := proxywasm.SendHttpResponse(
			uint32(config.DenyCode),
			[][2]string{
				{"Content-Type", "application/json"},
			},
			[]byte(fmt.Sprintf(`{"error": "%s", "reason": "Security Vocabulary Matched"}`, answer)),
			GRPCStatusNotUsed,
		)
		if err != nil {
			log.Errorf("Failed to send HTTP response: %v", err)
		}
		return types.ActionPause
	}

	// 4. 数据脱敏
	maskedBody := maskSensitiveData(bodyStr)
	if maskedBody != bodyStr {
		if err := proxywasm.ReplaceHttpRequestBody([]byte(maskedBody)); err != nil {
			log.Errorf("Failed to replace request body: %v", err)
		}
	}

	return types.ActionContinue
}

// ============ HTTP 响应处理 ============

func onHttpResponseBody(ctx wrapper.HttpContext, config PluginConfig, body []byte, log logs.Log) types.Action {
	if !config.CheckResponse {
		return types.ActionContinue
	}

	bodyStr := string(body)

	// 响应侧脱敏
	maskedBody := maskSensitiveData(bodyStr)
	if maskedBody != bodyStr {
		if err := proxywasm.ReplaceHttpResponseBody([]byte(maskedBody)); err != nil {
			log.Errorf("Failed to replace response body: %v", err)
		}
	}

	return types.ActionContinue
}

