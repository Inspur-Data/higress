package main

import (
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	logs "github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

func main() {}

type EmptyConfig struct {
	Message string `json:"message"`
}

func init() {
	wrapper.SetCtx(
		"empty-test",
		wrapper.ParseConfigBy(parseConfig),
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
	)
}

func parseConfig(json gjson.Result, config *EmptyConfig, log logs.Log) error {
	config.Message = json.Get("message").String()
	if config.Message == "" {
		config.Message = "Hello from empty test"
	}
	log.Infof("Empty test plugin loaded: %s", config.Message)
	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config EmptyConfig, log logs.Log) types.Action {
	log.Infof("Request received: %s", config.Message)
	
	// 添加一个测试响应头
	proxywasm.AddHttpRequestHeader("X-Test-Plugin", "loaded")
	
	// 继续处理请求
	return types.HeaderContinue
}
