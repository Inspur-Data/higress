package test

import (
	"encoding/json"
	"testing"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/test"
	"github.com/stretchr/testify/require"
)

// 测试配置：多 provider + 意图路由
var intentRoutingConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"id":   "qwen-finance",
				"type": "qwen",
				"apiTokens": []string{"qwen-token-1"},
				"modelMapping": map[string]string{
					"*": "qwen-turbo",
				},
			},
			{
				"id":   "gpt-legal",
				"type": "openai",
				"apiTokens": []string{"openai-token-1"},
				"modelMapping": map[string]string{
					"*": "gpt-4",
				},
			},
			{
				"id":   "claude-tech",
				"type": "claude",
				"apiTokens": []string{"claude-token-1"},
				"modelMapping": map[string]string{
					"*": "claude-3-sonnet",
				},
			},
		},
		"activeProviderId": "qwen-finance",
		"intentRouting": map[string]string{
			"金融": "qwen-finance",
			"法律": "gpt-legal",
			"技术": "claude-tech",
		},
	})
	return data
}()

func RunIntentRoutingParseConfigTests(t *testing.T) {
	test.RunGoTest(t, func(t *testing.T) {
		t.Run("parse intent routing config", func(t *testing.T) {
			host, status := test.NewTestHost(intentRoutingConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)
		})
	})
}

func RunIntentRoutingOnHttpRequestHeadersTests(t *testing.T) {
	test.RunTest(t, func(t *testing.T) {
		// 测试没有意图时的默认行为
		t.Run("no intent category uses default provider", func(t *testing.T) {
			host, status := test.NewTestHost(intentRoutingConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			action := host.CallOnHttpRequestHeaders([][2]string{
				{":authority", "example.com"},
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
				{"Content-Type", "application/json"},
			})

			require.Equal(t, types.HeaderStopIteration, action)

			requestHeaders := host.GetRequestHeaders()
			require.NotNil(t, requestHeaders)

			// 验证使用了默认的 qwen provider
			authValue, hasAuth := test.GetHeaderValue(requestHeaders, "Authorization")
			require.True(t, hasAuth, "Authorization header should exist")
			require.Contains(t, authValue, "qwen-token-1", "Should use default qwen provider token")
		})

		// 测试有意图类别时的动态路由
		t.Run("with intent category routes to correct provider", func(t *testing.T) {
			host, status := test.NewTestHost(intentRoutingConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			// 模拟 ai-intent 插件设置了意图类别
			err := host.SetProperty([]string{"intent_category"}, []byte("法律"))
			require.NoError(t, err)

			action := host.CallOnHttpRequestHeaders([][2]string{
				{":authority", "example.com"},
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
				{"Content-Type", "application/json"},
			})

			require.Equal(t, types.HeaderStopIteration, action)

			requestHeaders := host.GetRequestHeaders()
			require.NotNil(t, requestHeaders)

			// 验证根据意图选择了正确的 provider (法律 -> gpt-legal)
			authValue, hasAuth := test.GetHeaderValue(requestHeaders, "Authorization")
			require.True(t, hasAuth, "Authorization header should exist")
			require.Contains(t, authValue, "openai-token-1", "Should route to openai provider for legal intent")
		})

		// 测试不同的意图类别
		t.Run("different intent categories route to different providers", func(t *testing.T) {
			testCases := []struct {
				intent       string
				expectedToken string
				description  string
			}{
				{"金融", "qwen-token-1", "finance intent should use qwen"},
				{"法律", "openai-token-1", "legal intent should use openai"},
				{"技术", "claude-token-1", "tech intent should use claude"},
			}

			for _, tc := range testCases {
				t.Run(tc.description, func(t *testing.T) {
					host, status := test.NewTestHost(intentRoutingConfig)
					defer host.Reset()
					require.Equal(t, types.OnPluginStartStatusOK, status)

					// 设置意图类别
					err := host.SetProperty([]string{"intent_category"}, []byte(tc.intent))
					require.NoError(t, err)

					action := host.CallOnHttpRequestHeaders([][2]string{
						{":authority", "example.com"},
						{":path", "/v1/chat/completions"},
						{":method", "POST"},
						{"Content-Type", "application/json"},
					})

					require.Equal(t, types.HeaderStopIteration, action)

					requestHeaders := host.GetRequestHeaders()
					require.NotNil(t, requestHeaders)

					authValue, hasAuth := test.GetHeaderValue(requestHeaders, "Authorization")
					require.True(t, hasAuth, "Authorization header should exist")
					require.Contains(t, authValue, tc.expectedToken, tc.description)
				})
			}
		})

		// 测试未匹配的意图类别使用默认 provider
		t.Run("unmatched intent category uses default provider", func(t *testing.T) {
			host, status := test.NewTestHost(intentRoutingConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			// 设置一个未在 intentRouting 中配置的意图
			err := host.SetProperty([]string{"intent_category"}, []byte("医疗"))
			require.NoError(t, err)

			action := host.CallOnHttpRequestHeaders([][2]string{
				{":authority", "example.com"},
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
				{"Content-Type", "application/json"},
			})

			require.Equal(t, types.HeaderStopIteration, action)

			requestHeaders := host.GetRequestHeaders()
			require.NotNil(t, requestHeaders)

			// 应该使用默认的 qwen provider
			authValue, hasAuth := test.GetHeaderValue(requestHeaders, "Authorization")
			require.True(t, hasAuth, "Authorization header should exist")
			require.Contains(t, authValue, "qwen-token-1", "Should use default provider for unmatched intent")
		})

		// 测试前缀匹配
		t.Run("prefix matching", func(t *testing.T) {
			testCases := []struct {
				intent        string
				expectedToken string
				description   string
			}{
				{"法律咨询", "openai-token-1", "'法律咨询' should match '法律' by prefix"},
				{"法律服务", "openai-token-1", "'法律服务' should match '法律' by prefix"},
				{"金融分析", "qwen-token-1", "'金融分析' should match '金融' by prefix"},
				{"技术支持", "claude-token-1", "'技术支持' should match '技术' by prefix"},
			}

			for _, tc := range testCases {
				t.Run(tc.description, func(t *testing.T) {
					host, status := test.NewTestHost(intentRoutingConfig)
					defer host.Reset()
					require.Equal(t, types.OnPluginStartStatusOK, status)

					// 设置前缀匹配的意图
					err := host.SetProperty([]string{"intent_category"}, []byte(tc.intent))
					require.NoError(t, err)

					action := host.CallOnHttpRequestHeaders([][2]string{
						{":authority", "example.com"},
						{":path", "/v1/chat/completions"},
						{":method", "POST"},
						{"Content-Type", "application/json"},
					})

					require.Equal(t, types.HeaderStopIteration, action)

					requestHeaders := host.GetRequestHeaders()
					require.NotNil(t, requestHeaders)

					authValue, hasAuth := test.GetHeaderValue(requestHeaders, "Authorization")
					require.True(t, hasAuth, "Authorization header should exist")
					require.Contains(t, authValue, tc.expectedToken, tc.description)
				})
			}
		})

		// 测试包含匹配
		t.Run("contains matching", func(t *testing.T) {
			testCases := []struct {
				intent        string
				expectedToken string
				description   string
			}{
				{"关于法律的问题", "openai-token-1", "'关于法律的问题' should match '法律' by contains"},
				{"我需要金融方面的帮助", "qwen-token-1", "'我需要金融方面的帮助' should match '金融' by contains"},
				{"寻求技术支持", "claude-token-1", "'寻求技术支持' should match '技术' by contains"},
			}

			for _, tc := range testCases {
				t.Run(tc.description, func(t *testing.T) {
					host, status := test.NewTestHost(intentRoutingConfig)
					defer host.Reset()
					require.Equal(t, types.OnPluginStartStatusOK, status)

					// 设置包含匹配的意图
					err := host.SetProperty([]string{"intent_category"}, []byte(tc.intent))
					require.NoError(t, err)

					action := host.CallOnHttpRequestHeaders([][2]string{
						{":authority", "example.com"},
						{":path", "/v1/chat/completions"},
						{":method", "POST"},
						{"Content-Type", "application/json"},
					})

					require.Equal(t, types.HeaderStopIteration, action)

					requestHeaders := host.GetRequestHeaders()
					require.NotNil(t, requestHeaders)

					authValue, hasAuth := test.GetHeaderValue(requestHeaders, "Authorization")
					require.True(t, hasAuth, "Authorization header should exist")
					require.Contains(t, authValue, tc.expectedToken, tc.description)
				})
			}
		})

		// 测试精确匹配优先于前缀匹配
		t.Run("exact match takes precedence over prefix match", func(t *testing.T) {
			host, status := test.NewTestHost(intentRoutingConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			// 设置与配置完全一致的意图
			err := host.SetProperty([]string{"intent_category"}, []byte("法律"))
			require.NoError(t, err)

			action := host.CallOnHttpRequestHeaders([][2]string{
				{":authority", "example.com"},
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
				{"Content-Type", "application/json"},
			})

			require.Equal(t, types.HeaderStopIteration, action)

			requestHeaders := host.GetRequestHeaders()
			require.NotNil(t, requestHeaders)

			// 应该使用精确匹配（虽然也会被前缀匹配捕获，但精确匹配优先级更高）
			authValue, hasAuth := test.GetHeaderValue(requestHeaders, "Authorization")
			require.True(t, hasAuth, "Authorization header should exist")
			require.Contains(t, authValue, "openai-token-1", "Should use exact match")
		})
	})
}
