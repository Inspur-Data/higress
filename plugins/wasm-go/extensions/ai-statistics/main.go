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
	"sync/atomic"
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
	counterMetrics map[string]proxywasm.MetricCounter
	//存储每个 metric 的准确值（以 Redis 为准）
	counterValues map[string]uint64
	//保护 counterValues 的互斥锁
	counterMutex sync.RWMutex
	// Attributes to be recorded in log & span
	attributes []Attribute
	// If there exist attributes extracted from streaming body, chunks should be buffered
	shouldBufferStreamingBody bool
	// If disableOpenaiUsage is true, model/input_token/output_token logs will be skipped
	disableOpenaiUsage bool
	RedisClient        wrapper.RedisClient
	redisInitialized   atomic.Bool
	//新增：标记 metrics 是否已加载
	metricsLoaded atomic.Bool
}

type redisOperationStatus struct {
	done atomic.Bool
}

// 新增：供外部接口获取准确 metric 值的方法
func (config *AIStatisticsConfig) GetMetricValue(metricName string) uint64 {
	config.counterMutex.RLock()
	defer config.counterMutex.RUnlock()
	return config.counterValues[metricName]
}

func (config *AIStatisticsConfig) setCounterValue(metricName string, value uint64) {
	config.counterMutex.Lock()
	defer config.counterMutex.Unlock()
	config.counterValues[metricName] = value
}

func (config *AIStatisticsConfig) getCounterValue(metricName string) (uint64, bool) {
	config.counterMutex.RLock()
	defer config.counterMutex.RUnlock()
	val, ok := config.counterValues[metricName]
	return val, ok
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

func (config *AIStatisticsConfig) incrementCounter(metricName string, inc uint64) {
	if inc == 0 {
		return
	}

	// 确保 metrics 已加载
	if !config.metricsLoaded.Load() {
		log.Warnf("Metrics not loaded yet, triggering load")
		loadMetricsFromRedis(config)
	}

	if config.RedisClient != nil && config.RedisClient.Ready() && config.redisInitialized.Load() {
		config.incrementWithRedisSmart(metricName, inc)
	} else {
		config.incrementWithNoRedis(metricName, inc)
	}
}

func (config *AIStatisticsConfig) incrementWithRedisSmart(metricName string, inc uint64) {
	localCounter, exists := config.counterMetrics[metricName]
	if !exists {
		localCounter = proxywasm.DefineCounterMetric(metricName)
		config.counterMetrics[metricName] = localCounter
		// 初始化时从 Redis 获取当前值
		config.syncMetricFromRedis(metricName)
	}

	opStatus := &redisOperationStatus{}
	opStatus.done.Store(false)

	redisErr := config.RedisClient.IncrBy(metricName, int(inc), func(response resp.Value) {
		defer opStatus.done.Store(true)

		if response.Error() != nil {
			log.Warnf("Redis error in callback: %v", response.Error())
			return
		}

		// 从 response 获取最新值并更新本地准确值映射
		newValue := response.Integer()
		log.Debugf("Redis incremented %s to %d", metricName, newValue)

		// 以 Redis 返回的值为权威，更新本地存储的准确值
		config.setCounterValue(metricName, uint64(newValue))
	})

	if redisErr != nil {
		log.Warnf("Redis unavailable, using local: %v", redisErr)
		config.incrementWithNoRedis(metricName, inc)
		return
	}

	// 等待 Redis 操作完成（最长 2 秒）
	waitForRedisOperation(opStatus, fmt.Sprintf("INCRBY %s", metricName), 200)
}

// 新增：从 Redis 同步单个 metric 的当前值
func (config *AIStatisticsConfig) syncMetricFromRedis(metricName string) {
	opStatus := &redisOperationStatus{}
	var synced bool
	var value uint64

	getErr := config.RedisClient.Get(metricName, func(getResponse resp.Value) {
		defer opStatus.done.Store(true)

		if getResponse.Error() != nil {
			log.Warnf("Redis GET error for key %s: %v", metricName, getResponse.Error())
			return
		}

		valueStr := getResponse.String()
		_, parseErr := fmt.Sscanf(valueStr, "%d", &value)
		if parseErr != nil {
			log.Warnf("Failed to parse value for key %s: %s", metricName, valueStr)
			return
		}

		synced = true
		config.setCounterValue(metricName, value)
		log.Debugf("Synced metric %s from Redis, value: %d", metricName, value)
	})

	if getErr != nil {
		log.Warnf("Failed to sync metric %s from Redis: %v", metricName, getErr)
		return
	}

	waitForRedisOperation(opStatus, fmt.Sprintf("Sync GET %s", metricName), 200)

	if !synced {
		// 如果 Redis 中没有该 key，初始化为 0
		config.setCounterValue(metricName, 0)
	}
}

func (config *AIStatisticsConfig) incrementWithNoRedis(metricName string, inc uint64) {
	localCounter, ok := config.counterMetrics[metricName]
	if !ok {
		localCounter = proxywasm.DefineCounterMetric(metricName)
		config.counterMetrics[metricName] = localCounter
	}
	localCounter.Increment(inc)
	log.Debugf("Local increment: %s +%d", metricName, inc)

	// 无 Redis 时，更新本地准确值
	if currentVal, exists := config.getCounterValue(metricName); exists {
		config.setCounterValue(metricName, currentVal+inc)
	} else {
		config.setCounterValue(metricName, inc)
	}
}

// 改造：将加载逻辑移出 parseConfig，改为懒加载
func parseConfig(configJson gjson.Result, config *AIStatisticsConfig) error {
	log.Debugf("ai-statistics start parseConfig")

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

	if config.counterMetrics == nil {
		config.counterMetrics = make(map[string]proxywasm.MetricCounter)
		config.counterValues = make(map[string]uint64)
	}

	config.disableOpenaiUsage = configJson.Get("disable_openai_usage").Bool()

	redisConfig := configJson.Get("redis")
	if redisConfig.Exists() {
		err := InitRedisClusterClient(redisConfig, config)
		if err != nil {
			log.Errorf("Failed to initialize Redis: %v", err)
			return err
		}

		// 注意：这里不再立即加载 metrics，改为在第一次 incrementCounter 时懒加载
		if config.RedisClient != nil && config.RedisClient.Ready() {
			log.Info("Redis initialized successfully, metrics will be loaded on first use")
		}
	}

	return nil
}

// 改造：添加重试机制和更详细的日志
func loadMetricsFromRedis(config *AIStatisticsConfig) {
	if config.metricsLoaded.Load() {
		log.Debugf("Metrics already loaded, skipping")
		return
	}

	log.Infof("Starting to load metrics from Redis. Client ready: %v, Initialized: %v",
		config.RedisClient.Ready(), config.redisInitialized.Load())

	prefix := "route."
	opStatus := &redisOperationStatus{}
	var metricsKeys []string
	var callbackInvoked atomic.Bool

	// 使用 Command 方法执行 KEYS 命令
	log.Infof("Dispatching KEYS command with prefix: %s*", prefix)
	keysErr := config.RedisClient.Command([]interface{}{"KEYS", prefix + "*"}, func(response resp.Value) {
		callbackInvoked.Store(true)
		log.Infof("KEYS callback invoked! Response error: %v", response.Error())
		defer opStatus.done.Store(true)

		if response.Error() != nil {
			log.Errorf("Redis KEYS error: %v", response.Error())
			return
		}

		// 解析返回的 keys
		keys := response.Array()
		log.Infof("Found %d metric keys in Redis", len(keys))

		// 提取 key 名称
		for _, keyValue := range keys {
			metricName := keyValue.String()
			if metricName != "" {
				metricsKeys = append(metricsKeys, metricName)
			}
		}
	})

	if keysErr != nil {
		log.Errorf("Failed to execute KEYS command: %v", keysErr)
		return
	}

	log.Infof("KEYS command dispatched successfully, waiting for callback...")
	waitForRedisOperation(opStatus, "KEYS", 200)

	if !callbackInvoked.Load() {
		log.Errorf("CRITICAL: KEYS callback was never invoked!")
		return
	}

	log.Infof("Found %d keys to sync: %v", len(metricsKeys), metricsKeys)

	// 为每个 key 执行 GET 操作
	for i, metricName := range metricsKeys {
		log.Infof("Syncing metric %d/%d: %s", i+1, len(metricsKeys), metricName)
		getOpStatus := &redisOperationStatus{}
		var getCallbackInvoked atomic.Bool

		getErr := config.RedisClient.Get(metricName, func(getResponse resp.Value) {
			getCallbackInvoked.Store(true)
			log.Infof("GET callback invoked for %s, error: %v", metricName, getResponse.Error())
			defer getOpStatus.done.Store(true)

			if getResponse.Error() != nil {
				log.Warnf("Redis GET error for key %s: %v", metricName, getResponse.Error())
				return
			}

			// 获取值并转换为 uint64
			valueStr := getResponse.String()
			var value uint64
			_, parseErr := fmt.Sscanf(valueStr, "%d", &value)
			if parseErr != nil {
				log.Warnf("Failed to parse value for key %s: %s", metricName, valueStr)
				return
			}

			// 初始化本地计数器
			localCounter := proxywasm.DefineCounterMetric(metricName)
			log.Infof("Loading metric %s with value %d from Redis", metricName, value)
			config.counterMetrics[metricName] = localCounter

			// 重要：存储准确值到本地映射（以 Redis 为准）
			config.setCounterValue(metricName, value)
		})

		if getErr != nil {
			log.Warnf("Failed to execute GET for key %s: %v", metricName, getErr)
			continue
		}

		waitForRedisOperation(getOpStatus, fmt.Sprintf("GET %s", metricName), 200)

		if !getCallbackInvoked.Load() {
			log.Errorf("CRITICAL: GET callback for %s was never invoked!", metricName)
		}
	}

	config.metricsLoaded.Store(true)
	log.Info("Redis metrics loading completed")
}

// 改造：增加详细日志和状态检查
func waitForRedisOperation(opStatus *redisOperationStatus, operationName string, maxRetries int) {
	log.Infof("Waiting for Redis operation %s (max retries: %d)", operationName, maxRetries)

	for i := 0; i < maxRetries; i++ {
		if opStatus.done.Load() {
			log.Infof("Redis operation %s completed after %d attempts", operationName, i+1)
			return
		}

		// 每 100 次打印一次日志
		if i%100 == 0 {
			log.Warnf("Still waiting for Redis operation %s, attempt %d/%d", operationName, i, maxRetries)
		}

		time.Sleep(10 * time.Millisecond)
	}

	log.Errorf("Redis operation %s TIMEOUT after %d attempts", operationName, maxRetries)
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

	if config.RedisClient.Ready() {
		log.Info("Redis init successfully")
		config.redisInitialized.Store(true)
	} else {
		log.Error("redis init failed, will try later")
		config.redisInitialized.Store(false)
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
	setAttributeBySource(ctx, config, SourceIP, nil)
	log.Debugf("ai-statistics end onHttpRequestHeaders/setAttributeBySource/SOURCEIP")

	setAttributeBySource(ctx, config, FixedValue, nil)
	setAttributeBySource(ctx, config, RequestHeader, nil)
	setSpanAttribute(ArmsSpanKind, "LLM")

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config AIStatisticsConfig, body []byte) types.Action {
	setAttributeBySource(ctx, config, RequestBody, body)
	requestModel := "UNKNOWN"
	if model := gjson.GetBytes(body, "model"); model.Exists() {
		requestModel = model.String()
	} else {
		requestPath := ctx.GetStringContext(RequestPath, "")
		if strings.Contains(requestPath, "generateContent") || strings.Contains(requestPath, "streamGenerateContent") {
			reg := regexp.MustCompile(`^.*/(?P<api_version>[^/]+)/models/(?P<model>[^:]+):\w+Content$`)
			matches := reg.FindStringSubmatch(requestPath)
			if len(matches) == 3 {
				requestModel = matches[2]
			}
		}
	}
	setSpanAttribute(ArmsRequestModel, requestModel)

	userPromptCount := 0
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
	ctx.SetUserAttribute(ChatRound, userPromptCount)

	ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)
	return types.ActionContinue
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config AIStatisticsConfig) types.Action {
	contentType, _ := proxywasm.GetHttpResponseHeader("content-type")
	if !strings.Contains(contentType, "text/event-stream") {
		ctx.BufferResponseBody()
	}

	setAttributeBySource(ctx, config, ResponseHeader, nil)

	return types.ActionContinue
}

func onHttpStreamingBody(ctx wrapper.HttpContext, config AIStatisticsConfig, data []byte, endOfStream bool) []byte {
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
		}
	}

	if endOfStream {
		responseEndTime := time.Now().UnixMilli()
		ctx.SetUserAttribute(LLMServiceDuration, responseEndTime-requestStartTime)

		if config.shouldBufferStreamingBody {
			streamingBodyBuffer, ok := ctx.GetContext(CtxStreamingBodyBuffer).([]byte)
			if !ok {
				return data
			}
			setAttributeBySource(ctx, config, ResponseStreamingBody, streamingBodyBuffer)
		}

		ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)
		writeMetric(ctx, config)
	}
	return data
}

func onHttpResponseBody(ctx wrapper.HttpContext, config AIStatisticsConfig, body []byte) types.Action {
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
		}
	}

	setAttributeBySource(ctx, config, ResponseBody, body)

	ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)

	writeMetric(ctx, config)

	return types.ActionContinue
}

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
	var ok bool
	var route, cluster, model string
	consumer := ctx.GetStringContext(ConsumerKey, "none")
	route, ok = ctx.GetContext(RouteName).(string)
	if !ok {
		log.Warnf("RouteName type assert failed, skip metric record")
		return
	}
	cluster, ok = ctx.GetContext(ClusterName).(string)
	if !ok {
		log.Warnf("ClusterName type assert failed, skip metric record")
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
		log.Warnf("Model type assert failed, skip metric record")
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

	// 使用准确值进行递增
	if inputToken, ok := convertToUInt(ctx.GetUserAttribute(tokenusage.CtxKeyInputToken)); ok {
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, tokenusage.CtxKeyInputToken), inputToken)
	} else {
		log.Warnf("InputToken type assert failed, skip metric record")
	}
	if outputToken, ok := convertToUInt(ctx.GetUserAttribute(tokenusage.CtxKeyOutputToken)); ok {
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, tokenusage.CtxKeyOutputToken), outputToken)
	} else {
		log.Warnf("OutputToken type assert failed, skip metric record")
	}
	if totalToken, ok := convertToUInt(ctx.GetUserAttribute(tokenusage.CtxKeyTotalToken)); ok {
		config.incrementCounter(generateMetricName(route, cluster, model, consumer, sourceIP, tokenusage.CtxKeyTotalToken), totalToken)
	} else {
		log.Warnf("TotalToken type assert failed, skip metric record")
	}

	var llmFirstTokenDuration, llmServiceDuration uint64
	if ctx.GetUserAttribute(LLMFirstTokenDuration) != nil {
		llmFirstTokenDuration, ok = convertToUInt(ctx.GetUserAttribute(LLMFirstTokenDuration))
		if !ok {
			log.Warnf("LLMFirstTokenDuration type assert failed")
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
