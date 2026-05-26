package config

import (
	"fmt"
	"strings"

	"github.com/alibaba/higress/plugins/wasm-go/extensions/ai-proxy/provider"
	"github.com/tidwall/gjson"
)

// @Name ai-proxy
// @Category custom
// @Phase UNSPECIFIED_PHASE
// @Priority 0
// @Title zh-CN AI代理
// @Description zh-CN 通过AI助手提供智能对话服务，支持基于意图的智能路由
// @IconUrl https://img.alicdn.com/imgextra/i1/O1CN018iKKih1iVx287RltL_!!6000000004419-2-tps-42-42.png
// @Version 0.1.0
//
// @Contact.name CH3CHO
// @Contact.url https://github.com/CH3CHO
// @Contact.email ch3cho@qq.com
//
// @Example
// { "provider": { "type": "qwen", "apiToken": "YOUR_DASHSCOPE_API_TOKEN", "modelMapping": { "*": "qwen-turbo" } } }
// @End
type PluginConfig struct {
	// @Title zh-CN AI服务提供商配置
	// @Description zh-CN AI服务提供商配置，包含API接口、模型和知识库文件等信息
	providerConfigs []provider.ProviderConfig `required:"true" yaml:"providers"`

	// @Title zh-CN 意图路由配置
	// @Description zh-CN 根据意图类别动态选择对应的Provider，实现智能路由。key为意图类别，value为provider的id
	intentRouting map[string]string `required:"false" yaml:"intentRouting" json:"intentRouting"`

	activeProviderConfig *provider.ProviderConfig `yaml:"-"`
	activeProvider       provider.Provider        `yaml:"-"`
}

func (c *PluginConfig) FromJson(json gjson.Result) {
	if providersJson := json.Get("providers"); providersJson.Exists() && providersJson.IsArray() {
		c.providerConfigs = make([]provider.ProviderConfig, 0)
		for _, providerJson := range providersJson.Array() {
			providerConfig := provider.ProviderConfig{}
			providerConfig.FromJson(providerJson)
			c.providerConfigs = append(c.providerConfigs, providerConfig)
		}
	}

	if providerJson := json.Get("provider"); providerJson.Exists() && providerJson.IsObject() {
		// TODO: For legacy config support. To be removed later.
		providerConfig := provider.ProviderConfig{}
		providerConfig.FromJson(providerJson)
		c.providerConfigs = []provider.ProviderConfig{providerConfig}
		c.activeProviderConfig = &providerConfig
		// Legacy configuration is used and the active provider is determined.
		// We don't need to continue with the new configuration style.
		return
	}

	c.activeProviderConfig = nil

	// 解析意图路由配置
	c.intentRouting = make(map[string]string)
	if intentRoutingJson := json.Get("intentRouting"); intentRoutingJson.Exists() && intentRoutingJson.IsObject() {
		for k, v := range intentRoutingJson.Map() {
			c.intentRouting[k] = v.String()
		}
	}

	activeProviderId := json.Get("activeProviderId").String()
	if activeProviderId != "" {
		for _, providerConfig := range c.providerConfigs {
			if providerConfig.GetId() == activeProviderId {
				c.activeProviderConfig = &providerConfig
				break
			}
		}
	}
}

func (c *PluginConfig) Validate() error {
	if c.activeProviderConfig == nil {
		return nil
	}
	if err := c.activeProviderConfig.Validate(); err != nil {
		return err
	}
	return nil
}

func (c *PluginConfig) Complete() error {
	if c.activeProviderConfig == nil {
		c.activeProvider = nil
		return nil
	}

	var err error

	c.activeProvider, err = provider.CreateProvider(*c.activeProviderConfig)
	if err != nil {
		return err
	}

	providerConfig := c.GetProviderConfig()
	return providerConfig.SetApiTokensFailover(c.activeProvider)
}

func (c *PluginConfig) GetProvider() provider.Provider {
	return c.activeProvider
}

func (c *PluginConfig) GetProviderConfig() *provider.ProviderConfig {
	return c.activeProviderConfig
}

// GetIntentRouting 获取意图路由配置
func (c *PluginConfig) GetIntentRouting() map[string]string {
	return c.intentRouting
}

// GetProviderById 根据provider id获取对应的Provider实例
func (c *PluginConfig) GetProviderById(providerId string) (provider.Provider, error) {
	for _, providerConfig := range c.providerConfigs {
		if providerConfig.GetId() == providerId {
			return provider.CreateProvider(providerConfig)
		}
	}
	return nil, fmt.Errorf("provider with id '%s' not found", providerId)
}

// SelectProviderByIntent 根据意图类别选择对应的Provider（支持前缀和包含匹配）
func (c *PluginConfig) SelectProviderByIntent(intentCategory string) (provider.Provider, *provider.ProviderConfig, error) {
	if intentCategory == "" || len(c.intentRouting) == 0 {
		return c.activeProvider, c.activeProviderConfig, nil
	}

	// 1. 精确匹配（优先级最高）
	if providerId, ok := c.intentRouting[intentCategory]; ok {
		for i := range c.providerConfigs {
			if c.providerConfigs[i].GetId() == providerId {
				p, err := provider.CreateProvider(c.providerConfigs[i])
				if err != nil {
					return nil, nil, err
				}
				return p, &c.providerConfigs[i], nil
			}
		}
	}

	// 2. 前缀匹配（次优先）
	// 例如：intentCategory="法律咨询" 可以匹配 routeKey="法律"
	for routeKey, providerId := range c.intentRouting {
		if strings.HasPrefix(intentCategory, routeKey) {
			for i := range c.providerConfigs {
				if c.providerConfigs[i].GetId() == providerId {
					p, err := provider.CreateProvider(c.providerConfigs[i])
					if err != nil {
						return nil, nil, err
					}
					return p, &c.providerConfigs[i], nil
				}
			}
		}
	}

	// 3. 包含匹配（最后尝试）
	// 例如：intentCategory="关于法律的问题" 可以匹配 routeKey="法律"
	for routeKey, providerId := range c.intentRouting {
		if strings.Contains(intentCategory, routeKey) {
			for i := range c.providerConfigs {
				if c.providerConfigs[i].GetId() == providerId {
					p, err := provider.CreateProvider(c.providerConfigs[i])
					if err != nil {
						return nil, nil, err
					}
					return p, &c.providerConfigs[i], nil
				}
			}
		}
	}

	// 4. 如果没有匹配到，返回默认的activeProvider
	return c.activeProvider, c.activeProviderConfig, nil
}

// SetActiveProviderForTest replaces the runtime Provider after Complete(); intended for unit tests in package main only.
func (c *PluginConfig) SetActiveProviderForTest(p provider.Provider) {
	c.activeProvider = p
}
