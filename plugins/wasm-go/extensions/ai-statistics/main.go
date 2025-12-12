package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/tokenusage"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/resp"
)

func main() {}

var (
	metricLocks = make(map[string]*sync.RWMutex)
	lockMutex   sync.Mutex // Protects metricLocks map access
)

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
	RouteName                  = "route"
	ClusterName                = "cluster"
	APIName                    = "api"
	ConsumerKey                = "x-mse-consumer"
	RequestPath                = "request_path"

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
)

// TracingSpan is the tracing span configuration.
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
	// Metrics
	// TODO: add more metrics in Gauge and Histogram format
	counterMetrics map[string]proxywasm.MetricCounter
	// Attributes to be recorded in log & span
	attributes []Attribute
	// If there exist attributes extracted from streaming body, chunks should be buffered
	shouldBufferStreamingBody bool
	// If disableOpenaiUsage is true, model/input_token/output_token logs will be skipped
	disableOpenaiUsage bool
	RedisClient        wrapper.RedisClient
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

func getClusterName() (string, error) {
	if raw, err := proxywasm.GetProperty([]string{"cluster_name"}); err != nil {
		return "-", err
	} else {
		return string(raw), nil
	}
}

// acquireMetricLock gets or creates a lock for a specific metric name
func acquireMetricLock(metricName string) *sync.RWMutex {
	lockMutex.Lock()
	defer lockMutex.Unlock()
	lock, exists := metricLocks[metricName]
	if !exists {
		lock = &sync.RWMutex{}
		metricLocks[metricName] = lock
	}
	return lock
}

func (config *AIStatisticsConfig) incrementCounter(metricName string, inc uint64) {
	if inc == 0 {
		return
	}

	// Acquire lock for this specific metric name to prevent race conditions during read-modify-write
	lock := acquireMetricLock(metricName)
	lock.Lock()
	defer lock.Unlock()

	// First, get the current value from Redis
	var currentRedisValue uint64 = 0
	if config.RedisClient != nil {
		log.Errorf("it is not error. redisClient is not null. now metricname is %s", metricName)
		err := config.RedisClient.Get(metricName, func(response resp.Value) {
			currentRedisValue = uint64(response.Integer())
		})
		if err != nil {
			log.Errorf("failed to get redis key %s,error is %v", metricName, err)
		}
	} else {
		log.Errorf("it is not error. redisClient is null, so it can not get key")
	}

	// Get the current local counter value (if it exists)
	localCounter, ok := config.counterMetrics[metricName]
	if !ok {
		localCounter = proxywasm.DefineCounterMetric(metricName)
		config.counterMetrics[metricName] = localCounter
	}

	baseValue := currentRedisValue
	log.Errorf("it is not error. currentRedisValue=%d, localCounter=%d", currentRedisValue, localCounter.Value())
	if currentRedisValue < localCounter.Value() {
		baseValue = localCounter.Value() // Use local value as base if it's larger
	}
	finalValue := baseValue + inc
	localCounter.Increment(finalValue - localCounter.Value())

	// Update Redis (if client is available)
	if config.RedisClient != nil {
		err := config.RedisClient.Set(metricName, finalValue, nil)
		if err != nil {
			log.Warnf("Failed to update Redis metric %s: %v", metricName, err)
		}
	}
}

func parseConfig(configJson gjson.Result, config *AIStatisticsConfig) error {
	log.Debugf("ai-statistics start parseConfig")
	// Parse tracing span attributes setting.
	attributeConfigs := configJson.Get("attributes").Array()
	config.attributes = make([]Attribute, len(attributeConfigs))
	for i, attributeConfig := range attributeConfigs {
		attribute := Attribute{}
		err := json.Unmarshal([]byte(attributeConfig.Raw), &attribute)
		if err != nil {
			log.Errorf("parse config failed, %v", err)
			return err
		}
		if attribute.ValueSource == ResponseStreamingBody {
			config.shouldBufferStreamingBody = true
		}
		if attribute.Rule != "" && attribute.Rule != RuleFirst && attribute.Rule != RuleReplace && attribute.Rule != RuleAppend {
			return errors.New("value of rule must be one of [nil, first, replace, append]")
		}
		config.attributes[i] = attribute
	}
	// Metric settings
	counter1 := proxywasm.DefineCounterMetric("gateway_model_metrics")
	if config.counterMetrics == nil {
		config.counterMetrics = make(map[string]proxywasm.MetricCounter)
	}
	config.counterMetrics["gateway_model_metrics"] = counter1

	// Parse openai usage config setting.
	config.disableOpenaiUsage = configJson.Get("disable_openai_usage").Bool()

	// Parse Redis address for persistence
	redisConfig := configJson.Get("redis")
	if redisConfig.Exists() {
		_ = InitRedisClusterClient(redisConfig, config)
	}

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
			// use default logic port which is 80 for static service
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
	if config.RedisClient.Ready() {
		log.Info("redis init successfully")
	} else {
		log.Error("redis init failed, will try later")
	}
	return err
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config AIStatisticsConfig) types.Action {
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
	if requestPath, _ := proxywasm.GetHttpRequestHeader(":path"); requestPath != "" {
		ctx.SetContext(RequestPath, requestPath)
	}
	if consumer, _ := proxywasm.GetHttpRequestHeader(ConsumerKey); consumer != "" {
		ctx.SetContext(ConsumerKey, consumer)
	}

	ctx.SetRequestBodyBufferLimit(defaultMaxBodyBytes)

	log.Debugf("ai-statistics start onHttpRequestHeaders/setAttributeBySource/SOURCEIP")
	// 先设置 source_ip
	setAttributeBySource(ctx, config, SourceIP, nil)
	log.Debugf("ai-statistics end onHttpRequestHeaders/setAttributeBySource/SOURCEIP")

	// Set user defined log & span attributes which type is fixed_value
	setAttributeBySource(ctx, config, FixedValue, nil)
	// Set user defined log & span attributes which type is request_header
	setAttributeBySource(ctx, config, RequestHeader, nil)
	// Set span attributes for ARMS.
	setSpanAttribute(ArmsSpanKind, "LLM")

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config AIStatisticsConfig, body []byte) types.Action {
	// Set user defined log & span attributes.
	setAttributeBySource(ctx, config, RequestBody, body)
	// Set span attributes for ARMS.
	requestModel := "UNKNOWN"
	if model := gjson.GetBytes(body, "model"); model.Exists() {
		requestModel = model.String()
	} else {
		requestPath := ctx.GetStringContext(RequestPath, "")
		if strings.Contains(requestPath, "generateContent") || strings.Contains(requestPath, "streamGenerateContent") { // Google Gemini GenerateContent
			reg := regexp.MustCompile(`^.*/(?P<api_version>[^/]+)/models/(?P<model>[^:]+):\w+Content$`)
			matches := reg.FindStringSubmatch(requestPath)
			if len(matches) == 3 {
				requestModel = matches[2]
			}
		}
	}
	setSpanAttribute(ArmsRequestModel, requestModel)
	// Set the number of conversation rounds

	userPromptCount := 0
	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		for _, msg := range messages.Array() {
			if msg.Get("role").String() == "user" {
				userPromptCount += 1
			}
		}
	} else if contents := gjson.GetBytes(body, "contents"); contents.Exists() && contents.IsArray() { // Google Gemini GenerateContent
		for _, content := range contents.Array() {
			if !content.Get("role").Exists() || content.Get("role").String() == "user" {
				userPromptCount += 1
			}
		}
	}
	ctx.SetUserAttribute(ChatRound, userPromptCount)

	// Write log
	ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)
	return types.ActionContinue
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config AIStatisticsConfig) types.Action {
	contentType, _ := proxywasm.GetHttpResponseHeader("content-type")
	if !strings.Contains(contentType, "text/event-stream") {
		ctx.BufferResponseBody()
	}

	// Set user defined log & span attributes.
	setAttributeBySource(ctx, config, ResponseHeader, nil)

	return types.ActionContinue
}

func onHttpStreamingBody(ctx wrapper.HttpContext, config AIStatisticsConfig, data []byte, endOfStream bool) []byte {
	// Buffer stream body for record log & span attributes
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
		"responseId", // Gemini generateContent
		"message.id", // anthropic messages
	}); chatID != nil {
		ctx.SetUserAttribute(ChatID, chatID.String())
	}

	// Get requestStartTime from http context
	requestStartTime, ok := ctx.GetContext(StatisticsRequestStartTime).(int64)
	if !ok {
		log.Error("failed to get requestStartTime from http context")
		return data
	}

	// If this is the first chunk, record first token duration metric and span attribute
	if ctx.GetContext(StatisticsFirstTokenTime) == nil {
		firstTokenTime := time.Now().UnixMilli()
		ctx.SetContext(StatisticsFirstTokenTime, firstTokenTime)
		ctx.SetUserAttribute(LLMFirstTokenDuration, firstTokenTime-requestStartTime)
	}

	// Set information about this request
	if !config.disableOpenaiUsage {
		if usage := tokenusage.GetTokenUsage(ctx, data); usage.TotalToken > 0 {
			// Set span attributes for ARMS.
			setSpanAttribute(ArmsTotalToken, usage.TotalToken)
			setSpanAttribute(ArmsModelName, usage.Model)
			setSpanAttribute(ArmsInputToken, usage.InputToken)
			setSpanAttribute(ArmsOutputToken, usage.OutputToken)
		}
	}
	// If the end of the stream is reached, record metrics/logs/spans.
	if endOfStream {
		responseEndTime := time.Now().UnixMilli()
		ctx.SetUserAttribute(LLMServiceDuration, responseEndTime-requestStartTime)

		// Set user defined log & span attributes.
		if config.shouldBufferStreamingBody {
			streamingBodyBuffer, ok := ctx.GetContext(CtxStreamingBodyBuffer).([]byte)
			if !ok {
				return data
			}
			setAttributeBySource(ctx, config, ResponseStreamingBody, streamingBodyBuffer)
		}

		// Write log
		ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)

		// Write metrics
		writeMetric(ctx, config)
	}
	return data
}

func onHttpResponseBody(ctx wrapper.HttpContext, config AIStatisticsConfig, body []byte) types.Action {
	// Get requestStartTime from http context
	requestStartTime, _ := ctx.GetContext(StatisticsRequestStartTime).(int64)

	responseEndTime := time.Now().UnixMilli()
	ctx.SetUserAttribute(LLMServiceDuration, responseEndTime-requestStartTime)

	ctx.SetUserAttribute(ResponseType, "normal")
	if chatID := wrapper.GetValueFromBody(body, []string{
		"id",
		"response.id",
		"responseId", // Gemini generateContent
		"message.id", // anthropic messages
	}); chatID != nil {
		ctx.SetUserAttribute(ChatID, chatID.String())
	}

	// Set information about this request
	if !config.disableOpenaiUsage {
		if usage := tokenusage.GetTokenUsage(ctx, body); usage.TotalToken > 0 {
			// Set span attributes for ARMS.
			setSpanAttribute(ArmsModelName, usage.Model)
			setSpanAttribute(ArmsInputToken, usage.InputToken)
			setSpanAttribute(ArmsOutputToken, usage.OutputToken)
			setSpanAttribute(ArmsTotalToken, usage.TotalToken)
		}
	}

	// Set user defined log & span attributes.
	setAttributeBySource(ctx, config, ResponseBody, body)

	// Write log
	ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)

	// Write metrics
	writeMetric(ctx, config)

	return types.ActionContinue
}

// fetches the tracing span value from the specified source.

func setAttributeBySource(ctx wrapper.HttpContext, config AIStatisticsConfig, source string, body []byte) {
	for _, attribute := range config.attributes {
		var key string
		var value interface{}
		if source == attribute.ValueSource {
			key = attribute.Key
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
				value = extractStreamingBodyByJsonPath(body, attribute.Value, attribute.Rule)
			case ResponseBody:
				value = gjson.GetBytes(body, attribute.Value).Value()
			case SourceIP:
				// 1. 先尝试从 X-Forwarded-For 获取
				value = "unknown"
				// 1. 优先从 source.address 获取 (eBPF PPv2 注入后的真实 IP)
				if bs, err := proxywasm.GetProperty([]string{"source", "address"}); err == nil {
					rawSource := string(bs)
					sourceIP := parseIP(rawSource)
					if isValidIP(sourceIP) {
						value = sourceIP
						// 打印 Info 级别日志验证 eBPF 是否生效
						log.Infof("[Check-eBPF] Got Source IP from connection: %s (Raw: %s)", value, rawSource)
					}
				}

				// 2. 如果上面的方式拿到的是内网 IP 或者是 unknown，尝试 XFF (可选，视你的信任策略而定)
				// 注意：如果你确定 eBPF 正常工作，其实不需要 XFF 了，因为 source.address 是最可信的
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

				// 3. 兜底
				if value == "" {
					value = "unknown"
				}
			default:
			}
			if (value == nil || value == "") && attribute.DefaultValue != "" {
				value = attribute.DefaultValue
			}
			log.Debugf("[attribute] source type: %s, key: %s, value: %+v", source, key, value)
			if attribute.ApplyToLog {
				if attribute.AsSeparateLogField {
					marshalledJsonStr := wrapper.MarshalStr(fmt.Sprint(value))
					if err := proxywasm.SetProperty([]string{key}, []byte(marshalledJsonStr)); err != nil {
						log.Warnf("failed to set %s in filter state, raw is %s, err is %v", key, marshalledJsonStr, err)
					}
				} else {
					ctx.SetUserAttribute(key, value)
				}
			}
			// for metrics
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
		// extract llm response
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

// Set the tracing span with value.
func setSpanAttribute(key string, value interface{}) {
	if value != "" {
		traceSpanTag := wrapper.TraceSpanTagPrefix + key
		if e := proxywasm.SetProperty([]string{traceSpanTag}, []byte(fmt.Sprint(value))); e != nil {
			log.Warnf("failed to set %s in filter state: %v", traceSpanTag, e)
		}
	} else {
		log.Debugf("failed to write span attribute [%s], because it's value is empty")
	}
}

func writeMetric(ctx wrapper.HttpContext, config AIStatisticsConfig) {
	// Generate usage metrics
	var ok bool
	var route, cluster, model string
	consumer := ctx.GetStringContext(ConsumerKey, "none")
	route, ok = ctx.GetContext(RouteName).(string)
	if !ok {
		log.Warnf("RouteName typd assert failed, skip metric record")
		return
	}
	cluster, ok = ctx.GetContext(ClusterName).(string)
	if !ok {
		log.Warnf("ClusterName typd assert failed, skip metric record")
		return
	}

	if config.disableOpenaiUsage {
		return
	}

	if ctx.GetUserAttribute(tokenusage.CtxKeyModel) == nil || ctx.GetUserAttribute(tokenusage.CtxKeyInputToken) == nil || ctx.GetUserAttribute(tokenusage.CtxKeyOutputToken) == nil || ctx.GetUserAttribute(tokenusage.CtxKeyTotalToken) == nil {
		log.Warnf("get usage information failed, skip metric record")
		return
	}
	model, ok = ctx.GetUserAttribute(tokenusage.CtxKeyModel).(string)
	if !ok {
		log.Warnf("Model typd assert failed, skip metric record")
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
		log.Warnf("InputToken typd assert failed, skip metric record")
	}
	if outputToken, ok := convertToUInt(ctx.GetUserAttribute(tokenusage.CtxKeyOutputToken)); ok {
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, tokenusage.CtxKeyOutputToken), outputToken)
	} else {
		log.Warnf("OutputToken typd assert failed, skip metric record")
	}
	if totalToken, ok := convertToUInt(ctx.GetUserAttribute(tokenusage.CtxKeyTotalToken)); ok {
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, tokenusage.CtxKeyTotalToken), totalToken)
	} else {
		log.Warnf("TotalToken typd assert failed, skip metric record")
	}

	// Generate duration metrics
	var llmFirstTokenDuration, llmServiceDuration uint64
	// Is stream response
	if ctx.GetUserAttribute(LLMFirstTokenDuration) != nil {
		llmFirstTokenDuration, ok = convertToUInt(ctx.GetUserAttribute(LLMFirstTokenDuration))
		if !ok {
			log.Warnf("LLMFirstTokenDuration typd assert failed")
			return
		}
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, LLMFirstTokenDuration), llmFirstTokenDuration)
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, LLMStreamDurationCount), 1)
	}
	if ctx.GetUserAttribute(LLMServiceDuration) != nil {
		llmServiceDuration, ok = convertToUInt(ctx.GetUserAttribute(LLMServiceDuration))
		if !ok {
			log.Warnf("LLMServiceDuration typd assert failed")
			return
		}
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, LLMServiceDuration), llmServiceDuration)
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, LLMDurationCount), 1)
	}
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

	// IPv4
	if strings.Contains(source, ".") {
		if idx := strings.LastIndex(source, ":"); idx != -1 {
			return source[:idx]
		}
		return source
	}

	// IPv6
	if strings.Contains(source, "[") && strings.Contains(source, "]") {
		if start := strings.Index(source, "["); start != -1 {
			if end := strings.Index(source, "]"); end != -1 {
				return source[start+1 : end]
			}
		}
	}

	// 可能是纯 IPv6 地址
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
