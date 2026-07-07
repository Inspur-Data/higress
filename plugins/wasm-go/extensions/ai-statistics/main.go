package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/tokenusage"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

const (
	// Envoy log levels
	LogLevelTrace = iota
	LogLevelDebug
	LogLevelInfo
	LogLevelWarn
	LogLevelError
	LogLevelCritical
)

func main() {}

func init() {
	fmt.Print("ai-statistics start")
	wrapper.SetCtx(
		"ai-statistics",
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		wrapper.ProcessStreamingResponseBody(onHttpStreamingBody),
		wrapper.ProcessResponseBody(onHttpResponseBody),
		wrapper.WithRebuildAfterRequests[AIStatisticsConfig](1000),
		wrapper.WithRebuildMaxMemBytes[AIStatisticsConfig](200*1024*1024),
	)
}

const (
	defaultMaxBodyBytes uint32 = 100 * 1024 * 1024
	// Context consts
	StatisticsRequestStartTime = "ai-statistics-request-start-time"
	StatisticsFirstTokenTime   = "ai-statistics-first-token-time"
	CtxGeneralAtrribute        = "attributes"
	CtxLogAtrribute            = "logAttributes"
	CtxStreamingBodyBuffer     = "streamingBodyBuffer"
	CtxStreamingReasoning      = "streamingReasoning"
	CtxStreamingToolCallsData  = "streamingToolCallsData"
	CtxStreamingFuncCallData   = "streamingFuncCallData"
	RouteName                  = "route"
	ClusterName                = "cluster"
	APIName                    = "api"
	ConsumerKey                = "x-mse-consumer"
	CtxConsumerValue           = "ai_statistics_consumer_value"
	RequestPath                = "request_path"
	SkipProcessing             = "skip_processing"

	// Session ID related
	SessionID = "session_id"

	// AI API Paths
	PathOpenAIChatCompletions       = "/v1/chat/completions"
	PathOpenAICompletions           = "/v1/completions"
	PathOpenAIEmbeddings            = "/v1/embeddings"
	PathOpenAIModels                = "/v1/models"
	PathGeminiGenerateContent       = "/generateContent"
	PathGeminiStreamGenerateContent = "/streamGenerateContent"

	// Source Type
	FixedValue            = "fixed_value"
	RequestHeader         = "request_header"
	RequestBody           = "request_body"
	ResponseHeader        = "response_header"
	ResponseStreamingBody = "response_streaming_body"
	ResponseBody          = "response_body"
	SourceIP              = "source_ip"

	// Inner metric & log attributes
	LLMFirstTokenDuration  = "llm_first_token_duration"
	LLMServiceDuration     = "llm_service_duration"
	LLMDurationCount       = "llm_duration_count"
	LLMStreamDurationCount = "llm_stream_duration_count"
	ResponseType           = "response_type"
	ChatID                 = "chat_id"
	ChatRound              = "chat_round"

	// Inner span attributes
	ArmsSpanKind     = "gen_ai.span.kind"
	ArmsModelName    = "gen_ai.model_name"
	ArmsRequestModel = "gen_ai.request.model"
	ArmsInputToken   = "gen_ai.usage.input_tokens"
	ArmsOutputToken  = "gen_ai.usage.output_tokens"
	ArmsTotalToken   = "gen_ai.usage.total_tokens"

	// Extract Rule
	RuleFirst   = "first"
	RuleReplace = "replace"
	RuleAppend  = "append"

	// Built-in attributes
	BuiltinQuestionKey        = "question"
	BuiltinAnswerKey          = "answer"
	BuiltinToolCallsKey       = "tool_calls"
	BuiltinReasoningKey       = "reasoning"
	BuiltinFunctionCallKey    = "function_call"
	BuiltinSystemKey          = "system"
	BuiltinReasoningTokens    = "reasoning_tokens"
	BuiltinCachedTokens       = "cached_tokens"
	BuiltinInputTokenDetails  = "input_token_details"
	BuiltinOutputTokenDetails = "output_token_details"

	// Built-in attribute paths
	QuestionPathOpenAI = "messages.@reverse.0.content"
	QuestionPathClaude = "messages.@reverse.0.content"

	SystemPathClaude = "system"

	AnswerPathOpenAINonStreaming = "choices.0.message.content"
	AnswerPathClaudeNonStreaming = "content.0.text"

	AnswerPathOpenAIStreaming = "choices.0.delta.content"
	AnswerPathClaudeStreaming = "delta.text"

	// 完整 message 路径，用于工具调用场景提取 content + tool_calls + reasoning
	AnswerPathOpenAIMessage       = "choices.0.message"
	AnswerPathOpenAIMessageRole   = "choices.0.message.role"
	AnswerPathOpenAIMessageDelta  = "choices.0.delta"

	ToolCallsPathNonStreaming = "choices.0.message.tool_calls"
	ToolCallsPathStreaming    = "choices.0.delta.tool_calls"

	ClaudeEventType         = "type"
	ClaudeContentBlockType  = "content_block.type"
	ClaudeContentBlockID    = "content_block.id"
	ClaudeContentBlockName  = "content_block.name"
	ClaudeContentBlockInput = "content_block.input"
	ClaudeDeltaPartialJSON  = "delta.partial_json"
	ClaudeIndex             = "index"

	ReasoningPathNonStreaming      = "choices.0.message.reasoning"
	ReasoningPathNonStreamingAlt   = "choices.0.message.reasoning_content"
	ReasoningPathStreaming         = "choices.0.delta.reasoning"
	ReasoningPathStreamingAlt      = "choices.0.delta.reasoning_content"

	FunctionCallPathNonStreaming   = "choices.0.message.function_call"
	FunctionCallPathStreamingName  = "choices.0.delta.function_call.name"
	FunctionCallPathStreamingArgs  = "choices.0.delta.function_call.arguments"

	CtxStreamingToolCallsBuffer = "streamingToolCallsBuffer"

	CtxAILogOutput           = "ai_log_already_output"
	CtxRequestStartTimeSaved = "ai_statistics_request_start_time_saved"

	ResponseStatusCode          = "ai_statistics_response_status_code"
	CtxFailureCodeDetails       = "ai_statistics_failure_code_details"
	CtxUpstreamTransportFailure = "ai_statistics_upstream_transport_failure"
	CtxBackendUpstreamAddress   = "ai_statistics_backend_upstream_address"
	CtxFailureReason            = "ai_statistics_failure_reason"
	CtxIsFallbackRoute          = "ai_statistics_is_fallback_route"

	// DefaultMaxLogBodyBytes 默认 6KB。
	// 实测：log-pilot 单条日志上限 8KB，access log 其他字段约 2-3KB，
	// 因此 ai_log 必须控制在 6KB 以内，确保单条总日志不超过 8KB。
	DefaultMaxLogBodyBytes = 6 * 1024
	// DefaultMaxAttributeBytes 默认 1536B (1.5KB)。
	// 单个属性（question/answer/messages）最多保留 1.5KB，
	// 超长时先替换多模态占位符，再两端截断。
	DefaultMaxAttributeBytes = 1536

	// Embedding & Rerank paths
	QuestionPathEmbedding = "input"
	QuestionPathRerank    = "query"
	RerankDocumentsPath   = "documents"

	AnswerPathEmbeddingData = "data"
	AnswerPathRerank        = "results"
)

func getDefaultAttributes() []Attribute {
	return []Attribute{
		{
			Key:         "messages",
			ValueSource: RequestBody,
			Value:       "messages",
			ApplyToLog:  true,
		},
		{
			Key:        BuiltinQuestionKey,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinSystemKey,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinAnswerKey,
			ApplyToLog: true,
			Rule:       RuleAppend,
		},
		{
			Key:        BuiltinReasoningKey,
			ApplyToLog: true,
			Rule:       RuleAppend,
		},
		{
			Key:        BuiltinToolCallsKey,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinReasoningTokens,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinCachedTokens,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinInputTokenDetails,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinOutputTokenDetails,
			ApplyToLog: true,
		},
	}
}

func getDefaultResponseAttributes() []Attribute {
	return []Attribute{
		{
			Key:        BuiltinReasoningTokens,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinCachedTokens,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinInputTokenDetails,
			ApplyToLog: true,
		},
		{
			Key:        BuiltinOutputTokenDetails,
			ApplyToLog: true,
		},
	}
}

var defaultSessionHeaders = []string{
	"x-openclaw-session-key",
	"x-clawdbot-session-key",
	"x-moltbot-session-key",
	"x-agent-session",
}

func extractSessionId(customHeader string) string {
	if customHeader != "" {
		if sessionId, _ := proxywasm.GetHttpRequestHeader(customHeader); sessionId != "" {
			return sessionId
		}
	}
	for _, header := range defaultSessionHeaders {
		if sessionId, _ := proxywasm.GetHttpRequestHeader(header); sessionId != "" {
			return sessionId
		}
	}
	return ""
}

type ToolCall struct {
	Index    int              `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function,omitempty"`
}

type ToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type StreamingToolCallsBuffer struct {
	ToolCalls       map[int]*ToolCall
	InToolBlock     map[int]bool
	ArgumentsBuffer map[int]string
}

func extractStreamingToolCalls(data []byte, buffer *StreamingToolCallsBuffer) *StreamingToolCallsBuffer {
	if buffer == nil {
		buffer = &StreamingToolCallsBuffer{
			ToolCalls:       make(map[int]*ToolCall),
			InToolBlock:     make(map[int]bool),
			ArgumentsBuffer: make(map[int]string),
		}
	}

	chunks := bytes.Split(bytes.TrimSpace(wrapper.UnifySSEChunk(data)), []byte("\n\n"))
	for _, chunk := range chunks {
		toolCallsResult := gjson.GetBytes(chunk, ToolCallsPathStreaming)
		if !toolCallsResult.Exists() || !toolCallsResult.IsArray() {
			continue
		}

		for _, tcResult := range toolCallsResult.Array() {
			index := int(tcResult.Get("index").Int())

			tc, exists := buffer.ToolCalls[index]
			if !exists {
				tc = &ToolCall{Index: index}
				buffer.ToolCalls[index] = tc
			}

			if id := tcResult.Get("id").String(); id != "" {
				tc.ID = id
			}
			if tcType := tcResult.Get("type").String(); tcType != "" {
				tc.Type = tcType
			}
			if funcName := tcResult.Get("function.name").String(); funcName != "" {
				tc.Function.Name = funcName
			}
			if args := tcResult.Get("function.arguments").String(); args != "" {
				tc.Function.Arguments += args
			}
		}
	}

	return buffer
}

func extractClaudeStreamingToolCalls(data []byte, buffer *StreamingToolCallsBuffer) *StreamingToolCallsBuffer {
	if buffer == nil {
		buffer = &StreamingToolCallsBuffer{
			ToolCalls:       make(map[int]*ToolCall),
			InToolBlock:     make(map[int]bool),
			ArgumentsBuffer: make(map[int]string),
		}
	}

	chunks := bytes.Split(bytes.TrimSpace(wrapper.UnifySSEChunk(data)), []byte("\n\n"))
	for _, chunk := range chunks {
		eventType := gjson.GetBytes(chunk, ClaudeEventType)
		if !eventType.Exists() {
			continue
		}

		switch eventType.String() {
		case "content_block_start":
			contentBlockType := gjson.GetBytes(chunk, ClaudeContentBlockType)
			if contentBlockType.Exists() && contentBlockType.String() == "tool_use" {
				index := int(gjson.GetBytes(chunk, ClaudeIndex).Int())

				tc := &ToolCall{Index: index}

				if id := gjson.GetBytes(chunk, ClaudeContentBlockID).String(); id != "" {
					tc.ID = id
				}
				if name := gjson.GetBytes(chunk, ClaudeContentBlockName).String(); name != "" {
					tc.Function.Name = name
				}
				tc.Type = "tool_use"

				buffer.ToolCalls[index] = tc
				buffer.InToolBlock[index] = true
				buffer.ArgumentsBuffer[index] = ""

				if input := gjson.GetBytes(chunk, ClaudeContentBlockInput); input.Exists() {
					if inputMap, ok := input.Value().(map[string]interface{}); ok {
						if jsonBytes, err := json.Marshal(inputMap); err == nil {
							buffer.ArgumentsBuffer[index] = string(jsonBytes)
						}
					}
				}
			}

		case "content_block_delta":
			index := int(gjson.GetBytes(chunk, ClaudeIndex).Int())
			if buffer.InToolBlock[index] {
				partialJSON := gjson.GetBytes(chunk, ClaudeDeltaPartialJSON)
				if partialJSON.Exists() {
					buffer.ArgumentsBuffer[index] += partialJSON.String()
				}
			}

		case "content_block_stop":
			index := int(gjson.GetBytes(chunk, ClaudeIndex).Int())
			if buffer.InToolBlock[index] {
				buffer.InToolBlock[index] = false

				if tc, exists := buffer.ToolCalls[index]; exists {
					tc.Function.Arguments = buffer.ArgumentsBuffer[index]
				}
			}
		}
	}

	return buffer
}

func getToolCallsFromBuffer(buffer *StreamingToolCallsBuffer) []ToolCall {
	if buffer == nil || len(buffer.ToolCalls) == 0 {
		return nil
	}

	maxIndex := 0
	for idx := range buffer.ToolCalls {
		if idx > maxIndex {
			maxIndex = idx
		}
	}

	result := make([]ToolCall, 0, len(buffer.ToolCalls))
	for i := 0; i <= maxIndex; i++ {
		if tc, exists := buffer.ToolCalls[i]; exists {
			result = append(result, *tc)
		}
	}
	return result
}

type AILogRecord struct {
	Timestamp             string `json:"@timestamp"`
	RequestID             string `json:"request_id,omitempty"`
	RequestSuccess        bool   `json:"request_success"`
	StatusCode            int    `json:"status_code"`
	Route                 string `json:"route"`
	Cluster               string `json:"cluster"`
	Model                 string `json:"model"`
	Consumer              string `json:"consumer"`
	SourceIP              string `json:"source_ip"`
	SessionID             string `json:"session_id,omitempty"`
	ResponseType          string `json:"response_type,omitempty"`
	LLMServiceDuration    int64  `json:"llm_service_duration,omitempty"`
	LLMFirstTokenDuration int64  `json:"llm_first_token_duration,omitempty"`
	RequestMethod         string `json:"request_method,omitempty"`
	RequestPath           string `json:"request_path,omitempty"`
	AILog                 map[string]interface{} `json:"ai_log,omitempty"`
	PodName                string `json:"gateway_pod_name"`
	BackendModelCluster    string `json:"backend_model_cluster"`
	BackendUpstreamAddress string `json:"backend_upstream_address,omitempty"`
	FailureReason          string `json:"failure_reason,omitempty"`
}

type Attribute struct {
	Key                string `json:"key"`
	ValueSource        string `json:"value_source"`
	Value              string `json:"value"`
	TraceSpanKey       string `json:"trace_span_key,omitempty"`
	DefaultValue       string `json:"default_value,omitempty"`
	Rule               string `json:"rule,omitempty"`
	ApplyToLog         bool   `json:"apply_to_log,omitempty"`
	ApplyToSpan        bool   `json:"apply_to_span,omitempty"`
	AsSeparateLogField bool   `json:"as_separate_log_field,omitempty"`
}

type AIStatisticsConfig struct {
	counterMetrics            map[string]proxywasm.MetricCounter
	attributes                []Attribute
	shouldBufferStreamingBody bool
	shouldBufferRequestBody   bool
	disableOpenaiUsage        bool
	valueLengthLimit          int
	enablePathSuffixes          []string
	enableContentTypes        []string
	sessionIdHeader           string
	RedisClient               wrapper.RedisClient
	maxLogBodyBytes           int
	maxAttributeBytes         int
}

func generateMetricName(route, cluster, model, consumer, sourceIP, metricName string) string {
	return fmt.Sprintf("route.%s.upstream.%s.model.%s.consumer.%s.srcip.%s.metric.%s", route, cluster, model, consumer, sourceIP, metricName)
}

func getRouteName() (string, error) {
	if raw, err := proxywasm.GetProperty([]string{"route_name"}); err != nil {
		return "-", err
	} else {
		return string(raw), nil
	}
}

func getAPIName() (string, error) {
	if raw, err := proxywasm.GetProperty([]string{"route_name"}); err != nil {
		return "-", err
	} else {
		parts := strings.Split(string(raw), "@")
		if len(parts) != 5 {
			return "-", errors.New("not api type")
		} else {
			return strings.Join(parts[:3], "@"), nil
		}
	}
}

// extractModelFromURLPath 从 URL path 中尝试提取 model 名称。
// 支持 Gemini API 格式: /v1/models/{model}:generateContent
// 对于 OpenAI 格式 /v1/chat/completions，model 在 body 中，返回空字符串。
func extractModelFromURLPath(requestPath string) string {
	if requestPath == "" {
		return ""
	}
	// Gemini API: /v1/models/{model}:generateContent 或 /v1/models/{model}:streamGenerateContent
	if strings.Contains(requestPath, "/models/") {
		reg := regexp.MustCompile(`^.*/models/([^:]+):\w+Content$`)
		matches := reg.FindStringSubmatch(requestPath)
		if len(matches) == 2 {
			return matches[1]
		}
	}
	return ""
}

func getClusterName() (string, error) {
	if raw, err := proxywasm.GetProperty([]string{"cluster_name"}); err != nil {
		return "-", err
	} else {
		return string(raw), nil
	}
}

// getConsumerFromRequest 尝试从请求中获取consumer标识。
// 优先从 x-mse-consumer 请求头获取（由consumer-auth插件设置），
// 如果不存在则尝试从 Authorization 头的Bearer token中提取。
func getConsumerFromRequest() string {
	// 1. 优先读取 x-mse-consumer 头（consumer-auth插件认证后设置）
	if consumer, _ := proxywasm.GetHttpRequestHeader(ConsumerKey); consumer != "" {
		log.Infof("[AI-STATISTICS-DEBUG] getConsumerFromRequest: found from %s header: %s", ConsumerKey, consumer)
		return consumer
	}

	// 2. Fallback: 从 Authorization 头解析 Bearer token（尝试多种大小写变体）
	// Envoy WASM SDK中header查找可能区分大小写，需尝试所有可能的形式
	authHeaders := []string{"authorization", "Authorization", "AUTHORIZATION"}
	for _, headerName := range authHeaders {
		auth, err := proxywasm.GetHttpRequestHeader(headerName)
		if err != nil {
			log.Debugf("[AI-STATISTICS-DEBUG] getConsumerFromRequest: reading %s header failed: %v", headerName, err)
			continue
		}
		if auth == "" {
			continue
		}
		auth = strings.TrimSpace(auth)
		log.Infof("[AI-STATISTICS-DEBUG] getConsumerFromRequest: found %s header, value length=%d", headerName, len(auth))
		// 支持 "Bearer <token>" 和 "bearer <token>" 格式
		lowerAuth := strings.ToLower(auth)
		if idx := strings.Index(lowerAuth, "bearer "); idx != -1 {
			token := strings.TrimSpace(auth[idx+7:])
			if token != "" {
				log.Infof("[AI-STATISTICS-DEBUG] getConsumerFromRequest: extracted consumer from %s Bearer: %s", headerName, token)
				return token
			}
		}
		// 尝试直接作为consumer值返回（如果auth头不是Bearer格式但整体有值）
		log.Debugf("[AI-STATISTICS-DEBUG] getConsumerFromRequest: %s header is not Bearer format: %s", headerName, auth)
	}

	// 3. 最后尝试：列出所有请求头来调试（仅取前几个字符避免敏感信息泄露）
	if shouldLogDebug() {
		if headers, err := proxywasm.GetHttpRequestHeaders(); err == nil {
			log.Debugf("[AI-STATISTICS-DEBUG] getConsumerFromRequest: total headers=%d", len(headers))
			for _, h := range headers {
				key := strings.ToLower(h[0])
				if key == "authorization" || key == "x-api-key" || key == "x-auth-token" || strings.Contains(key, "auth") {
					valPreview := h[1]
					if len(valPreview) > 20 {
						valPreview = valPreview[:20] + "..."
					}
					log.Debugf("[AI-STATISTICS-DEBUG] getConsumerFromRequest: found auth-related header %s=%s", h[0], valPreview)
				}
			}
		} else {
			log.Debugf("[AI-STATISTICS-DEBUG] getConsumerFromRequest: failed to list headers: %v", err)
		}
	}

	log.Infof("[AI-STATISTICS-DEBUG] getConsumerFromRequest: no consumer found in %s or Authorization header", ConsumerKey)
	return ""
}

func (config *AIStatisticsConfig) incrementCounter(metricName string, inc uint64) {
	if inc == 0 {
		return
	}
	counter, ok := config.counterMetrics[metricName]
	if !ok {
		counter = proxywasm.DefineCounterMetric(metricName)
		config.counterMetrics[metricName] = counter
	}
	counter.Increment(inc)
	if config.RedisClient != nil && config.RedisClient.Ready() {
		redisKeyPrefix := "modelcount." + os.Getenv("POD_NAME") + "."
		err := config.RedisClient.Set(redisKeyPrefix+metricName, counter.Value(), nil)
		if err != nil {
			log.Errorf("failed to execute redis set command: %v", err)
		}
	}
}

func isPathEnabled(requestPath string, enabledSuffixes []string) bool {
	if len(enabledSuffixes) == 0 {
		return true
	}

	pathWithoutQuery := requestPath
	if queryPos := strings.Index(requestPath, "?"); queryPos != -1 {
		pathWithoutQuery = requestPath[:queryPos]
	}

	for _, suffix := range enabledSuffixes {
		if strings.HasSuffix(pathWithoutQuery, suffix) {
			return true
		}
	}
	return false
}

func isContentTypeEnabled(contentType string, enabledContentTypes []string) bool {
	if len(enabledContentTypes) == 0 {
		return true
	}

	for _, enabledType := range enabledContentTypes {
		if strings.Contains(contentType, enabledType) {
			return true
		}
	}
	return false
}

func parseConfig(configJson gjson.Result, config *AIStatisticsConfig) error {
	useDefaultAttributes := configJson.Get("use_default_attributes").Bool()
	useDefaultResponseAttributes := configJson.Get("use_default_response_attributes").Bool()

	attributeConfigs := configJson.Get("attributes").Array()

	if configJson.Get("value_length_limit").Exists() {
		config.valueLengthLimit = int(configJson.Get("value_length_limit").Int())
	} else {
		config.valueLengthLimit = 3 * 1024
	}

	if useDefaultAttributes {
		config.attributes = getDefaultAttributes()
		if !configJson.Get("value_length_limit").Exists() {
			config.valueLengthLimit = 3 * 1024
		}
		log.Infof("Using default attributes configuration")
	} else if useDefaultResponseAttributes {
		config.attributes = getDefaultResponseAttributes()
		if !configJson.Get("value_length_limit").Exists() {
			config.valueLengthLimit = 3 * 1024
		}
		log.Infof("Using default response attributes configuration (lightweight mode)")
	} else {
		config.attributes = make([]Attribute, len(attributeConfigs))
		for i, attributeConfig := range attributeConfigs {
			attribute := Attribute{}
			err := json.Unmarshal([]byte(attributeConfig.Raw), &attribute)
			if err != nil {
				log.Errorf("parse config failed, %v", err)
				return err
			}
			if attribute.Rule != "" && attribute.Rule != RuleFirst && attribute.Rule != RuleReplace && attribute.Rule != RuleAppend {
				return errors.New("value of rule must be one of [nil, first, replace, append]")
			}
			config.attributes[i] = attribute
		}
	}

	for _, attribute := range config.attributes {
		if attribute.ValueSource == RequestBody {
			config.shouldBufferRequestBody = true
		}
		if attribute.ValueSource == ResponseStreamingBody {
			config.shouldBufferStreamingBody = true
		}
		if attribute.ValueSource == "" && isBuiltinAttribute(attribute.Key) {
			defaultSources := getBuiltinAttributeDefaultSources(attribute.Key)
			for _, src := range defaultSources {
				if src == RequestBody {
					config.shouldBufferRequestBody = true
				}
				if src == ResponseStreamingBody && needsBodyBuffering(attribute.Key) {
					config.shouldBufferStreamingBody = true
				}
			}
		}
	}

	config.counterMetrics = make(map[string]proxywasm.MetricCounter)
	config.disableOpenaiUsage = configJson.Get("disable_openai_usage").Bool()

	pathSuffixes := configJson.Get("enable_path_suffixes").Array()
	config.enablePathSuffixes = make([]string, 0, len(pathSuffixes))

	if (useDefaultAttributes || useDefaultResponseAttributes) && !configJson.Get("enable_path_suffixes").Exists() {
		config.enablePathSuffixes = []string{"/completions", "/messages"}
		log.Infof("Using default path suffixes: /completions, /messages")
	} else {
		for _, suffix := range pathSuffixes {
			suffixStr := suffix.String()
			if suffixStr == "*" {
				config.enablePathSuffixes = make([]string, 0)
				break
			}
			config.enablePathSuffixes = append(config.enablePathSuffixes, suffixStr)
		}
	}

	contentTypes := configJson.Get("enable_content_types").Array()
	config.enableContentTypes = make([]string, 0, len(contentTypes))

	for _, contentType := range contentTypes {
		contentTypeStr := contentType.String()
		if contentTypeStr == "*" {
			config.enableContentTypes = make([]string, 0)
			break
		}
		config.enableContentTypes = append(config.enableContentTypes, contentTypeStr)
	}

	if sessionIdHeader := configJson.Get("session_id_header"); sessionIdHeader.Exists() {
		config.sessionIdHeader = sessionIdHeader.String()
	}

	counter1 := proxywasm.DefineCounterMetric("gateway_model_metrics")
	if config.counterMetrics == nil {
		config.counterMetrics = make(map[string]proxywasm.MetricCounter)
	}
	config.counterMetrics["gateway_model_metrics"] = counter1

	config.disableOpenaiUsage = configJson.Get("disable_openai_usage").Bool()

	redisConfig := configJson.Get("redis")
	if !redisConfig.Exists() {
		log.Errorf("redis config not exists error")
		return errors.New("redis config not exists error")
	}
	err := InitRedisClusterClient(redisConfig, config)
	if err != nil {
		log.Errorf("Failed to initialize Redis: %v", err)
		return err
	}
	if config.RedisClient == nil || !config.RedisClient.Ready() {
		log.Errorf("redisClient is not ready")
		return errors.New("redisClient is not ready")
	}

	if configJson.Get("max_log_body_bytes").Exists() {
		config.maxLogBodyBytes = int(configJson.Get("max_log_body_bytes").Int())
	} else {
		config.maxLogBodyBytes = DefaultMaxLogBodyBytes
	}
	if configJson.Get("max_attribute_bytes").Exists() {
		config.maxAttributeBytes = int(configJson.Get("max_attribute_bytes").Int())
	} else {
		config.maxAttributeBytes = DefaultMaxAttributeBytes
	}

	// 安全建议值：log-pilot 单条日志上限 8KB，access log 其他字段约 2-3KB。
	// 如果配置值超过安全建议值，打印 warning 但不强制覆盖（配置优先）。
	const suggestMaxLogBodyBytes = 6 * 1024
	const suggestMaxAttributeBytes = 1536
	const suggestValueLengthLimit = 3 * 1024
	if config.maxLogBodyBytes > suggestMaxLogBodyBytes {
		log.Warnf("max_log_body_bytes=%d exceeds suggested safe value %d, "+
			"log-pilot may truncate logs exceeding ~16KB",
			config.maxLogBodyBytes, suggestMaxLogBodyBytes)
	}
	if config.maxAttributeBytes > suggestMaxAttributeBytes {
		log.Warnf("max_attribute_bytes=%d exceeds suggested safe value %d, "+
			"large fields may cause total log size to exceed limit",
			config.maxAttributeBytes, suggestMaxAttributeBytes)
	}
	if config.valueLengthLimit > suggestValueLengthLimit {
		log.Warnf("value_length_limit=%d exceeds suggested safe value %d, "+
			"base64 data may escape replacement if truncated before processing",
			config.valueLengthLimit, suggestValueLengthLimit)
	}

	// 无效值（≤0）回退到默认值
	if config.maxLogBodyBytes <= 0 {
		config.maxLogBodyBytes = DefaultMaxLogBodyBytes
	}
	if config.maxAttributeBytes <= 0 {
		config.maxAttributeBytes = DefaultMaxAttributeBytes
	}
	if config.valueLengthLimit <= 0 {
		config.valueLengthLimit = suggestValueLengthLimit
	}

	log.Infof("[AI-STAT-DEBUG] parseConfig final: maxLogBodyBytes=%d maxAttributeBytes=%d valueLengthLimit=%d",
		config.maxLogBodyBytes, config.maxAttributeBytes, config.valueLengthLimit)

	return nil
}

func InitRedisClusterClient(redisConfig gjson.Result, config *AIStatisticsConfig) error {
	serviceName := redisConfig.Get("service_name").String()
	if serviceName == "" {
		return errors.New("redis service name must not be empty")
	}

	servicePort := int(redisConfig.Get("service_port").Int())
	if servicePort == 0 {
		if strings.HasSuffix(serviceName, ".static") {
			servicePort = 80
		} else {
			servicePort = 6379
		}
	}

	username := redisConfig.Get("username").String()
	password := redisConfig.Get("password").String()
	timeout := int(redisConfig.Get("timeout").Int())
	if timeout == 0 {
		timeout = 1000
	}

	config.RedisClient = wrapper.NewRedisClusterClient(wrapper.FQDNCluster{
		FQDN: serviceName,
		Port: int64(servicePort),
	})
	database := int(redisConfig.Get("database").Int())
	err := config.RedisClient.Init(username, password, int64(timeout), wrapper.WithDataBase(database))
	if err != nil {
		log.Errorf("redis init failed")
	}

	return err
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config AIStatisticsConfig) types.Action {
	requestPath, _ := proxywasm.GetHttpRequestHeader(":path")
	if !isPathEnabled(requestPath, config.enablePathSuffixes) {
		log.Debugf("ai-statistics: skipping request for path %s (not in enabled suffixes)", requestPath)
		ctx.SetContext(SkipProcessing, true)
		ctx.DontReadRequestBody()
		ctx.DontReadResponseBody()
		return types.ActionContinue
	}

	log.Infof("[AI-STATISTICS-DEBUG] onHttpRequestHeaders: path=%s suffixes=%v", requestPath, config.enablePathSuffixes)

	ctx.DisableReroute()
	route, _ := getRouteName()
	cluster, _ := getClusterName()
	api, apiError := getAPIName()
	if apiError == nil {
		route = api
	}
	ctx.SetContext(RouteName, route)
	ctx.SetContext(ClusterName, cluster)
	ctx.SetUserAttribute(APIName, api)
	ctx.SetContext(StatisticsRequestStartTime, time.Now().UnixMilli())

	// Detect fallback route: cluster name contains "fallback" indicates this is a catch-all route
	if strings.Contains(strings.ToLower(cluster), "fallback") {
		ctx.SetContext(CtxIsFallbackRoute, true)
		log.Infof("[AI-STATISTICS-DEBUG] fallback route detected: cluster=%s", cluster)
	} else {
		ctx.SetContext(CtxIsFallbackRoute, false)
	}

	if requestMethod, _ := proxywasm.GetHttpRequestHeader(":method"); requestMethod != "" {
		ctx.SetUserAttribute("request_method", requestMethod)
		log.Debugf("[AI-LOG] request method recorded: %s", requestMethod)
	}
	if requestPath, _ := proxywasm.GetHttpRequestHeader(":path"); requestPath != "" {
		ctx.SetUserAttribute("request_path", requestPath)
		log.Debugf("[AI-LOG] request path recorded: %s", requestPath)
	}
	if requestPath, _ := proxywasm.GetHttpRequestHeader(":path"); requestPath != "" {
		ctx.SetContext(RequestPath, requestPath)
	}
	consumer := getConsumerFromRequest()
	if consumer != "" {
		// Store consumer in Envoy property (filter state) for reliable cross-phase access.
		if err := proxywasm.SetProperty([]string{"ai_statistics_consumer"}, []byte(consumer)); err != nil {
			log.Warnf("[AI-STATISTICS-DEBUG] failed to set consumer property: %v", err)
		} else {
			log.Infof("[AI-STATISTICS-DEBUG] consumer stored in property: %s", consumer)
		}
		// Backup to context for writeMetric (property may have issues with repeated reads)
		ctx.SetContext(CtxConsumerValue, consumer)
		log.Infof("[AI-STATISTICS-DEBUG] consumer backed up to context: %s", consumer)
		// Pre-build ai_log with consumer in request phase so 500/error scenarios
		// still have ai_log data even if response-phase GetProperty fails
		appendToAILogFilterState(map[string]interface{}{"consumer": consumer})
		log.Infof("[AI-STATISTICS-DEBUG] consumer pre-written to ai_log filter state")
	} else {
		log.Infof("[AI-STATISTICS-DEBUG] consumer not found in %s or Authorization header", ConsumerKey)
	}

	// Extract model from URL path in request phase (for APIs where model is in path).
	// This ensures model is available even if onHttpRequestBody is not executed
	// (e.g., 500 direct_response scenarios where upstream cluster doesn't exist).
	// Body-phase model extraction will override this if body is available.
	requestPath = ctx.GetStringContext(RequestPath, "")
	urlModel := extractModelFromURLPath(requestPath)
	if urlModel != "" {
		ctx.SetContext(tokenusage.CtxKeyRequestModel, urlModel)
		ctx.SetUserAttribute("model", urlModel)
		ctx.SetUserAttribute(tokenusage.CtxKeyModel, urlModel)
		// Store in property for reliable cross-phase access (same as consumer)
		if err := proxywasm.SetProperty([]string{"ai_statistics_model"}, []byte(urlModel)); err != nil {
			log.Warnf("[AI-STATISTICS-DEBUG] failed to set model property: %v", err)
		}
		appendToAILogFilterState(map[string]interface{}{"model": urlModel})
		log.Infof("[AI-STATISTICS-DEBUG] model extracted from URL path: %s", urlModel)
	} else {
		log.Debugf("[AI-STATISTICS-DEBUG] model not found in URL path: %s", requestPath)
	}

	ctx.SetRequestBodyBufferLimit(defaultMaxBodyBytes)

	sessionId := extractSessionId(config.sessionIdHeader)
	if sessionId != "" {
		ctx.SetUserAttribute(SessionID, sessionId)
	}

	setSpanAttribute(ArmsSpanKind, "LLM")
	log.Debugf("ai-statistics start onHttpRequestHeaders/setAttributeBySource/SOURCEIP")
	setAttributeBySource(ctx, config, SourceIP, nil)
	log.Debugf("ai-statistics end onHttpRequestHeaders/setAttributeBySource/SOURCEIP")
	setAttributeBySource(ctx, config, FixedValue, nil)
	setAttributeBySource(ctx, config, RequestHeader, nil)

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config AIStatisticsConfig, body []byte) types.Action {
	if ctx.GetBoolContext(SkipProcessing, false) {
		log.Infof("[AI-STATISTICS-DEBUG] onHttpRequestBody: skipped due to SkipProcessing=true")
		return types.ActionContinue
	}

	log.Infof("[AI-STATISTICS-DEBUG] onHttpRequestBody: bodySize=%d shouldBuffer=%v", len(body), config.shouldBufferRequestBody)

	if config.shouldBufferRequestBody && len(body) > 0 {
		setAttributeBySource(ctx, config, RequestBody, body)
	}

	requestModel := "UNKNOWN"
	if len(body) > 0 {
		if model := gjson.GetBytes(body, "model"); model.Exists() {
			requestModel = model.String()
		}
	}
	if requestModel == "UNKNOWN" {
		requestPath := ctx.GetStringContext(RequestPath, "")
		if strings.Contains(requestPath, "generateContent") || strings.Contains(requestPath, "streamGenerateContent") {
			reg := regexp.MustCompile(`^.*/(?P<<api_version>[^/]+)/models/(?P<<model>[^:]+):\w+Content$`)
			matches := reg.FindStringSubmatch(requestPath)
			if len(matches) == 3 {
				requestModel = matches[2]
			}
		}
	}
	ctx.SetContext(tokenusage.CtxKeyRequestModel, requestModel)
	ctx.SetUserAttribute("model", requestModel)
	ctx.SetUserAttribute(tokenusage.CtxKeyModel, requestModel)
	log.Debugf("[AI-STATISTICS-DEBUG] model parsed and set to user attribute: %s", requestModel)
	setSpanAttribute(ArmsRequestModel, requestModel)

	// Update property and ai_log filter state with body-extracted model (overrides URL path model)
	if requestModel != "UNKNOWN" {
		if err := proxywasm.SetProperty([]string{"ai_statistics_model"}, []byte(requestModel)); err != nil {
			log.Warnf("[AI-STATISTICS-DEBUG] failed to update model property from body: %v", err)
		}
		appendToAILogFilterState(map[string]interface{}{"model": requestModel})
		log.Infof("[AI-STATISTICS-DEBUG] model updated in property and ai_log from body: %s", requestModel)
	}

	userPromptCount := 0
	if len(body) > 0 {
		if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
			for _, msg := range messages.Array() {
				if msg.Get("role").String() == "user" {
					userPromptCount += 1
				}
			}
		} else if contents := gjson.GetBytes(body, "contents"); contents.Exists() && contents.IsArray() {
			for _, content := range contents.Array() {
				if !content.Get("role").Exists() || content.Get("role").String() == "user" {
					userPromptCount += 1
				}
			}
		}
	}
	ctx.SetUserAttribute(ChatRound, userPromptCount)

	if q := ctx.GetUserAttribute("question"); q != nil {
		log.Infof("[AI-STATISTICS-DEBUG] question parsed: %v", q)
	} else {
		log.Infof("[AI-STATISTICS-DEBUG] question NOT parsed")
	}

	debugLogAiLog(ctx)
	_ = ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)

	// Pre-build ai_log with request-phase data (model, question) so 500/error
	// scenarios still have ai_log data. Response-phase data (answer, tokens)
	// will be added later if response processing succeeds.
	requestPhaseAILog := make(map[string]interface{})
	if requestModel != "UNKNOWN" {
		requestPhaseAILog["model"] = requestModel
	}
	if q := ctx.GetUserAttribute("question"); q != nil {
		requestPhaseAILog["question"] = q
	}
	if len(requestPhaseAILog) > 0 {
		appendToAILogFilterState(requestPhaseAILog)
		log.Infof("[AI-STATISTICS-DEBUG] request-phase data pre-written to ai_log filter state: %v", requestPhaseAILog)
	}

	return types.ActionContinue
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config AIStatisticsConfig) types.Action {
	statusCode, _ := proxywasm.GetHttpResponseHeader(":status")
	if statusCode != "" {
		ctx.SetContext(ResponseStatusCode, statusCode)
	}

	log.Infof("[AI-STATISTICS-DEBUG] onHttpResponseHeaders: status=%s", statusCode)

	if statusCode != "" {
		code, _ := strconv.Atoi(statusCode)
		if code >= 400 {
			log.Infof("[AI-STATISTICS-DEBUG] FAILED request detected: status=%d, collecting failure info", code)

			var codeDetails, transportFailure string
			if cd, err := proxywasm.GetProperty([]string{"response", "code_details"}); err == nil && len(cd) > 0 {
				codeDetails = string(cd)
				ctx.SetContext(CtxFailureCodeDetails, codeDetails)
				log.Debugf("response code_details: %s", codeDetails)
			}
			if tf, err := proxywasm.GetProperty([]string{"upstream", "transport_failure_reason"}); err == nil && len(tf) > 0 {
				transportFailure = string(tf)
				ctx.SetContext(CtxUpstreamTransportFailure, transportFailure)
				log.Debugf("upstream transport_failure_reason: %s", transportFailure)
			}

			var upstreamAddress string
			if ua, err := proxywasm.GetProperty([]string{"upstream", "address"}); err == nil && len(ua) > 0 {
				upstreamAddress = string(ua)
			}
			if upstreamAddress == "" {
				upstreamAddress, _ = proxywasm.GetHttpResponseHeader("x-envoy-upstream-remote-address")
			}
			if upstreamAddress != "" {
				ctx.SetContext(CtxBackendUpstreamAddress, upstreamAddress)
				ctx.SetUserAttribute("backend_upstream_address", upstreamAddress)
				log.Debugf("backend upstream address: %s", upstreamAddress)
			}

			isFallbackRoute := ctx.GetBoolContext(CtxIsFallbackRoute, false)
			failureReason := classifyFailure(statusCode, codeDetails, transportFailure, isFallbackRoute)
			if failureReason != "" {
				ctx.SetContext(CtxFailureReason, failureReason)
				ctx.SetUserAttribute("failure_reason", failureReason)
				log.Infof("request failure classified: status=%s, reason=%s, isFallback=%v", statusCode, failureReason, isFallbackRoute)
			}

			// Set x-mse-consumer response header so access log can read it via %RESP(X-MSE-CONSUMER)%
			// This is needed because in fallback route scenarios, consumer-auth plugin doesn't run
			// and the x-mse-consumer request header is never set.
			if raw, err := proxywasm.GetProperty([]string{"ai_statistics_consumer"}); err == nil && len(raw) > 0 {
				consumerVal := string(raw)
				if err := proxywasm.ReplaceHttpResponseHeader("x-mse-consumer", consumerVal); err != nil {
					log.Warnf("[AI-STATISTICS-DEBUG] failed to set x-mse-consumer response header: %v", err)
				} else {
					log.Infof("[AI-STATISTICS-DEBUG] set x-mse-consumer response header: %s", consumerVal)
				}
			}

			outputAILogFailure(ctx, config)
			ctx.SetContext(CtxAILogOutput, true)
			log.Infof("[AI-STATISTICS-DEBUG] failure log output complete, CtxAILogOutput=true")
		}
	}

	contentType, _ := proxywasm.GetHttpResponseHeader("content-type")

	if !isContentTypeEnabled(contentType, config.enableContentTypes) {
		log.Debugf("ai-statistics: skipping response for content type %s (not in enabled content types)", contentType)
		ctx.SetContext(SkipProcessing, true)
		ctx.DontReadResponseBody()
		return types.ActionContinue
	}

	if !strings.Contains(contentType, "text/event-stream") {
		ctx.BufferResponseBody()
	}

	setAttributeBySource(ctx, config, ResponseHeader, nil)

	return types.ActionContinue
}

func onHttpStreamingBody(ctx wrapper.HttpContext, config AIStatisticsConfig, data []byte, endOfStream bool) []byte {
	if ctx.GetBoolContext(SkipProcessing, false) {
		return data
	}

	if config.shouldBufferStreamingBody {
		streamingBodyBuffer, ok := ctx.GetContext(CtxStreamingBodyBuffer).([]byte)
		if !ok {
			streamingBodyBuffer = data
		} else {
			streamingBodyBuffer = append(streamingBodyBuffer, data...)
		}
		ctx.SetContext(CtxStreamingBodyBuffer, streamingBodyBuffer)
	}

	ctx.SetUserAttribute(ResponseType, "stream")
	if chatID := wrapper.GetValueFromBody(data, []string{
		"id",
		"response.id",
		"responseId",
		"message.id",
	}); chatID != nil {
		ctx.SetUserAttribute(ChatID, chatID.String())
	}

	requestStartTime, ok := ctx.GetContext(StatisticsRequestStartTime).(int64)
	if !ok {
		log.Error("failed to get requestStartTime from http context")
		return data
	}

	if ctx.GetContext(StatisticsFirstTokenTime) == nil {
		firstTokenTime := time.Now().UnixMilli()
		ctx.SetContext(StatisticsFirstTokenTime, firstTokenTime)
		ctx.SetUserAttribute(LLMFirstTokenDuration, firstTokenTime-requestStartTime)
	}

	if !config.disableOpenaiUsage {
		if usage := tokenusage.GetTokenUsage(ctx, data); usage.TotalToken > 0 {
			setSpanAttribute(ArmsTotalToken, usage.TotalToken)
			setSpanAttribute(ArmsModelName, usage.Model)
			setSpanAttribute(ArmsInputToken, usage.InputToken)
			setSpanAttribute(ArmsOutputToken, usage.OutputToken)

			if len(usage.InputTokenDetails) > 0 {
				ctx.SetContext(tokenusage.CtxKeyInputTokenDetails, usage.InputTokenDetails)
			}
			if len(usage.OutputTokenDetails) > 0 {
				ctx.SetContext(tokenusage.CtxKeyOutputTokenDetails, usage.OutputTokenDetails)
			}

			_ = ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)
		}
	}

	// 从每个 chunk 中提取 reasoning / tool_calls / function_call，
	// 缓存到 ctx 中（不是 ai_log filter state），endOfStream 时写入 SetUserAttribute。
	// buildAILogRecord 中的 collectAIAttr 只从 user attribute 读取。
	if reasoning := extractStreamingReasoning(data); reasoning != nil {
		buf, _ := ctx.GetContext(CtxStreamingReasoning).(string)
		ctx.SetContext(CtxStreamingReasoning, buf+fmt.Sprint(reasoning))
		log.Infof("[AI-STAT-DEBUG] streaming reasoning chunk: +%d bytes", len(fmt.Sprint(reasoning)))
	}
	if toolCalls := extractStreamingToolCallsSimple(data); toolCalls != nil {
		buf, _ := ctx.GetContext(CtxStreamingToolCallsData).(string)
		ctx.SetContext(CtxStreamingToolCallsData, buf+fmt.Sprint(toolCalls))
		log.Infof("[AI-STAT-DEBUG] streaming tool_calls chunk: +%d bytes", len(fmt.Sprint(toolCalls)))
	}
	if funcCall := extractStreamingFunctionCall(data); funcCall != nil {
		buf, _ := ctx.GetContext(CtxStreamingFuncCallData).(string)
		ctx.SetContext(CtxStreamingFuncCallData, buf+fmt.Sprint(funcCall))
		log.Infof("[AI-STAT-DEBUG] streaming function_call chunk: +%d bytes", len(fmt.Sprint(funcCall)))
	}

	// 累积结构化 tool_calls buffer，供 extractStreamingMessage 在 endOfStream 时读取完整数据
	var toolCallsBuffer *StreamingToolCallsBuffer
	if existingBuffer, ok := ctx.GetContext(CtxStreamingToolCallsBuffer).(*StreamingToolCallsBuffer); ok {
		toolCallsBuffer = existingBuffer
	}
	toolCallsBuffer = extractStreamingToolCalls(data, toolCallsBuffer)
	toolCallsBuffer = extractClaudeStreamingToolCalls(data, toolCallsBuffer)
	ctx.SetContext(CtxStreamingToolCallsBuffer, toolCallsBuffer)

	if endOfStream {
		responseEndTime := time.Now().UnixMilli()
		ctx.SetUserAttribute(LLMServiceDuration, responseEndTime-requestStartTime)

		// 从 chunk 缓存中读取 reasoning / tool_calls / function_call，
		// 写入 user attribute。collectAIAttr 只从 user attribute 读取。
		if reasoningBuf, ok := ctx.GetContext(CtxStreamingReasoning).(string); ok && reasoningBuf != "" {
			ctx.SetUserAttribute(BuiltinReasoningKey, reasoningBuf)
			log.Infof("[AI-STAT-DEBUG] endOfStream reasoning written: len=%d", len(reasoningBuf))
		}
		if toolCallsBuf, ok := ctx.GetContext(CtxStreamingToolCallsData).(string); ok && toolCallsBuf != "" {
			ctx.SetUserAttribute(BuiltinToolCallsKey, toolCallsBuf)
			log.Infof("[AI-STAT-DEBUG] endOfStream tool_calls written: len=%d", len(toolCallsBuf))
		}
		if funcCallBuf, ok := ctx.GetContext(CtxStreamingFuncCallData).(string); ok && funcCallBuf != "" {
			ctx.SetUserAttribute(BuiltinFunctionCallKey, funcCallBuf)
			log.Infof("[AI-STAT-DEBUG] endOfStream function_call written: len=%d", len(funcCallBuf))
		}

		var streamingBodyBuffer []byte
		if config.shouldBufferStreamingBody {
			streamingBodyBuffer, _ = ctx.GetContext(CtxStreamingBodyBuffer).([]byte)
		}
		setAttributeBySource(ctx, config, ResponseStreamingBody, streamingBodyBuffer)

		debugLogAiLog(ctx)
		_ = ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)

		writeMetric(ctx, config)
		if !ctx.GetBoolContext(CtxAILogOutput, false) {
			log.Debugf("[AI-LOG] streaming body end, outputting success log")
			outputAILog(ctx, config)
		} else {
			log.Debugf("[AI-LOG] streaming body end, skipping duplicate log")
		}
	}
	return data
}

func onHttpResponseBody(ctx wrapper.HttpContext, config AIStatisticsConfig, body []byte) types.Action {
	if ctx.GetBoolContext(SkipProcessing, false) {
		return types.ActionContinue
	}

	requestStartTime, _ := ctx.GetContext(StatisticsRequestStartTime).(int64)

	responseEndTime := time.Now().UnixMilli()
	ctx.SetUserAttribute(LLMServiceDuration, responseEndTime-requestStartTime)

	ctx.SetUserAttribute(ResponseType, "normal")
	if chatID := wrapper.GetValueFromBody(body, []string{
		"id",
		"response.id",
		"responseId",
		"message.id",
	}); chatID != nil {
		ctx.SetUserAttribute(ChatID, chatID.String())
	}

	if !config.disableOpenaiUsage {
		if usage := tokenusage.GetTokenUsage(ctx, body); usage.TotalToken > 0 {
			setSpanAttribute(ArmsModelName, usage.Model)
			setSpanAttribute(ArmsInputToken, usage.InputToken)
			setSpanAttribute(ArmsOutputToken, usage.OutputToken)
			setSpanAttribute(ArmsTotalToken, usage.TotalToken)

			if len(usage.InputTokenDetails) > 0 {
				ctx.SetContext(tokenusage.CtxKeyInputTokenDetails, usage.InputTokenDetails)
			}
			if len(usage.OutputTokenDetails) > 0 {
				ctx.SetContext(tokenusage.CtxKeyOutputTokenDetails, usage.OutputTokenDetails)
			}
		}
	}

	setAttributeBySource(ctx, config, ResponseBody, body)

	debugLogAiLog(ctx)
	_ = ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)

	writeMetric(ctx, config)
	if !ctx.GetBoolContext(CtxAILogOutput, false) {
		log.Debugf("[AI-LOG] response body processed, outputting success log")
		outputAILog(ctx, config)
	} else {
		log.Debugf("[AI-LOG] response body processed, skipping duplicate log")
	}

	return types.ActionContinue
}

func setAttributeBySource(ctx wrapper.HttpContext, config AIStatisticsConfig, source string, body []byte) {
	for _, attribute := range config.attributes {
		var key string
		var value interface{}
		key = attribute.Key

		if !shouldProcessBuiltinAttribute(key, attribute.ValueSource, source) {
			continue
		}

		if attribute.Value != "" {
			switch source {
			case FixedValue:
				value = attribute.Value
			case RequestHeader:
				value, _ = proxywasm.GetHttpRequestHeader(attribute.Value)
			case RequestBody:
				value = gjson.GetBytes(body, attribute.Value).Value()
			case ResponseHeader:
				value, _ = proxywasm.GetHttpResponseHeader(attribute.Value)
			case ResponseStreamingBody:
				// answer 特殊处理：如果配置 value 是 choices.0.delta.content，
				// 调用 extractStreamingMessage 提取 content + reasoning + tool_calls
				if key == BuiltinAnswerKey && attribute.Value == AnswerPathOpenAIStreaming {
					log.Infof("[AI-STAT-DEBUG] setAttr answer using extractStreamingMessage")
					value = extractStreamingMessage(ctx, body, attribute.Rule)
				} else {
					value = extractStreamingBodyByJsonPath(body, attribute.Value, attribute.Rule)
				}
			case ResponseBody:
				// answer 特殊处理：如果配置 value 是 choices.0.message.content，
				// 调用 extractOpenAIMessage 提取 content + reasoning + tool_calls
				if key == BuiltinAnswerKey && attribute.Value == AnswerPathOpenAINonStreaming {
					log.Infof("[AI-STAT-DEBUG] setAttr answer using extractOpenAIMessage")
					value = extractOpenAIMessage(body)
				} else {
					value = gjson.GetBytes(body, attribute.Value).Value()
				}
			case SourceIP:
				value = "unknown"
				if bs, err := proxywasm.GetProperty([]string{"source", "address"}); err == nil {
					rawSource := string(bs)
					sourceIP := parseIP(rawSource)
					if isValidIP(sourceIP) {
						value = sourceIP
						log.Infof("[Check-eBPF] Got Source IP from connection: %s (Raw: %s)", value, rawSource)
					}
				}

				if value == "unknown" {
					if xff, err := proxywasm.GetHttpRequestHeader("X-Forwarded-For"); err == nil && xff != "" {
						ips := strings.Split(xff, ",")
						for _, ip := range ips {
							cleanIP := strings.TrimSpace(ip)
							if isValidIP(cleanIP) {
								value = cleanIP
								log.Debugf("Got IP from XFF: %s", value)
								break
							}
						}
					}
				}

				if value == "" {
					value = "unknown"
				}
			default:
			}
		}

		if (value == nil || value == "") && isBuiltinAttribute(key) {
			value = getBuiltinAttributeFallback(ctx, config, key, source, body, attribute.Rule)
			if value != nil && value != "" {
				log.Debugf("[attribute] Used built-in extraction for %s: %+v", key, value)
			}
		}

		if (value == nil || value == "") && attribute.DefaultValue != "" {
			value = attribute.DefaultValue
		}

		var formattedValue interface{}
		switch v := value.(type) {
		case map[string]int64:
			jsonBytes, err := json.Marshal(v)
			if err != nil {
				log.Warnf("failed to marshal token details: %v", err)
				formattedValue = fmt.Sprint(v)
			} else {
				formattedValue = string(jsonBytes)
			}
		default:
			if value == nil {
				// value 为 nil 时 fmt.Sprint(nil) 返回 "<nil>" 字符串，
				// 会被错误写入 ai_log。直接跳过 nil 值。
				log.Infof("[AI-STAT-DEBUG] setAttr SKIP nil: key=%s", key)
				formattedValue = nil
			} else {
				// 先替换多模态占位符，再检查长度限制。
				// 必须在 valueLengthLimit 截断之前替换，否则截断会破坏
				// data URI 完整性，导致 base64 数据逃过替换。
				strValue := fmt.Sprint(value)
				log.Infof("[AI-STAT-DEBUG] setAttr BEFORE replace: key=%s len=%d vll=%d", key, len(strValue), config.valueLengthLimit)
				strValue = replaceMultimediaWithPlaceholders(strValue)
				log.Infof("[AI-STAT-DEBUG] setAttr AFTER replace: key=%s len=%d", key, len(strValue))
				if len(strValue) > config.valueLengthLimit {
					origLen := len(strValue)
					if key == "question" {
						// question 优先删除多模态占位符，尽量保留完整 text
						cleaned := cleanMultimediaResiduals(strValue)
						if len(cleaned) <= config.valueLengthLimit {
							strValue = cleaned
						} else {
							strValue = cleaned[:config.valueLengthLimit] + "..."
						}
					} else if key != BuiltinAnswerKey {
						// 其他字段（answer 除外）两端保留、中间截断（保留上下文）
						strValue = strValue[:config.valueLengthLimit/2] + "..." + strconv.Itoa(len(strValue)-config.valueLengthLimit) + "B>" + strValue[len(strValue)-config.valueLengthLimit/2:]
					}
					// answer 字段不做 valueLengthLimit 截断，避免破坏内部 JSON 结构；
					// 其长度控制由 buildAILogRecord 中的 summarizeAttribute 统一处理。
					log.Infof("[AI-STAT-DEBUG] setAttr VLL truncate: key=%s %d->%d vll=%d", key, origLen, len(strValue), config.valueLengthLimit)
				}
				formattedValue = strValue
				log.Infof("[AI-STAT-DEBUG] setAttr FINAL: key=%s len=%d type=%T", key, len(fmt.Sprint(formattedValue)), formattedValue)
			}
		}

		log.Debugf("[attribute] source type: %s, key: %s, value: %+v", source, key, formattedValue)
		if attribute.ApplyToLog && formattedValue != nil {
			if attribute.AsSeparateLogField {
				var marshalledJsonStr string
				if _, ok := value.(map[string]int64); ok {
					marshalledJsonStr = fmt.Sprint(formattedValue)
				} else {
					marshalledJsonStr = wrapper.MarshalStr(fmt.Sprint(formattedValue))
				}
				if err := proxywasm.SetProperty([]string{key}, []byte(marshalledJsonStr)); err != nil {
					log.Warnf("failed to set %s in filter state, raw is %s, err is %v", key, marshalledJsonStr, err)
				}
			} else {
				ctx.SetUserAttribute(key, formattedValue)
			}
		}
		if key == tokenusage.CtxKeyModel || key == tokenusage.CtxKeyInputToken || key == tokenusage.CtxKeyOutputToken || key == tokenusage.CtxKeyTotalToken || key == SourceIP {
			ctx.SetContext(key, value)
		}
		if attribute.ApplyToSpan {
			if attribute.TraceSpanKey != "" {
				key = attribute.TraceSpanKey
			}
			setSpanAttribute(key, value)
		}
	}
}

func isBuiltinAttribute(key string) bool {
	return key == BuiltinQuestionKey || key == BuiltinAnswerKey || key == BuiltinToolCallsKey || key == BuiltinReasoningKey || key == BuiltinFunctionCallKey || key == BuiltinSystemKey ||
		key == BuiltinReasoningTokens || key == BuiltinCachedTokens ||
		key == BuiltinInputTokenDetails || key == BuiltinOutputTokenDetails
}

func needsBodyBuffering(key string) bool {
	return key == BuiltinAnswerKey || key == BuiltinToolCallsKey || key == BuiltinReasoningKey || key == BuiltinFunctionCallKey
}

func getBuiltinAttributeDefaultSources(key string) []string {
	switch key {
	case BuiltinQuestionKey, BuiltinSystemKey:
		return []string{RequestBody}
	case BuiltinAnswerKey, BuiltinToolCallsKey, BuiltinReasoningKey, BuiltinFunctionCallKey:
		return []string{ResponseStreamingBody, ResponseBody}
	case BuiltinReasoningTokens, BuiltinCachedTokens, BuiltinInputTokenDetails, BuiltinOutputTokenDetails:
		return []string{ResponseStreamingBody, ResponseBody}
	default:
		return nil
	}
}

func shouldProcessBuiltinAttribute(key, configuredSource, currentSource string) bool {
	if configuredSource != "" {
		return configuredSource == currentSource
	}
	defaultSources := getBuiltinAttributeDefaultSources(key)
	for _, src := range defaultSources {
		if src == currentSource {
			return true
		}
	}
	return false
}

// extractTextFromMultimodalContent 从多模态 content 数组中提取所有 text 类型内容。
// 如果最后一个 message 的 content 是数组（OpenAI 多模态格式），则只保留 text 元素，
// 避免 image/audio 的 base64 数据占用过多空间。
func extractTextFromMultimodalContent(body []byte) string {
	content := gjson.GetBytes(body, "messages.@reverse.0.content")
	if !content.Exists() || !content.IsArray() {
		return ""
	}
	var texts []string
	for _, item := range content.Array() {
		if item.Get("type").String() == "text" {
			if text := item.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n")
	}
	return ""
}

// cleanMultimediaResiduals 删除字符串中的多模态占位符及其周边 JSON/Go-map 结构残留，
// 最大化保留纯文本内容。用于 question 超长时的优先保 text 策略。
func cleanMultimediaResiduals(str string) string {
	// 1. 删除标准多模态占位符
	for _, ph := range []string{"[image]", "[video]", "[audio]", "[file]", "[base64 data]"} {
		str = strings.ReplaceAll(str, ph, "")
	}

	// 2. 删除 Go fmt 输出的 map 结构残留（包含 image_url 的 map 块）
	str = regexp.MustCompile(`map\[[^\]]*image_url[^\]]*\]`).ReplaceAllString(str, "")

	// 3. 清理空的 map/object/array
	str = regexp.MustCompile(`map\[\]`).ReplaceAllString(str, "")
	str = regexp.MustCompile(`\{\s*\}`).ReplaceAllString(str, "")
	str = regexp.MustCompile(`\[\s*\]`).ReplaceAllString(str, "")

	// 4. 清理多余分隔符
	str = regexp.MustCompile(`,\s*,+`).ReplaceAllString(str, ",")
	str = regexp.MustCompile(`\[\s*,`).ReplaceAllString(str, "[")
	str = regexp.MustCompile(`,\s*\]`).ReplaceAllString(str, "]")
	str = regexp.MustCompile(`\{\s*,`).ReplaceAllString(str, "{")
	str = regexp.MustCompile(`,\s*\}`).ReplaceAllString(str, "}")
	str = strings.TrimSpace(str)

	return str
}

// extractEmbeddingAnswer 从 Embedding 模型响应中提取摘要信息。
// 返回格式: "N embedding(s) of dimension D, tokens: prompt=X total=Y"
func extractEmbeddingAnswer(body []byte) interface{} {
	data := gjson.GetBytes(body, AnswerPathEmbeddingData)
	if !data.Exists() || !data.IsArray() {
		return nil
	}
	arr := data.Array()
	if len(arr) == 0 {
		return nil
	}

	// 获取 embedding 维度
	dim := 0
	if emb := arr[0].Get("embedding"); emb.Exists() && emb.IsArray() {
		dim = len(emb.Array())
	}

	// 获取 usage
	promptTokens := gjson.GetBytes(body, "usage.prompt_tokens").Int()
	totalTokens := gjson.GetBytes(body, "usage.total_tokens").Int()

	return fmt.Sprintf("%d embedding(s) of dimension %d, tokens: prompt=%d total=%d",
		len(arr), dim, promptTokens, totalTokens)
}

// extractRerankQuestion 从 Rerank 请求中提取 query 和 documents 组合成 question。
// 格式: "query: <query>\ndocuments: <N> items\n[0] <doc0>\n[1] <doc1> ..."
func extractRerankQuestion(body []byte) interface{} {
	query := gjson.GetBytes(body, QuestionPathRerank)
	if !query.Exists() || query.String() == "" {
		return nil
	}

	var buf bytes.Buffer
	buf.WriteString("query: ")
	buf.WriteString(query.String())

	docs := gjson.GetBytes(body, RerankDocumentsPath)
	if docs.Exists() && docs.IsArray() {
		arr := docs.Array()
		buf.WriteString(fmt.Sprintf("\ndocuments: %d items", len(arr)))
		for i, doc := range arr {
			buf.WriteString(fmt.Sprintf("\n[%d] ", i))
			buf.WriteString(doc.String())
		}
	}

	return buf.String()
}

// StreamingMessageBuffer 用于流式响应中聚合 content 和 tool_calls
// 因为 extractStreamingBodyByJsonPath 只按单个 jsonPath 提取，
// 工具调用需要同时聚合 content 和 tool_calls 两个字段。
type StreamingMessageBuffer struct {
	Content   string
	ToolCalls []ToolCall
}

// extractOpenAIMessage 从非流式响应的 choices.0.message 中提取完整内容。
// 始终返回固定格式 JSON 字符串，包含 content / function_call / tool_calls / reasoning。
func extractOpenAIMessage(body []byte) interface{} {
	message := gjson.GetBytes(body, AnswerPathOpenAIMessage)
	if !message.Exists() {
		return nil
	}

	content := message.Get("content").String()
	toolCalls := message.Get("tool_calls")
	reasoning := message.Get("reasoning").String()
	funcCall := message.Get("function_call")

	result := map[string]interface{}{
		"content":       content,
		"reasoning":     reasoning,
		"tool_calls":    []interface{}{},
		"function_call": "",
	}

	if toolCalls.Exists() && toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
		result["tool_calls"] = toolCalls.Value()
	}

	if funcCall.Exists() && funcCall.Raw != "" && funcCall.Raw != "null" {
		if fcVal := funcCall.Value(); fcVal != nil {
			result["function_call"] = fcVal
		}
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		log.Warnf("[extractOpenAIMessage] marshal failed: %v", err)
		return nil
	}
	return string(jsonBytes)
}

// extractStreamingMessage 从流式响应的聚合 buffer 中提取 content + tool_calls + reasoning + function_call。
// 始终返回固定格式 JSON 字符串，空值给默认值。
func extractStreamingMessage(ctx wrapper.HttpContext, data []byte, rule string) interface{} {
	// 1. 提取 content
	content := extractStreamingBodyByJsonPath(data, AnswerPathOpenAIStreaming, rule)
	contentStr := ""
	if content != nil {
		contentStr = fmt.Sprint(content)
	}

	// 2. 提取 reasoning
	reasoningPath1 := "choices.0.delta.reasoning_content"
	reasoningPath2 := "choices.0.delta.reasoning"
	reasoning := extractStreamingBodyByJsonPath(data, reasoningPath1, RuleAppend)
	if reasoning == nil || fmt.Sprint(reasoning) == "" {
		reasoning = extractStreamingBodyByJsonPath(data, reasoningPath2, RuleAppend)
	}
	reasoningStr := ""
	if reasoning != nil {
		reasoningStr = fmt.Sprint(reasoning)
	}

	// 3. 提取 function_call
	funcNamePath := "choices.0.delta.function_call.name"
	funcArgsPath := "choices.0.delta.function_call.arguments"
	funcName := extractStreamingBodyByJsonPath(data, funcNamePath, RuleAppend)
	funcArgs := extractStreamingBodyByJsonPath(data, funcArgsPath, RuleAppend)

	funcCallObj := map[string]string{}
	if funcName != nil && fmt.Sprint(funcName) != "" {
		funcCallObj["name"] = fmt.Sprint(funcName)
	}
	if funcArgs != nil && fmt.Sprint(funcArgs) != "" {
		funcCallObj["arguments"] = fmt.Sprint(funcArgs)
	}

	// 4. 获取流式 tool_calls buffer（累积所有 chunk）
	var toolCalls []ToolCall
	if buffer, ok := ctx.GetContext(CtxStreamingToolCallsBuffer).(*StreamingToolCallsBuffer); ok {
		toolCalls = getToolCallsFromBuffer(buffer)
	}

	result := map[string]interface{}{
		"content":    contentStr,
		"reasoning":  reasoningStr,
		"tool_calls": toolCalls,
		"function_call": "",
	}
	if len(funcCallObj) > 0 {
		result["function_call"] = funcCallObj
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		log.Warnf("[extractStreamingMessage] marshal failed: %v", err)
		return contentStr
	}
	return string(jsonBytes)
}

// extractStreamingReasoning 从流式 chunk 中提取 reasoning。
// 尝试多个路径：delta.reasoning / delta.reasoning_content / message.reasoning
// 某些模型（如 DeepSeek）在流式响应中把 reasoning 放在 message 中而非 delta 中。
func extractStreamingReasoning(data []byte) interface{} {
	paths := []string{
		"choices.0.delta.reasoning",
		"choices.0.delta.reasoning_content",
		"choices.0.message.reasoning",
		"choices.0.message.reasoning_content",
	}
	for _, path := range paths {
		if value := extractStreamingBodyByJsonPath(data, path, RuleAppend); value != nil && fmt.Sprint(value) != "" {
			return value
		}
	}
	return nil
}

// extractStreamingToolCallsSimple 从流式 chunk 中提取 tool_calls。
// 尝试 delta.tool_calls 和 message.tool_calls 两个路径。
func extractStreamingToolCallsSimple(data []byte) interface{} {
	paths := []string{
		"choices.0.delta.tool_calls",
		"choices.0.message.tool_calls",
	}
	for _, path := range paths {
		if value := extractStreamingBodyByJsonPath(data, path, RuleAppend); value != nil && fmt.Sprint(value) != "" {
			return value
		}
	}
	return nil
}

// extractStreamingFunctionCall 从流式 chunk 中提取 function_call。
// 尝试 delta.function_call 和 message.function_call 两个路径。
func extractStreamingFunctionCall(data []byte) interface{} {
	paths := []string{
		"choices.0.delta.function_call",
		"choices.0.message.function_call",
	}
	for _, path := range paths {
		if value := extractStreamingBodyByJsonPath(data, path, RuleAppend); value != nil && fmt.Sprint(value) != "" {
			return value
		}
	}
	return nil
}

func getBuiltinAttributeFallback(ctx wrapper.HttpContext, config AIStatisticsConfig, key, source string, body []byte, rule string) interface{} {
	log.Infof("[AI-STAT-DEBUG] getBuiltinFallback START: key=%s source=%s bodyLen=%d", key, source, len(body))
	switch key {
	case BuiltinQuestionKey:
		if source == RequestBody {
			// 优先尝试通用 chat/completions 路径
			// 如果是多模态数组，提取所有 text 类型内容，避免 image base64 占用空间
			if texts := extractTextFromMultimodalContent(body); texts != "" {
				log.Infof("[AI-STAT-DEBUG] question extracted from multimodal text, len=%d", len(texts))
				return texts
			}
			if value := gjson.GetBytes(body, QuestionPathOpenAI).Value(); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] question extracted from QuestionPathOpenAI, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			// Embedding 模型: input 字段（string 或 string array）
			if value := gjson.GetBytes(body, QuestionPathEmbedding).Value(); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] question extracted from QuestionPathEmbedding, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			// Rerank 模型: 组合 query + documents
			if value := extractRerankQuestion(body); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] question extracted from extractRerankQuestion, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			log.Infof("[AI-STAT-DEBUG] question: all extraction paths failed")
		}
	case BuiltinSystemKey:
		if source == RequestBody {
			if value := gjson.GetBytes(body, SystemPathClaude).Value(); value != nil && value != "" {
				return value
			}
		}
	case BuiltinAnswerKey:
		if source == ResponseStreamingBody {
			// 优先提取 content + tool_calls 合并（工具调用场景）
			if value := extractStreamingMessage(ctx, body, rule); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] answer extracted from streaming message, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			// 兜底：只提取 content
			if value := extractStreamingBodyByJsonPath(body, AnswerPathOpenAIStreaming, rule); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] answer extracted from OpenAI streaming content, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			if value := extractStreamingBodyByJsonPath(body, AnswerPathClaudeStreaming, rule); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] answer extracted from Claude streaming, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
		} else if source == ResponseBody {
			// 优先从完整 message 中提取（工具调用场景：content + tool_calls + reasoning）
			if value := extractOpenAIMessage(body); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] answer extracted from OpenAI message, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			// 兜底：只提取 content
			if value := gjson.GetBytes(body, AnswerPathOpenAINonStreaming).Value(); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] answer extracted from OpenAI content, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			if value := gjson.GetBytes(body, AnswerPathClaudeNonStreaming).Value(); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] answer extracted from Claude non-streaming, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			// Embedding 模型: 提取 data 摘要和 usage
			if value := extractEmbeddingAnswer(body); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] answer extracted from Embedding, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			// Rerank 模型: results 字段
			if value := gjson.GetBytes(body, AnswerPathRerank).Value(); value != nil && value != "" {
				log.Infof("[AI-STAT-DEBUG] answer extracted from Rerank, type=%T len=%d", value, len(fmt.Sprint(value)))
				return value
			}
			log.Infof("[AI-STAT-DEBUG] answer: all extraction paths failed for source=%s", source)
		}
	case BuiltinToolCallsKey:
		if source == ResponseStreamingBody {
			var buffer *StreamingToolCallsBuffer
			if existingBuffer, ok := ctx.GetContext(CtxStreamingToolCallsBuffer).(*StreamingToolCallsBuffer); ok {
				buffer = existingBuffer
			}
			buffer = extractStreamingToolCalls(body, buffer)
			buffer = extractClaudeStreamingToolCalls(body, buffer)
			ctx.SetContext(CtxStreamingToolCallsBuffer, buffer)

			toolCalls := getToolCallsFromBuffer(buffer)
			if len(toolCalls) > 0 {
				ctx.SetUserAttribute(BuiltinToolCallsKey, toolCalls)
				return toolCalls
			}
		} else if source == ResponseBody {
			if value := gjson.GetBytes(body, ToolCallsPathNonStreaming).Value(); value != nil {
				return value
			}
		}
	case BuiltinReasoningKey:
		if source == ResponseStreamingBody {
			// 尝试 delta.reasoning（标准 OpenAI 流式）
			if value := extractStreamingBodyByJsonPath(body, ReasoningPathStreaming, RuleAppend); value != nil && value != "" {
				return value
			}
			// 尝试 delta.reasoning_content（DeepSeek 格式）
			if value := extractStreamingBodyByJsonPath(body, ReasoningPathStreamingAlt, RuleAppend); value != nil && value != "" {
				return value
			}
			// 尝试 message.reasoning（某些模型在流式 chunk 中也放在 message 中）
			if value := extractStreamingBodyByJsonPath(body, "choices.0.message.reasoning", RuleAppend); value != nil && value != "" {
				return value
			}
			if value := extractStreamingBodyByJsonPath(body, "choices.0.message.reasoning_content", RuleAppend); value != nil && value != "" {
				return value
			}
		} else if source == ResponseBody {
			// 纯 JSON 格式
			if value := gjson.GetBytes(body, ReasoningPathNonStreaming).Value(); value != nil && value != "" {
				return value
			}
			if value := gjson.GetBytes(body, ReasoningPathNonStreamingAlt).Value(); value != nil && value != "" {
				return value
			}
			// SSE 格式 fallback（body 可能是 SSE 聚合结果）
			if value := extractStreamingBodyByJsonPath(body, ReasoningPathNonStreaming, RuleAppend); value != nil && value != "" {
				return value
			}
			if value := extractStreamingBodyByJsonPath(body, ReasoningPathNonStreamingAlt, RuleAppend); value != nil && value != "" {
				return value
			}
		}
	case BuiltinFunctionCallKey:
		if source == ResponseStreamingBody {
			// 尝试 delta.function_call（标准 OpenAI 流式）
			funcName := extractStreamingBodyByJsonPath(body, FunctionCallPathStreamingName, RuleAppend)
			funcArgs := extractStreamingBodyByJsonPath(body, FunctionCallPathStreamingArgs, RuleAppend)
			if funcName != nil || funcArgs != nil {
				fc := map[string]string{}
				if funcName != nil && fmt.Sprint(funcName) != "" {
					fc["name"] = fmt.Sprint(funcName)
				}
				if funcArgs != nil && fmt.Sprint(funcArgs) != "" {
					fc["arguments"] = fmt.Sprint(funcArgs)
				}
				if jsonBytes, err := json.Marshal(fc); err == nil {
					return string(jsonBytes)
				}
			}
			// 尝试 message.function_call（某些模型放在 message 中）
			funcName = extractStreamingBodyByJsonPath(body, "choices.0.message.function_call.name", RuleAppend)
			funcArgs = extractStreamingBodyByJsonPath(body, "choices.0.message.function_call.arguments", RuleAppend)
			if funcName != nil || funcArgs != nil {
				fc := map[string]string{}
				if funcName != nil && fmt.Sprint(funcName) != "" {
					fc["name"] = fmt.Sprint(funcName)
				}
				if funcArgs != nil && fmt.Sprint(funcArgs) != "" {
					fc["arguments"] = fmt.Sprint(funcArgs)
				}
				if jsonBytes, err := json.Marshal(fc); err == nil {
					return string(jsonBytes)
				}
			}
		} else if source == ResponseBody {
			// 纯 JSON 格式
			if value := gjson.GetBytes(body, FunctionCallPathNonStreaming).Value(); value != nil {
				return value
			}
			// SSE 格式 fallback
			if value := extractStreamingBodyByJsonPath(body, "choices.0.message.function_call", RuleAppend); value != nil && fmt.Sprint(value) != "" {
				return value
			}
		}
	case BuiltinReasoningTokens:
		if source == ResponseBody || source == ResponseStreamingBody {
			if outputTokenDetails, ok := ctx.GetContext(tokenusage.CtxKeyOutputTokenDetails).(map[string]int64); ok {
				if reasoningTokens, exists := outputTokenDetails["reasoning_tokens"]; exists {
					return reasoningTokens
				}
			}
		}
	case BuiltinCachedTokens:
		if source == ResponseBody || source == ResponseStreamingBody {
			if inputTokenDetails, ok := ctx.GetContext(tokenusage.CtxKeyInputTokenDetails).(map[string]int64); ok {
				if cachedTokens, exists := inputTokenDetails["cached_tokens"]; exists {
					return cachedTokens
				}
			}
		}
	case BuiltinInputTokenDetails:
		if source == ResponseBody || source == ResponseStreamingBody {
			if inputTokenDetails, ok := ctx.GetContext(tokenusage.CtxKeyInputTokenDetails).(map[string]int64); ok {
				return inputTokenDetails
			}
		}
	case BuiltinOutputTokenDetails:
		if source == ResponseBody || source == ResponseStreamingBody {
			if outputTokenDetails, ok := ctx.GetContext(tokenusage.CtxKeyOutputTokenDetails).(map[string]int64); ok {
				return outputTokenDetails
			}
		}
	}
	return nil
}

func extractStreamingBodyByJsonPath(data []byte, jsonPath string, rule string) interface{} {
	chunks := bytes.Split(bytes.TrimSpace(wrapper.UnifySSEChunk(data)), []byte("\n\n"))
	var value interface{}
	if rule == RuleFirst {
		for _, chunk := range chunks {
			jsonObj := gjson.GetBytes(chunk, jsonPath)
			if jsonObj.Exists() {
				value = jsonObj.Value()
				break
			}
		}
	} else if rule == RuleReplace {
		for _, chunk := range chunks {
			jsonObj := gjson.GetBytes(chunk, jsonPath)
			if jsonObj.Exists() {
				value = jsonObj.Value()
			}
		}
	} else if rule == RuleAppend {
		var strValue string
		for _, chunk := range chunks {
			jsonObj := gjson.GetBytes(chunk, jsonPath)
			if jsonObj.Exists() {
				strValue += jsonObj.String()
			}
		}
		value = strValue
	} else {
		log.Errorf("unsupported rule type: %s", rule)
	}
	return value
}

func shouldLogDebug() bool {
	value, err := proxywasm.CallForeignFunction("get_log_level", nil)
	if err != nil {
		return false
	}
	if len(value) < 4 {
		return false
	}
	envoyLogLevel := binary.LittleEndian.Uint32(value[:4])
	return envoyLogLevel == LogLevelTrace || envoyLogLevel == LogLevelDebug
}

func debugLogAiLog(ctx wrapper.HttpContext) {
	if !shouldLogDebug() {
		return
	}

	userAttrs := make(map[string]interface{})

	if question := ctx.GetUserAttribute("question"); question != nil {
		userAttrs["question"] = question
	}
	if system := ctx.GetUserAttribute("system"); system != nil {
		userAttrs["system"] = system
	}
	if answer := ctx.GetUserAttribute("answer"); answer != nil {
		userAttrs["answer"] = answer
	}
	if reasoning := ctx.GetUserAttribute("reasoning"); reasoning != nil {
		userAttrs["reasoning"] = reasoning
	}
	if toolCalls := ctx.GetUserAttribute("tool_calls"); toolCalls != nil {
		userAttrs["tool_calls"] = toolCalls
	}
	if funcCall := ctx.GetUserAttribute("function_call"); funcCall != nil {
		userAttrs["function_call"] = funcCall
	}
	if messages := ctx.GetUserAttribute("messages"); messages != nil {
		userAttrs["messages"] = messages
	}
	if sessionId := ctx.GetUserAttribute("session_id"); sessionId != nil {
		userAttrs["session_id"] = sessionId
	}
	if model := ctx.GetUserAttribute("model"); model != nil {
		userAttrs["model"] = model
	}
	if consumer := ctx.GetUserAttribute("consumer"); consumer != nil {
		userAttrs["consumer"] = consumer
	}
	if inputToken := ctx.GetUserAttribute("input_token"); inputToken != nil {
		userAttrs["input_token"] = inputToken
	}
	if outputToken := ctx.GetUserAttribute("output_token"); outputToken != nil {
		userAttrs["output_token"] = outputToken
	}
	if totalToken := ctx.GetUserAttribute("total_token"); totalToken != nil {
		userAttrs["total_token"] = totalToken
	}
	if chatId := ctx.GetUserAttribute("chat_id"); chatId != nil {
		userAttrs["chat_id"] = chatId
	}
	if responseType := ctx.GetUserAttribute("response_type"); responseType != nil {
		userAttrs["response_type"] = responseType
	}
	if llmFirstTokenDuration := ctx.GetUserAttribute("llm_first_token_duration"); llmFirstTokenDuration != nil {
		userAttrs["llm_first_token_duration"] = llmFirstTokenDuration
	}
	if llmServiceDuration := ctx.GetUserAttribute("llm_service_duration"); llmServiceDuration != nil {
		userAttrs["llm_service_duration"] = llmServiceDuration
	}
	if reasoningTokens := ctx.GetUserAttribute("reasoning_tokens"); reasoningTokens != nil {
		userAttrs["reasoning_tokens"] = reasoningTokens
	}
	if cachedTokens := ctx.GetUserAttribute("cached_tokens"); cachedTokens != nil {
		userAttrs["cached_tokens"] = cachedTokens
	}
	if inputTokenDetails := ctx.GetUserAttribute("input_token_details"); inputTokenDetails != nil {
		userAttrs["input_token_details"] = inputTokenDetails
	}
	if outputTokenDetails := ctx.GetUserAttribute("output_token_details"); outputTokenDetails != nil {
		userAttrs["output_token_details"] = outputTokenDetails
	}

	logJson, _ := json.Marshal(userAttrs)
	log.Debugf("[ai_log] attributes to be written: %s", string(logJson))
}

func setSpanAttribute(key string, value interface{}) {
	if value != "" {
		traceSpanTag := wrapper.TraceSpanTagPrefix + key
		if e := proxywasm.SetProperty([]string{traceSpanTag}, []byte(fmt.Sprint(value))); e != nil {
			log.Warnf("failed to set %s in filter state: %v", traceSpanTag, e)
		}
	} else {
		log.Debugf("failed to write span attribute [%s], because it's value is empty", key)
	}
}

func writeMetric(ctx wrapper.HttpContext, config AIStatisticsConfig) {
	var ok bool
	var route, cluster, model string
	// Read consumer from context (backed up in request phase).
	// Fallback to property if context value is missing.
	consumer := ctx.GetStringContext(CtxConsumerValue, "")
	if consumer == "" {
		if raw, err := proxywasm.GetProperty([]string{"ai_statistics_consumer"}); err == nil && len(raw) > 0 {
			consumer = string(raw)
		}
	}
	if consumer == "" {
		consumer = "none"
	}
	route, ok = ctx.GetContext(RouteName).(string)
	if !ok {
		log.Info("RouteName type assert failed, skip metric record")
		return
	}
	cluster, ok = ctx.GetContext(ClusterName).(string)
	if !ok {
		log.Info("ClusterName type assert failed, skip metric record")
		return
	}

	if config.disableOpenaiUsage {
		return
	}

	if ctx.GetUserAttribute(tokenusage.CtxKeyModel) == nil || ctx.GetUserAttribute(tokenusage.CtxKeyInputToken) == nil || ctx.GetUserAttribute(tokenusage.CtxKeyOutputToken) == nil || ctx.GetUserAttribute(tokenusage.CtxKeyTotalToken) == nil {
		log.Info("get usage information failed, skip metric record")
		return
	}
	model, ok = ctx.GetUserAttribute(tokenusage.CtxKeyModel).(string)
	if !ok {
		log.Info("Model type assert failed, skip metric record")
		return
	}
	sourceIP := "unknown"
	sourceIPByAttribute, ok := ctx.GetUserAttribute(SourceIP).(string)
	if !ok {
		log.Warnf("attribute 'sourceIP' does not exist or is not a string")
	} else {
		sourceIP = sourceIPByAttribute
		log.Debugf("sourceIP is %v", sourceIP)
	}

	if inputToken, ok := convertToUInt(ctx.GetUserAttribute(tokenusage.CtxKeyInputToken)); ok {
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, tokenusage.CtxKeyInputToken), inputToken)
	} else {
		log.Info("InputToken type assert failed, skip metric record")
	}
	if outputToken, ok := convertToUInt(ctx.GetUserAttribute(tokenusage.CtxKeyOutputToken)); ok {
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, tokenusage.CtxKeyOutputToken), outputToken)
	} else {
		log.Info("OutputToken type assert failed, skip metric record")
	}
	if totalToken, ok := convertToUInt(ctx.GetUserAttribute(tokenusage.CtxKeyTotalToken)); ok {
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, tokenusage.CtxKeyTotalToken), totalToken)
	} else {
		log.Info("TotalToken type assert failed, skip metric record")
	}

	var llmFirstTokenDuration, llmServiceDuration uint64
	if ctx.GetUserAttribute(LLMFirstTokenDuration) != nil {
		llmFirstTokenDuration, ok = convertToUInt(ctx.GetUserAttribute(LLMFirstTokenDuration))
		if !ok {
			log.Info("LLMFirstTokenDuration type assert failed")
			return
		}
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, LLMFirstTokenDuration), llmFirstTokenDuration)
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, LLMStreamDurationCount), 1)
	}
	if ctx.GetUserAttribute(LLMServiceDuration) != nil {
		llmServiceDuration, ok = convertToUInt(ctx.GetUserAttribute(LLMServiceDuration))
		if !ok {
			log.Warnf("LLMServiceDuration type assert failed")
			return
		}
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, LLMServiceDuration), llmServiceDuration)
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, LLMDurationCount), 1)
	}
}

func classifyFailure(statusCode, codeDetails, transportFailure string, isFallbackRoute bool) string {
	code, _ := strconv.Atoi(statusCode)

	if code >= 200 && code < 400 {
		return ""
	}

	lowerDetails := strings.ToLower(codeDetails)
	lowerTransport := strings.ToLower(transportFailure)

	if transportFailure != "" {
		switch {
		case strings.Contains(lowerTransport, "connection refused"):
			return "upstream_connection_refused"
		case strings.Contains(lowerTransport, "no healthy host"):
			return "upstream_no_healthy_host"
		case strings.Contains(lowerTransport, "timeout") || strings.Contains(lowerTransport, "timed out"):
			if strings.Contains(lowerTransport, "connect") {
				return "upstream_connect_timeout"
			}
			return "upstream_response_timeout"
		case strings.Contains(lowerTransport, "tls") || strings.Contains(lowerTransport, "certificate") || strings.Contains(lowerTransport, "ssl"):
			return "upstream_tls_failure"
		default:
			return "upstream_transport_failure: " + transportFailure
		}
	}

	switch {
	case code == 401:
		if strings.Contains(lowerDetails, "jwt") {
			return "auth_jwt_invalid"
		}
		if strings.Contains(lowerDetails, "ext_authz") || strings.Contains(lowerDetails, "auth") {
			return "auth_api_key_invalid"
		}
		return "auth_unauthorized"
	case code == 403:
		if strings.Contains(lowerDetails, "ext_authz") || strings.Contains(lowerDetails, "auth") {
			return "auth_forbidden"
		}
		if strings.Contains(lowerDetails, "ratelimit") || strings.Contains(lowerDetails, "rate_limit") {
			return "rate_limited"
		}
		if strings.Contains(lowerDetails, "rbac") {
			return "rbac_denied"
		}
		return "forbidden"
	case code == 429:
		return "rate_limited"
	}

	if code == 404 {
		// In fallback route scenario, 404 means the request didn't match any real route
		// (the catch-all route forwarded to a dummy upstream which returns 404)
		if isFallbackRoute {
			return "route_not_found"
		}
		if strings.Contains(lowerDetails, "no_route") || strings.Contains(lowerDetails, "no cluster") {
			return "route_not_found"
		}
		return "route_not_found"
	}

	if code == 408 {
		return "request_timeout"
	}

	if code >= 500 && code < 600 {
		if strings.Contains(lowerDetails, "via_upstream") {
			return "upstream_service_error"
		}
		if strings.Contains(lowerDetails, "connect_timeout") || strings.Contains(lowerDetails, "connect timeout") {
			return "upstream_connect_timeout"
		}
		if strings.Contains(lowerDetails, "connection_failure") || strings.Contains(lowerDetails, "connection failure") {
			return "upstream_connection_failure"
		}
		if strings.Contains(lowerDetails, "upstream_reset") || strings.Contains(lowerDetails, "upstream reset") {
			return "upstream_reset"
		}
		if strings.Contains(lowerDetails, "remote_disconnect") || strings.Contains(lowerDetails, "remote disconnect") {
			return "upstream_remote_disconnect"
		}
		if strings.Contains(lowerDetails, "no_route") || strings.Contains(lowerDetails, "no cluster") {
			return "gateway_no_route"
		}
		if strings.Contains(lowerDetails, "overload") {
			return "gateway_overload"
		}
		if strings.Contains(lowerDetails, "local_reply") || strings.Contains(lowerDetails, "local reply") {
			return "gateway_local_error"
		}
		return fmt.Sprintf("server_error_%d", code)
	}

	if code >= 400 && code < 500 {
		return fmt.Sprintf("client_error_%d", code)
	}

	if code == 0 {
		return "unknown"
	}
	return fmt.Sprintf("error_status_%d", code)
}

func getSourceIP(ctx wrapper.HttpContext) string {
	if v := ctx.GetUserAttribute(SourceIP); v != nil {
		if s, ok := v.(string); ok && s != "" && s != "unknown" {
			return s
		}
	}
	if bs, err := proxywasm.GetProperty([]string{"source", "address"}); err == nil && len(bs) > 0 {
		if ip := parseIP(string(bs)); isValidIP(ip) {
			return ip
		}
	}
	if xff, err := proxywasm.GetHttpRequestHeader("X-Forwarded-For"); err == nil && xff != "" {
		ips := strings.Split(xff, ",")
		for _, ip := range ips {
			if clean := strings.TrimSpace(ip); isValidIP(clean) {
				return clean
			}
		}
	}
	return "unknown"
}

func writeRawAILogToFilterState(aiLog map[string]interface{}) {
	if aiLog == nil {
		aiLog = make(map[string]interface{})
	}
	rawJSON, err := json.Marshal(aiLog)
	if err != nil {
		log.Warnf("failed to marshal ai_log for filter state: %v", err)
		return
	}
	if err := proxywasm.SetProperty([]string{"wasm", "ai_log"}, rawJSON); err != nil {
		log.Warnf("failed to set wasm.ai_log filter state: %v", err)
	}
}

// appendToAILogFilterState 增量更新 wasm.ai_log filter state。
// 先读取已有的 ai_log，合并新字段后再写回。用于在 request 阶段逐步构建 ai_log，
// 确保 500/error 场景下 ai_log 至少有 request 阶段能获取的数据。
func appendToAILogFilterState(updates map[string]interface{}) {
	merged := make(map[string]interface{})
	// 先读取已有的 ai_log（如果存在）
	if raw, err := proxywasm.GetProperty([]string{"wasm", "ai_log"}); err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, &merged); err != nil {
			log.Debugf("appendToAILogFilterState: failed to unmarshal existing ai_log: %v", err)
		}
	}
	// 合并新字段
	for k, v := range updates {
		merged[k] = v
	}
	writeRawAILogToFilterState(merged)
}

// writeStringToFilterState 将字段包装为单键 JSON 对象写入 Envoy filter state。
// 例如 key="model", value="DeepSeek" 会存储为 {"model":"DeepSeek"}。
// 配合 accessLogFormat 中 "model": %FILTER_STATE(wasm.model:PLAIN)% 使用（不加引号），
// Envoy 会直接将 JSON 对象嵌入外层日志，避免字符串转义问题。
func writeStringToFilterState(key, value string) {
	if value == "" {
		value = "-"
	}
	// 包装为单键 JSON 对象，如 {"model":"DeepSeek-R1"}
	obj := map[string]string{key: value}
	rawJSON, err := json.Marshal(obj)
	if err != nil {
		log.Warnf("failed to marshal JSON object for key %s: %v", key, err)
		return
	}
	if err := proxywasm.SetProperty([]string{"wasm", key}, rawJSON); err != nil {
		log.Warnf("failed to set filter state wasm.%s: %v", key, err)
	} else {
		log.Infof("successfully set filter state wasm.%s = %s", key, string(rawJSON))
	}
}

// writeTopLevelFields 将 record 中的核心字段以及 ai_log 中的关键业务字段
// 作为独立的 filter state 键写入，供 accessLogFormat 直接平级引用。
// 本函数仅做"额外冗余输出"，不影响原有的 ai_log 与 stdout 日志链路。
func writeTopLevelFields(record *AILogRecord) {
	if record == nil {
		return
	}

	// === 从 record 提取的元数据（请求级）===
	writeStringToFilterState("model", record.Model)
	writeStringToFilterState("consumer", record.Consumer)
	writeStringToFilterState("source_ip", record.SourceIP)
	writeStringToFilterState("route_name", record.Route)
	writeStringToFilterState("request_path", record.RequestPath)
	writeStringToFilterState("request_method", record.RequestMethod)
	writeStringToFilterState("status_code", strconv.Itoa(record.StatusCode))
	if record.RequestSuccess {
		writeStringToFilterState("request_success", "true")
	} else {
		writeStringToFilterState("request_success", "false")
	}
	writeStringToFilterState("failure_reason", record.FailureReason)
	writeStringToFilterState("backend_upstream_address", record.BackendUpstreamAddress)
	writeStringToFilterState("response_type", record.ResponseType)
	writeStringToFilterState("session_id", record.SessionID)

	// === 从 ai_log 提取的 AI 业务字段 ===
	if record.AILog != nil {
		if v, ok := record.AILog["question"]; ok {
			writeStringToFilterState("question", fmt.Sprint(v))
		}
		if v, ok := record.AILog["answer"]; ok {
			writeStringToFilterState("answer", fmt.Sprint(v))
		}
		if v, ok := record.AILog["system"]; ok {
			writeStringToFilterState("system", fmt.Sprint(v))
		}
		if v, ok := record.AILog["reasoning"]; ok {
			writeStringToFilterState("reasoning", fmt.Sprint(v))
		}
		if v, ok := record.AILog["tool_calls"]; ok {
			writeStringToFilterState("tool_calls", fmt.Sprint(v))
		}
		if v, ok := record.AILog["chat_id"]; ok {
			writeStringToFilterState("chat_id", fmt.Sprint(v))
		}
		if v, ok := record.AILog["chat_round"]; ok {
			writeStringToFilterState("chat_round", fmt.Sprint(v))
		}
		if v, ok := record.AILog["input_token"]; ok {
			writeStringToFilterState("input_token", fmt.Sprint(v))
		}
		if v, ok := record.AILog["output_token"]; ok {
			writeStringToFilterState("output_token", fmt.Sprint(v))
		}
		if v, ok := record.AILog["total_token"]; ok {
			writeStringToFilterState("total_token", fmt.Sprint(v))
		}
		if v, ok := record.AILog["llm_service_duration"]; ok {
			writeStringToFilterState("llm_service_duration", fmt.Sprint(v))
		}
		if v, ok := record.AILog["llm_first_token_duration"]; ok {
			writeStringToFilterState("llm_first_token_duration", fmt.Sprint(v))
		}
	}
}

func outputAILog(ctx wrapper.HttpContext, config AIStatisticsConfig) {
	log.Debugf("[AI-LOG] building success log record...")
	record := buildAILogRecord(ctx, config)
	log.Debugf("[AI-LOG] record built: status=%d model=%s consumer=%s success=%v", record.StatusCode, record.Model, record.Consumer, record.RequestSuccess)

	// buildAILogRecord 内部已统一调用 writeRawAILogToFilterState 和 writeTopLevelFields，
	// 这里直接序列化输出即可，无需重复写入 filter state。
	recordBytes, err := json.Marshal(record)
	if err != nil {
		log.Errorf("[AI-LOG] failed to marshal AI log: %v", err)
		return
	}
	log.Infof("[AILOG] %s", string(recordBytes))
	log.Debugf("[AI-LOG] success log output complete, size=%d bytes", len(recordBytes))
}

func outputAILogFailure(ctx wrapper.HttpContext, config AIStatisticsConfig) {
	log.Debugf("[AI-LOG] building failure log record...")
	record := buildAILogRecord(ctx, config)
	record.RequestSuccess = false
	if record.FailureReason == "" {
		record.FailureReason = ctx.GetStringContext(CtxFailureReason, "")
	}
	if record.StatusCode == 0 {
		if sc := ctx.GetStringContext(ResponseStatusCode, "0"); sc != "" {
			record.StatusCode, _ = strconv.Atoi(sc)
		}
	}
	log.Debugf("[AI-STATISTICS-DEBUG] outputAILogFailure: model=%s consumer=%s status=%d", record.Model, record.Consumer, record.StatusCode)
	if recordBytes, err := json.Marshal(record); err == nil {
		log.Infof("[AILOG] %s", string(recordBytes))
		log.Debugf("[AI-LOG] failure log output complete: status=%d reason=%s size=%d bytes", record.StatusCode, record.FailureReason, len(recordBytes))
	} else {
		log.Errorf("[AI-LOG] failed to marshal failure log: %v", err)
	}
}

func buildAILogRecord(ctx wrapper.HttpContext, config AIStatisticsConfig) *AILogRecord {
	// Read consumer from Envoy property (stored in request phase via proxywasm.SetProperty).
	// getConsumerFromRequest() cannot be called here because proxywasm.GetHttpRequestHeader
	// is not available in response phase to read request headers.
	consumer := "none"
	if raw, err := proxywasm.GetProperty([]string{"ai_statistics_consumer"}); err == nil && len(raw) > 0 {
		consumer = string(raw)
		log.Infof("[AI-STATISTICS-DEBUG] buildAILogRecord: consumer from property: %s", consumer)
	} else {
		log.Infof("[AI-STATISTICS-DEBUG] buildAILogRecord: consumer property not found, err=%v", err)
	}

	record := &AILogRecord{
		Timestamp:           time.Now().Format(time.RFC3339Nano),
		PodName:             os.Getenv("POD_NAME"),
		Route:               ctx.GetStringContext(RouteName, "-"),
		Cluster:             ctx.GetStringContext(ClusterName, "-"),
		BackendModelCluster: ctx.GetStringContext(ClusterName, "-"),
		Consumer:            consumer,
	}
	if record.PodName == "" {
		record.PodName = "unknown"
	}

	statusCodeStr := ctx.GetStringContext(ResponseStatusCode, "0")
	record.StatusCode, _ = strconv.Atoi(statusCodeStr)
	record.RequestSuccess = record.StatusCode >= 200 && record.StatusCode < 400

	if model := ctx.GetUserAttribute("model"); model != nil {
		record.Model = fmt.Sprint(model)
		log.Debugf("[AI-STATISTICS-DEBUG] buildAILogRecord: model from user attribute: %s", record.Model)
	} else if requestModel := ctx.GetContext(tokenusage.CtxKeyRequestModel); requestModel != nil {
		record.Model = fmt.Sprint(requestModel)
		log.Debugf("[AI-STATISTICS-DEBUG] buildAILogRecord: model from context fallback: %s", record.Model)
	} else {
		// Final fallback: read from property (set in onHttpRequestHeaders from URL path)
		if raw, err := proxywasm.GetProperty([]string{"ai_statistics_model"}); err == nil && len(raw) > 0 {
			record.Model = string(raw)
			log.Infof("[AI-STATISTICS-DEBUG] buildAILogRecord: model from property fallback: %s", record.Model)
		} else {
			record.Model = "UNKNOWN"
			log.Debugf("[AI-STATISTICS-DEBUG] buildAILogRecord: model not found anywhere, using UNKNOWN")
		}
	}

	record.SourceIP = getSourceIP(ctx)

	if sessionId := ctx.GetUserAttribute(SessionID); sessionId != nil {
		record.SessionID = fmt.Sprint(sessionId)
	}

	if respType := ctx.GetUserAttribute(ResponseType); respType != nil {
		record.ResponseType = fmt.Sprint(respType)
	}
	if duration := ctx.GetUserAttribute(LLMServiceDuration); duration != nil {
		if d, ok := convertToUInt(duration); ok {
			record.LLMServiceDuration = int64(d)
		}
	}
	if ftd := ctx.GetUserAttribute(LLMFirstTokenDuration); ftd != nil {
		if d, ok := convertToUInt(ftd); ok {
			record.LLMFirstTokenDuration = int64(d)
		}
	}

	record.BackendUpstreamAddress = ctx.GetStringContext(CtxBackendUpstreamAddress, "")

	record.FailureReason = ctx.GetStringContext(CtxFailureReason, "")

	record.RequestMethod = ""
	if rm := ctx.GetUserAttribute("request_method"); rm != nil {
		record.RequestMethod = fmt.Sprint(rm)
	}
	record.RequestPath = ""
	if rp := ctx.GetUserAttribute("request_path"); rp != nil {
		record.RequestPath = fmt.Sprint(rp)
	}

	// Start with request-phase ai_log data pre-built in onHttpRequestHeaders/onHttpRequestBody.
	// This ensures 500/error scenarios still have consumer, model, question even if
	// response-phase GetProperty calls fail.
	aiLog := make(map[string]interface{})
	if raw, err := proxywasm.GetProperty([]string{"wasm", "ai_log"}); err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, &aiLog); err == nil {
			log.Infof("[AI-STATISTICS-DEBUG] buildAILogRecord: loaded request-phase ai_log with %d fields", len(aiLog))
		} else {
			log.Warnf("[AI-STATISTICS-DEBUG] buildAILogRecord: failed to unmarshal request-phase ai_log: %v", err)
		}
	} else {
		log.Infof("[AI-STATISTICS-DEBUG] buildAILogRecord: no request-phase ai_log found, err=%v", err)
	}

	collectBasicAILogInfo(ctx, aiLog, record.Consumer)

	collectAIAttr := func(key string) {
		if v := ctx.GetUserAttribute(key); v != nil {
			vLen := len(fmt.Sprint(v))
			log.Infof("[AI-STAT-DEBUG] collectAIAttr: key=%s type=%T len=%d", key, v, vLen)
			aiLog[key] = v
		} else {
			log.Infof("[AI-STAT-DEBUG] collectAIAttr: key=%s NOT FOUND", key)
		}
	}
	collectAIAttr("question")
	collectAIAttr("system")
	// answer / reasoning / tool_calls / function_call 将在下方统一合并为复合 answer JSON 对象
	collectAIAttr("messages")
	collectAIAttr("session_id")
	collectAIAttr("chat_id")
	collectAIAttr("chat_round")
	collectAIAttr("input_token")
	collectAIAttr("output_token")
	collectAIAttr("total_token")
	collectAIAttr("reasoning_tokens")
	collectAIAttr("cached_tokens")
	collectAIAttr("input_token_details")
	collectAIAttr("output_token_details")

	log.Infof("[AI-STAT-DEBUG] before summarize: maxAttributeBytes=%d", config.maxAttributeBytes)
	for key, val := range aiLog {
		origLen := len(fmt.Sprint(val))
		switch key {
		case "messages":
			aiLog[key] = summarizeMessages(val, config.maxAttributeBytes)
		case "question", "reasoning", "tool_calls", "function_call":
			aiLog[key] = summarizeAttribute(key, val, config.maxAttributeBytes)
		default:
			// 对其他可能包含多模态内容的字段，超长后才替换占位符
			if str, ok := val.(string); ok && len(str) > config.maxAttributeBytes {
				replaced := replaceMultimediaWithPlaceholders(str)
				if replaced != str {
					aiLog[key] = replaced
					log.Debugf("[buildAILogRecord] replaced multimedia in %s: %d -> %d bytes", key, len(str), len(replaced))
				}
			}
		}
		newLen := len(fmt.Sprint(aiLog[key]))
		if origLen != newLen {
			log.Infof("[AI-STAT-DEBUG] summarize: key=%s %d->%d bytes", key, origLen, newLen)
		}
	}

	// 构建复合 answer 对象：统一包含 content / function_call / tool_calls / reasoning
	if v := ctx.GetUserAttribute("answer"); v != nil {
		if jsonStr, ok := v.(string); ok && strings.HasPrefix(jsonStr, "{") {
			var answerMap map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &answerMap); err == nil {
				// 对 content 和 reasoning 做超长截断
				if content, ok := answerMap["content"].(string); ok {
					answerMap["content"] = summarizeAttribute("answer", content, config.maxAttributeBytes)
				}
				if reasoning, ok := answerMap["reasoning"].(string); ok {
					answerMap["reasoning"] = summarizeAttribute("reasoning", reasoning, config.maxAttributeBytes)
				}
				if newJSON, err := json.Marshal(answerMap); err == nil {
					aiLog["answer"] = string(newJSON)
				} else {
					aiLog["answer"] = jsonStr
				}
			} else {
				aiLog["answer"] = summarizeAttribute("answer", jsonStr, config.maxAttributeBytes)
			}
		} else {
			// 非 JSON 字符串，包装为固定格式
			content := fmt.Sprint(v)
			answerMap := map[string]interface{}{
				"content":       summarizeAttribute("answer", content, config.maxAttributeBytes),
				"function_call": "",
				"tool_calls":    []interface{}{},
				"reasoning":     "",
			}
			newJSON, _ := json.Marshal(answerMap)
			aiLog["answer"] = string(newJSON)
		}
	} else {
		// 无 answer，生成空对象
		emptyAnswer := map[string]interface{}{
			"content":       "",
			"function_call": "",
			"tool_calls":    []interface{}{},
			"reasoning":     "",
		}
		emptyJSON, _ := json.Marshal(emptyAnswer)
		aiLog["answer"] = string(emptyJSON)
	}

	// 删除冗余字段：reasoning/tool_calls/function_call 已合并到 answer 中，
	// 避免日志中出现重复的 reasoning（流式场景下 WriteUserAttributeToLogWithKey
	// 会把这些字段写入 wasm.ai_log，导致 buildAILogRecord 加载后出现重复）
	delete(aiLog, "reasoning")
	delete(aiLog, "tool_calls")
	delete(aiLog, "function_call")
	log.Infof("[AI-STAT-DEBUG] removed redundant fields from aiLog: reasoning, tool_calls, function_call")

	record.AILog = aiLog

	// 记录 enforceSizeCap 前后的总大小
	beforeBytes, _ := json.Marshal(record)
	log.Infof("[AI-STAT-DEBUG] enforceSizeCap BEFORE: record=%d bytes maxLogBodyBytes=%d", len(beforeBytes), config.maxLogBodyBytes)
	enforceSizeCap(record, config.maxLogBodyBytes)

	afterBytes, _ := json.Marshal(record)
	log.Infof("[AI-STAT-DEBUG] enforceSizeCap AFTER: record=%d bytes", len(afterBytes))

	writeRawAILogToFilterState(record.AILog)
	writeTopLevelFields(record)

	return record
}

// dataURIPattern 匹配 data URI 格式的多媒体内容（OpenAI 等标准格式）
// 例如: data:image/jpeg;base64,/9j/4AAQ... 或 data:video/mp4;base64,...
var dataURIPattern = regexp.MustCompile(`(?i)data:([a-z]+)/[a-z0-9+-]+;base64,[A-Za-z0-9+/]{100,}={0,2}`)

// jsonBase64Pattern 匹配 JSON 中带引号的纯 base64 长字符串
var jsonBase64Pattern = regexp.MustCompile(`(?i)["']([A-Za-z0-9+/]{800,}={0,2})["']`)

// standaloneBase64Pattern 匹配 Go fmt 输出、Anthropic 格式等非 data URI 的独立 base64 块
// 前后必须是分隔符（空白、标点、map key 等），避免误匹配普通长单词
// Anthropic 格式: source:map[type:base64 media_type:image/jpeg data:AAAA...]
var standaloneBase64Pattern = regexp.MustCompile(`(?i)\b(data:)([A-Za-z0-9+/]{500,}={0,2})\b`)

// replaceMultimediaWithPlaceholders 将多模态 base64 内容替换为短占位符，
// 最大化保留文字内容。image/video/audio 分别替换为 [image]/[video]/[audio]，
// 其他类型替换为 [file] 或 [base64 data]。
func replaceMultimediaWithPlaceholders(str string) string {
	// 1. 替换 data URI 格式的多媒体内容（OpenAI 标准格式）
	str = dataURIPattern.ReplaceAllStringFunc(str, func(match string) string {
		parts := strings.SplitN(match, ":", 2)
		if len(parts) < 2 {
			return "[media]"
		}
		mediaPart := strings.SplitN(parts[1], ";", 2)[0]
		mediaType := strings.SplitN(mediaPart, "/", 2)[0]
		switch strings.ToLower(mediaType) {
		case "image":
			return "[image]"
		case "video":
			return "[video]"
		case "audio":
			return "[audio]"
		default:
			return "[file]"
		}
	})

	// 2. 替换 JSON 中带引号的纯 base64 长字符串
	str = jsonBase64Pattern.ReplaceAllStringFunc(str, func(match string) string {
		return `"[base64 data]"`
	})

	// 3. 替换 Anthropic / Go fmt 等非 data URI 的独立 base64 块
	// 保留 "data:" 前缀，替换 base64 内容为占位符
	str = standaloneBase64Pattern.ReplaceAllStringFunc(str, func(match string) string {
		return "data:[base64 data]"
	})

	return str
}

func summarizeAttribute(key string, value interface{}, maxBytes int) interface{} {
	str := fmt.Sprint(value)
	log.Infof("[AI-STAT-DEBUG] summarizeAttribute START: key=%s len=%d maxBytes=%d", key, len(str), maxBytes)

	// 如果不超过限制，保留原始内容
	if len(str) <= maxBytes {
		log.Infof("[AI-STAT-DEBUG] summarizeAttribute SKIP: key=%s %d<=%d", key, len(str), maxBytes)
		return str
	}

	// 超长：先将多模态内容替换为短占位符，腾出空间保留文字
	str = replaceMultimediaWithPlaceholders(str)
	log.Infof("[AI-STAT-DEBUG] summarizeAttribute AFTER replaceMM: key=%s len=%d", key, len(str))

	// 替换后仍超长，再截断
	if len(str) <= maxBytes {
		log.Infof("[AI-STAT-DEBUG] summarizeAttribute RETURN replaced: key=%s %d<=%d", key, len(str), maxBytes)
		return str
	}

	var truncated string
	if key == "question" {
		// question 优先删除多模态占位符，尽量保留完整 text
		cleaned := cleanMultimediaResiduals(str)
		log.Infof("[AI-STAT-DEBUG] summarizeAttribute AFTER cleanMM: key=%s len=%d", key, len(cleaned))
		if len(cleaned) <= maxBytes {
			return cleaned
		}
		truncated = cleaned[:maxBytes] + "..."
	} else {
		// 其他字段（answer / reasoning 等）保持现状：两端保留、中间截断（保留上下文）
		truncated = str[:maxBytes/2] + "..." + strconv.Itoa(len(str)-maxBytes) + "B>" + str[len(str)-maxBytes/2:]
	}
	log.Infof("[AI-STAT-DEBUG] summarizeAttribute TRUNCATED: key=%s %d->%d", key, len(str), len(truncated))
	return truncated
}

func summarizeMessages(value interface{}, maxBytes int) interface{} {
	str := fmt.Sprint(value)
	log.Infof("[AI-STAT-DEBUG] summarizeMessages START: len=%d maxBytes=%d", len(str), maxBytes)

	// 如果不超过限制，保留原始内容
	if len(str) <= maxBytes {
		log.Infof("[AI-STAT-DEBUG] summarizeMessages SKIP: %d<=%d", len(str), maxBytes)
		return str
	}

	raw, ok := value.(string)
	if !ok {
		return summarizeAttribute("messages", value, maxBytes)
	}
	result := gjson.Parse(raw)
	if !result.IsArray() {
		return summarizeAttribute("messages", value, maxBytes)
	}
	arr := result.Array()
	if len(arr) <= 4 {
		return summarizeAttribute("messages", value, maxBytes)
	}
	var buf bytes.Buffer
	buf.WriteString("[")
	for i := 0; i < 2 && i < len(arr); i++ {
		if i > 0 {
			buf.WriteString(",")
		}
		// 超长时才替换每条消息中的多模态内容为占位符
		truncatedMsg := replaceMultimediaWithPlaceholders(arr[i].Raw)
		buf.WriteString(truncatedMsg)
	}
	truncatedCount := len(arr) - 4
	buf.WriteString(fmt.Sprintf(`,"[ ... %d conversation rounds truncated (original %d rounds, %d bytes) ... ]"`, truncatedCount, len(arr), len(str)))
	for i := len(arr) - 2; i < len(arr); i++ {
		if i >= 0 {
			buf.WriteString(",")
			truncatedMsg := replaceMultimediaWithPlaceholders(arr[i].Raw)
			buf.WriteString(truncatedMsg)
		}
	}
	buf.WriteString("]")
	return buf.String()
}

func enforceSizeCap(record *AILogRecord, maxBytes int) {
	if maxBytes <= 0 || record.AILog == nil {
		log.Infof("[AI-STAT-DEBUG] enforceSizeCap SKIP: maxBytes=%d aiLogNil=%v", maxBytes, record.AILog == nil)
		return
	}

	trial, _ := json.Marshal(record)
	log.Infof("[AI-STAT-DEBUG] enforceSizeCap START: record=%d bytes maxBytes=%d overLimit=%v", len(trial), maxBytes, len(trial) > maxBytes)

	// ========== Step 1: 日志整体超长时，才将多模态内容替换为短占位符 ==========
	// 先检查总大小，只有在超过限制后才进行替换，避免不超长时破坏原始内容
	if len(trial) > maxBytes {
		log.Infof("[AI-STAT-DEBUG] enforceSizeCap Step1: total %d > %d, replacing multimedia", len(trial), maxBytes)
		for key, val := range record.AILog {
			if str, ok := val.(string); ok && len(str) > 500 {
				replaced := replaceMultimediaWithPlaceholders(str)
				if replaced != str {
					record.AILog[key] = replaced
					log.Infof("[AI-STAT-DEBUG] enforceSizeCap Step1: replaced %s %d->%d", key, len(str), len(replaced))
				}
			}
		}
	} else {
		log.Infof("[AI-STAT-DEBUG] enforceSizeCap Step1 SKIP: total %d <= %d", len(trial), maxBytes)
	}

	// ========== Step 2: 智能截断 —— 按字段大小排序，从大到小截断 ==========
	// 收集所有可截断的非关键字段，按大小降序排列
	type fieldSize struct {
		key  string
		size int
	}
	var fields []fieldSize
	for key, val := range record.AILog {
		if isCriticalField(key) {
			continue // 关键字段永不截断
		}
		size := len(fmt.Sprint(val))
		if size > 200 { // 只处理大于 200 字节的字段
			fields = append(fields, fieldSize{key, size})
		}
	}
	// 按大小降序排序
	for i := 0; i < len(fields); i++ {
		for j := i + 1; j < len(fields); j++ {
			if fields[j].size > fields[i].size {
				fields[i], fields[j] = fields[j], fields[i]
			}
		}
	}

	truncated := []string{}
	for _, f := range fields {
		trial, _ := json.Marshal(record)
		if len(trial) <= maxBytes {
			break // 已满足限制
		}
		val := record.AILog[f.key]
		str := fmt.Sprint(val)
		if len(str) <= 200 {
			continue
		}

		// 动态计算截断后长度：目标是把当前日志压缩到 maxBytes 的 80%
		overhead := len(trial) - maxBytes
		targetLen := len(str) - overhead - 50 // 留 50 字节余量给截断标记
		if targetLen < 100 {
			targetLen = 100 // 最少保留 100 字节
		}
		if targetLen > len(str) {
			continue // 不需要截断
		}

		if f.key == "question" {
			// question 保留前面固定长度，其余全用省略号代替（不保留尾部字节数统计）
			record.AILog[f.key] = str[:targetLen] + "..."
			truncated = append(truncated, f.key)
			log.Debugf("[enforceSizeCap] truncated %s: %d -> ~%d bytes", f.key, len(str), targetLen)
		} else if f.key == "answer" {
			// answer 是 JSON 字符串，尝试解析并缩短内部字段
			smartTruncated := false
			if jsonStr, ok := record.AILog[f.key].(string); ok {
				var answerObj map[string]interface{}
				if err := json.Unmarshal([]byte(jsonStr), &answerObj); err == nil {
					half := targetLen / 4
					if content, ok := answerObj["content"].(string); ok && len(content) > half {
						answerObj["content"] = content[:half] + "..."
					}
					if reasoning, ok := answerObj["reasoning"].(string); ok && len(reasoning) > half {
						answerObj["reasoning"] = reasoning[:half] + "..."
					}
					if newJSON, err := json.Marshal(answerObj); err == nil {
						record.AILog[f.key] = string(newJSON)
						smartTruncated = true
						truncated = append(truncated, f.key)
						log.Debugf("[enforceSizeCap] smart truncated answer JSON: %d -> %d bytes", len(jsonStr), len(newJSON))
					}
				}
			}
			if !smartTruncated {
				half := targetLen / 2
				record.AILog[f.key] = str[:half] +
					fmt.Sprintf("...%dB>", len(str)-targetLen) +
					str[len(str)-half:]
				truncated = append(truncated, f.key)
				log.Debugf("[enforceSizeCap] truncated %s: %d -> ~%d bytes", f.key, len(str), targetLen)
			}
		} else {
			// 其他字段两端保留、中间截断（保留上下文）
			half := targetLen / 2
			record.AILog[f.key] = str[:half] +
				fmt.Sprintf("...%dB>", len(str)-targetLen) +
				str[len(str)-half:]
			truncated = append(truncated, f.key)
			log.Debugf("[enforceSizeCap] truncated %s: %d -> ~%d bytes", f.key, len(str), targetLen)
		}
	}

	// ========== Step 3: 如果仍然超限，删除已截断的字段（最后手段） ==========
	for _, f := range fields {
		trial, _ := json.Marshal(record)
		if len(trial) <= maxBytes {
			break
		}
		if _, exists := record.AILog[f.key]; exists {
			delete(record.AILog, f.key)
			log.Debugf("[enforceSizeCap] dropped %s to stay under %dKB", f.key, maxBytes/1024)
		}
	}

	// 记录截断操作
	if len(truncated) > 0 {
		record.AILog["_truncated_fields"] =
			fmt.Sprintf("truncated %v under %dKB", truncated, maxBytes/1024)
	}

	// ========== Step 4: 最终兜底 —— 强制硬上限 ==========
	for {
		finalBytes, _ := json.Marshal(record)
		if len(finalBytes) <= maxBytes {
			break
		}
		log.Warnf("[enforceSizeCap] hard cap: %d > %d bytes", len(finalBytes), maxBytes)
		// 找到最大的非关键字段并删除
		maxSize := 0
		maxKey := ""
		for key, val := range record.AILog {
			if isCriticalField(key) {
				continue
			}
			if size := len(fmt.Sprint(val)); size > maxSize {
				maxSize = size
				maxKey = key
			}
		}
		if maxKey == "" {
			break // 没有可删除的字段了
		}
		delete(record.AILog, maxKey)
		log.Warnf("[enforceSizeCap] hard cap dropped: %s", maxKey)
	}
}

// isCriticalField 判断字段是否为关键字段（不可删除）
func isCriticalField(key string) bool {
	switch key {
	case "consumer", "model", "input_token", "output_token", "total_token",
		"llm_service_duration", "llm_first_token_duration",
		"status_code", "failure_reason", "route_name", "cluster_name",
		"request_method", "request_path", "source_ip",
		"is_fallback_route", "_truncated_fields":
		return true
	}
	return false
}

func convertToUInt(val interface{}) (uint64, bool) {
	switch v := val.(type) {
	case float32:
		return uint64(v), true
	case float64:
		return uint64(v), true
	case int32:
		return uint64(v), true
	case int64:
		return uint64(v), true
	case uint32:
		return uint64(v), true
	case uint64:
		return v, true
	default:
		return 0, false
	}
}

func parseIP(source string) string {
	if source == "" {
		return "unknown"
	}

	if strings.Contains(source, ".") {
		if idx := strings.LastIndex(source, ":"); idx != -1 {
			return source[:idx]
		}
		return source
	}

	if strings.Contains(source, "[") && strings.Contains(source, "]") {
		if start := strings.Index(source, "["); start != -1 {
			if end := strings.Index(source, "]"); end != -1 {
				return source[start+1 : end]
			}
		}
	}

	if strings.Count(source, ":") >= 2 {
		if idx := strings.LastIndex(source, ":"); idx != -1 {
			return source[:idx]
		}
	}
	return source
}

func isValidIP(ip string) bool {
	return ip != "" && ip != "unknown" && net.ParseIP(ip) != nil
}

func collectBasicAILogInfo(ctx wrapper.HttpContext, aiLog map[string]interface{}, consumer string) {
	if aiLog == nil {
		return
	}
	// Use the consumer value already extracted in buildAILogRecord.
	// Do NOT call proxywasm.GetProperty here - repeated GetProperty calls
	// for the same key within the same function call chain may return empty
	// values due to Envoy WASM SDK internal caching/lifecycle.
	aiLog["consumer"] = consumer
	log.Infof("[AI-STATISTICS-DEBUG] collectBasicAILogInfo: consumer set to: %v", consumer)
	aiLog["route_name"] = ctx.GetStringContext(RouteName, "-")
	aiLog["cluster_name"] = ctx.GetStringContext(ClusterName, "-")
	if model := ctx.GetUserAttribute("model"); model != nil {
		aiLog["model"] = model
		log.Debugf("[AI-STATISTICS-DEBUG] collectBasicAILogInfo: model from user attribute: %v", model)
	} else if requestModel := ctx.GetContext(tokenusage.CtxKeyRequestModel); requestModel != nil {
		aiLog["model"] = requestModel
		log.Debugf("[AI-STATISTICS-DEBUG] collectBasicAILogInfo: model from context fallback: %v", requestModel)
	} else {
		log.Debugf("[AI-STATISTICS-DEBUG] collectBasicAILogInfo: model not found in user attribute or context")
	}
	if rm := ctx.GetUserAttribute("request_method"); rm != nil {
		aiLog["request_method"] = rm
	}
	if rp := ctx.GetUserAttribute("request_path"); rp != nil {
		aiLog["request_path"] = rp
	}
	if sessionId := ctx.GetUserAttribute(SessionID); sessionId != nil {
		aiLog["session_id"] = sessionId
	}
	statusCodeStr := ctx.GetStringContext(ResponseStatusCode, "0")
	if sc, _ := strconv.Atoi(statusCodeStr); sc > 0 {
		aiLog["status_code"] = sc
	}
	if failureReason := ctx.GetStringContext(CtxFailureReason, ""); failureReason != "" {
		aiLog["failure_reason"] = failureReason
	}
	// Mark fallback route scenario in ai_log for easier identification
	if isFallback := ctx.GetBoolContext(CtxIsFallbackRoute, false); isFallback {
		aiLog["is_fallback_route"] = true
	}
}