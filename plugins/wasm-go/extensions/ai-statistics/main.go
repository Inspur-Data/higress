package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/tokenusage"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

func main() {}

func init() {
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

// prometheusLabels contains all label values for metrics
type prometheusLabels struct {
	Route    string
	Cluster  string
	Model    string
	Consumer string
}

// AIStatisticsConfig configuration
type AIStatisticsConfig struct {
	counterMetrics            map[string]proxywasm.MetricCounter
	attributes                []Attribute
	shouldBufferStreamingBody bool
	disableOpenaiUsage        bool
}

// sanitizeLabelValue cleans label values to be Prometheus-compatible
func sanitizeLabelValue(value string) string {
	// Replace invalid characters with underscore
	value = regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(value, "_")
	// Trim leading/trailing underscores
	value = strings.Trim(value, "_")
	if value == "" {
		return "unknown"
	}
	return value
}

// generatePrometheusMetricName creates metric name with encoded labels for Prometheus relabeling
// Format: ai_request_<metric_name>{route="<route>",cluster="<cluster>",model="<model>",consumer="<consumer>"}
// In WASM, we encode labels in metric name using dot notation for later relabeling:
// ai.request.<route>.<cluster>.<model>.<consumer>.<metric_name>
func generatePrometheusMetricName(labels *prometheusLabels, metricName string) string {
	route := sanitizeLabelValue(labels.Route)
	cluster := sanitizeLabelValue(labels.Cluster)
	model := sanitizeLabelValue(labels.Model)
	consumer := sanitizeLabelValue(labels.Consumer)
	metric := sanitizeLabelValue(metricName)

	return fmt.Sprintf("ai.request.route.%s.cluster.%s.model.%s.consumer.%s.metric.%s",
		route, cluster, model, consumer, metric)
}

func getRouteName() string {
	if raw, err := proxywasm.GetProperty([]string{"route_name"}); err == nil && len(raw) > 0 {
		return string(raw)
	}
	return "unknown"
}

func getClusterName() string {
	if raw, err := proxywasm.GetProperty([]string{"cluster_name"}); err == nil && len(raw) > 0 {
		return string(raw)
	}
	return "unknown"
}

func getAPIName() (string, error) {
	route := getRouteName()
	parts := strings.Split(route, "@")
	if len(parts) != 5 {
		return "", errors.New("not api type")
	}
	return strings.Join(parts[:3], "@"), nil
}

func getConsumer(ctx wrapper.HttpContext) string {
	if consumer, ok := ctx.GetContext(ConsumerKey).(string); ok && consumer != "" {
		return consumer
	}
	return "none"
}

func (config *AIStatisticsConfig) incrementCounter(metricName string, inc uint64, labels *prometheusLabels) {
	if inc == 0 {
		return
	}

	fullMetricName := generatePrometheusMetricName(labels, metricName)
	counter, ok := config.counterMetrics[fullMetricName]
	if !ok {
		counter = proxywasm.DefineCounterMetric(fullMetricName)
		config.counterMetrics[fullMetricName] = counter
	}
	counter.Increment(inc)
}

func parseConfig(configJson gjson.Result, config *AIStatisticsConfig) error {
	// Parse tracing span attributes setting
	attributeConfigs := configJson.Get("attributes").Array()
	config.attributes = make([]Attribute, len(attributeConfigs))
	for i, attributeConfig := range attributeConfigs {
		attribute := Attribute{}
		if err := json.Unmarshal([]byte(attributeConfig.Raw), &attribute); err != nil {
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

	// Initialize metrics map
	config.counterMetrics = make(map[string]proxywasm.MetricCounter)

	// Parse openai usage config
	config.disableOpenaiUsage = configJson.Get("disable_openai_usage").Bool()

	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config AIStatisticsConfig) types.Action {
	ctx.DisableReroute()

	// Extract and store core labels
	route := getRouteName()
	cluster := getClusterName()
	if api, err := getAPIName(); err == nil {
		route = api
	}

	ctx.SetContext(RouteName, route)
	ctx.SetContext(ClusterName, cluster)
	ctx.SetContext(StatisticsRequestStartTime, time.Now().UnixMilli())

	// Extract consumer from header
	if consumer, _ := proxywasm.GetHttpRequestHeader(ConsumerKey); consumer != "" {
		ctx.SetContext(ConsumerKey, consumer)
	}

	if requestPath, _ := proxywasm.GetHttpRequestHeader(":path"); requestPath != "" {
		ctx.SetContext(RequestPath, requestPath)
	}

	ctx.SetRequestBodyBufferLimit(defaultMaxBodyBytes)

	// Set user defined attributes
	setAttributeBySource(ctx, config, FixedValue, nil)
	setAttributeBySource(ctx, config, RequestHeader, nil)
	setSpanAttribute(ArmsSpanKind, "LLM")

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config AIStatisticsConfig, body []byte) types.Action {
	// Set user defined attributes
	setAttributeBySource(ctx, config, RequestBody, body)

	// Extract model from request
	requestModel := "UNKNOWN"
	if model := gjson.GetBytes(body, "model"); model.Exists() {
		requestModel = model.String()
	} else {
		requestPath := ctx.GetStringContext(RequestPath, "")
		if strings.Contains(requestPath, "generateContent") || strings.Contains(requestPath, "streamGenerateContent") {
			reg := regexp.MustCompile(`^.*/(?P<api_version>[^/]+)/models/(?P<model>[^:]+):\w+Content$`)
			if matches := reg.FindStringSubmatch(requestPath); len(matches) == 3 {
				requestModel = matches[2]
			}
		}
	}
	setSpanAttribute(ArmsRequestModel, requestModel)
	ctx.SetContext(tokenusage.CtxKeyModel, requestModel)

	// Count conversation rounds
	userPromptCount := 0
	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		for _, msg := range messages.Array() {
			if msg.Get("role").String() == "user" {
				userPromptCount++
			}
		}
	} else if contents := gjson.GetBytes(body, "contents"); contents.Exists() && contents.IsArray() {
		for _, content := range contents.Array() {
			if !content.Get("role").Exists() || content.Get("role").String() == "user" {
				userPromptCount++
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

	setAttributeBySource(ctx, config, ResponseHeader, nil)
	return types.ActionContinue
}

func onHttpStreamingBody(ctx wrapper.HttpContext, config AIStatisticsConfig, data []byte, endOfStream bool) []byte {
	if config.shouldBufferStreamingBody {
		buffer, _ := ctx.GetContext(CtxStreamingBodyBuffer).([]byte)
		ctx.SetContext(CtxStreamingBodyBuffer, append(buffer, data...))
	}

	ctx.SetUserAttribute(ResponseType, "stream")

	// Extract chat ID
	if chatID := wrapper.GetValueFromBody(data, []string{
		"id", "response.id", "responseId", "message.id",
	}); chatID != nil {
		ctx.SetUserAttribute(ChatID, chatID.String())
	}

	// Record first token time
	if ctx.GetContext(StatisticsFirstTokenTime) == nil {
		if startTime, ok := ctx.GetContext(StatisticsRequestStartTime).(int64); ok {
			firstTokenTime := time.Now().UnixMilli()
			ctx.SetContext(StatisticsFirstTokenTime, firstTokenTime)
			ctx.SetUserAttribute(LLMFirstTokenDuration, firstTokenTime-startTime)
		}
	}

	// Extract token usage if available
	if !config.disableOpenaiUsage {
		if usage := tokenusage.GetTokenUsage(ctx, data); usage.TotalToken > 0 {
			setSpanAttribute(ArmsTotalToken, usage.TotalToken)
			setSpanAttribute(ArmsModelName, usage.Model)
			setSpanAttribute(ArmsInputToken, usage.InputToken)
			setSpanAttribute(ArmsOutputToken, usage.OutputToken)

			// Store for metrics
			ctx.SetContext(tokenusage.CtxKeyModel, usage.Model)
			ctx.SetContext(tokenusage.CtxKeyInputToken, usage.InputToken)
			ctx.SetContext(tokenusage.CtxKeyOutputToken, usage.OutputToken)
			ctx.SetContext(tokenusage.CtxKeyTotalToken, usage.TotalToken)
		}
	}

	if endOfStream {
		if startTime, ok := ctx.GetContext(StatisticsRequestStartTime).(int64); ok {
			ctx.SetUserAttribute(LLMServiceDuration, time.Now().UnixMilli()-startTime)
		}

		if config.shouldBufferStreamingBody {
			if buffer, ok := ctx.GetContext(CtxStreamingBodyBuffer).([]byte); ok {
				setAttributeBySource(ctx, config, ResponseStreamingBody, buffer)
			}
		}

		ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)
		writeMetric(ctx, config)
	}

	return data
}

func onHttpResponseBody(ctx wrapper.HttpContext, config AIStatisticsConfig, body []byte) types.Action {
	if startTime, ok := ctx.GetContext(StatisticsRequestStartTime).(int64); ok {
		ctx.SetUserAttribute(LLMServiceDuration, time.Now().UnixMilli()-startTime)
	}

	ctx.SetUserAttribute(ResponseType, "normal")

	// Extract chat ID
	if chatID := wrapper.GetValueFromBody(body, []string{
		"id", "response.id", "responseId", "message.id",
	}); chatID != nil {
		ctx.SetUserAttribute(ChatID, chatID.String())
	}

	// Extract token usage if available
	if !config.disableOpenaiUsage {
		if usage := tokenusage.GetTokenUsage(ctx, body); usage.TotalToken > 0 {
			setSpanAttribute(ArmsModelName, usage.Model)
			setSpanAttribute(ArmsInputToken, usage.InputToken)
			setSpanAttribute(ArmsOutputToken, usage.OutputToken)
			setSpanAttribute(ArmsTotalToken, usage.TotalToken)

			// Store for metrics
			ctx.SetContext(tokenusage.CtxKeyModel, usage.Model)
			ctx.SetContext(tokenusage.CtxKeyInputToken, usage.InputToken)
			ctx.SetContext(tokenusage.CtxKeyOutputToken, usage.OutputToken)
			ctx.SetContext(tokenusage.CtxKeyTotalToken, usage.TotalToken)
		}
	}

	setAttributeBySource(ctx, config, ResponseBody, body)
	ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)
	writeMetric(ctx, config)

	return types.ActionContinue
}

func setAttributeBySource(ctx wrapper.HttpContext, config AIStatisticsConfig, source string, body []byte) {
	for _, attribute := range config.attributes {
		if attribute.ValueSource != source {
			continue
		}

		var value interface{}
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
		}

		if (value == nil || value == "") && attribute.DefaultValue != "" {
			value = attribute.DefaultValue
		}

		log.Debugf("[attribute] source: %s, key: %s, value: %+v", source, attribute.Key, value)

		if attribute.ApplyToLog {
			if attribute.AsSeparateLogField {
				marshalled := wrapper.MarshalStr(fmt.Sprint(value))
				if err := proxywasm.SetProperty([]string{attribute.Key}, []byte(marshalled)); err != nil {
					log.Warnf("failed to set property %s: %v", attribute.Key, err)
				}
			} else {
				ctx.SetUserAttribute(attribute.Key, value)
			}
		}

		// Store token usage for metrics
		if attribute.Key == tokenusage.CtxKeyModel || attribute.Key == tokenusage.CtxKeyInputToken ||
			attribute.Key == tokenusage.CtxKeyOutputToken || attribute.Key == tokenusage.CtxKeyTotalToken {
			ctx.SetContext(attribute.Key, value)
		}

		if attribute.ApplyToSpan && value != "" {
			key := attribute.Key
			if attribute.TraceSpanKey != "" {
				key = attribute.TraceSpanKey
			}
			setSpanAttribute(key, value)
		}
	}
}

func extractStreamingBodyByJsonPath(data []byte, jsonPath string, rule string) interface{} {
	chunks := bytes.Split(bytes.TrimSpace(wrapper.UnifySSEChunk(data)), []byte("\n\n"))

	switch rule {
	case RuleFirst:
		for _, chunk := range chunks {
			if jsonObj := gjson.GetBytes(chunk, jsonPath); jsonObj.Exists() {
				return jsonObj.Value()
			}
		}
	case RuleReplace:
		var value interface{}
		for _, chunk := range chunks {
			if jsonObj := gjson.GetBytes(chunk, jsonPath); jsonObj.Exists() {
				value = jsonObj.Value()
			}
		}
		return value
	case RuleAppend:
		var parts []string
		for _, chunk := range chunks {
			if jsonObj := gjson.GetBytes(chunk, jsonPath); jsonObj.Exists() {
				parts = append(parts, jsonObj.String())
			}
		}
		return strings.Join(parts, "")
	default:
		log.Errorf("unsupported rule: %s", rule)
	}
	return nil
}

func setSpanAttribute(key string, value interface{}) {
	if value == nil || value == "" {
		log.Debugf("skip empty span attribute: %s", key)
		return
	}

	traceSpanTag := wrapper.TraceSpanTagPrefix + key
	if err := proxywasm.SetProperty([]string{traceSpanTag}, []byte(fmt.Sprint(value))); err != nil {
		log.Warnf("failed to set span attribute %s: %v", traceSpanTag, err)
	}
}

func writeMetric(ctx wrapper.HttpContext, config AIStatisticsConfig) {
	if config.disableOpenaiUsage {
		return
	}

	// Build labels struct
	labels := &prometheusLabels{
		Route:    getRouteName(),
		Cluster:  getClusterName(),
		Consumer: getConsumer(ctx),
	}

	// Get model from context
	if model, ok := ctx.GetContext(tokenusage.CtxKeyModel).(string); ok && model != "" {
		labels.Model = model
	} else {
		log.Warnf("model not found in context, using 'unknown'")
		labels.Model = "unknown"
	}

	// Record token metrics
	if inputToken, ok := convertToUInt(ctx.GetContext(tokenusage.CtxKeyInputToken)); ok {
		config.incrementCounter(tokenusage.CtxKeyInputToken, inputToken, labels)
	}
	if outputToken, ok := convertToUInt(ctx.GetContext(tokenusage.CtxKeyOutputToken)); ok {
		config.incrementCounter(tokenusage.CtxKeyOutputToken, outputToken, labels)
	}
	if totalToken, ok := convertToUInt(ctx.GetContext(tokenusage.CtxKeyTotalToken)); ok {
		config.incrementCounter(tokenusage.CtxKeyTotalToken, totalToken, labels)
	}

	// Record duration metrics
	if duration, ok := convertToUInt(ctx.GetUserAttribute(LLMFirstTokenDuration)); ok {
		config.incrementCounter(LLMFirstTokenDuration, duration, labels)
		config.incrementCounter(LLMStreamDurationCount, 1, labels)
	}
	if duration, ok := convertToUInt(ctx.GetUserAttribute(LLMServiceDuration)); ok {
		config.incrementCounter(LLMServiceDuration, duration, labels)
		config.incrementCounter(LLMDurationCount, 1, labels)
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
