package main

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

func main() {}

func init() {
	wrapper.SetCtx(
		"cluster-health-check",
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		wrapper.ProcessStreamingResponseBody(onHttpStreamingResponseBody),
		wrapper.ProcessStreamDone(onHttpStreamDone),
	)
}

// HealthCheckConfig 健康检查配置
type HealthCheckConfig struct {
	Enabled                    bool
	ConsecutiveFailuresThreshold int
}

// ClusterHealthChecker 集群健康检查器
type ClusterHealthChecker struct {
	ServiceList   []string
	
	// 健康检查配置
	HealthCheck HealthCheckConfig
	
	// 健康状态追踪
	ConsecutiveFailures map[string]int  // 连续失败次数
	IsServiceHealthy    map[string]bool // 服务健康状态
	
	// 可用服务列表（用于负载均衡）
	AvailableServices []string
}

// parseConfig 解析配置
func parseConfig(json gjson.Result, config *ClusterHealthChecker) error {
	config.ConsecutiveFailures = make(map[string]int)
	config.IsServiceHealthy = make(map[string]bool)
	
	// 解析健康检查配置
	config.HealthCheck.Enabled = true
	if json.Get("health_check.enabled").Exists() {
		config.HealthCheck.Enabled = json.Get("health_check.enabled").Bool()
	}
	
	config.HealthCheck.ConsecutiveFailuresThreshold = int(json.Get("health_check.consecutive_failures_threshold").Int())
	if config.HealthCheck.ConsecutiveFailuresThreshold == 0 {
		config.HealthCheck.ConsecutiveFailuresThreshold = 3 // 默认连续3次失败标记为不健康
	}
	
	// 解析服务列表
	serviceList := json.Get("service_list")
	if !serviceList.Exists() || !serviceList.IsArray() {
		return fmt.Errorf("service_list is required and must be an array")
	}
	
	for _, svc := range serviceList.Array() {
		serviceName := svc.String()
		config.ServiceList = append(config.ServiceList, serviceName)
		config.ConsecutiveFailures[serviceName] = 0
		config.IsServiceHealthy[serviceName] = true // 初始认为健康
		config.AvailableServices = append(config.AvailableServices, serviceName)
	}
	
	if config.HealthCheck.Enabled {
		log.Infof("Cluster health check enabled, services: %v, consecutive failures threshold: %d",
			config.ServiceList, config.HealthCheck.ConsecutiveFailuresThreshold)
	}
	
	return nil
}

// updateHealthStatus 更新服务健康状态
func (hc *ClusterHealthChecker) updateHealthStatus(serviceName string, isSuccess bool) {
	if !hc.HealthCheck.Enabled {
		return
	}
	
	if isSuccess {
		// 成功则重置失败计数
		hc.ConsecutiveFailures[serviceName] = 0
		if !hc.IsServiceHealthy[serviceName] {
			log.Infof("Service %s recovered and marked as healthy", serviceName)
		}
		hc.IsServiceHealthy[serviceName] = true
		
		// 重新构建可用服务列表
		hc.rebuildAvailableServices()
	} else {
		// 失败则累加计数
		hc.ConsecutiveFailures[serviceName]++
		
		// 超过阈值标记为不健康
		if hc.ConsecutiveFailures[serviceName] >= hc.HealthCheck.ConsecutiveFailuresThreshold {
			if hc.IsServiceHealthy[serviceName] {
				log.Warnf("Service %s marked as unhealthy after %d consecutive failures",
					serviceName, hc.ConsecutiveFailures[serviceName])
			}
			hc.IsServiceHealthy[serviceName] = false
			
			// 从可用服务列表中移除
			hc.removeFromAvailableServices(serviceName)
		}
	}
}

// rebuildAvailableServices 重新构建可用服务列表
func (hc *ClusterHealthChecker) rebuildAvailableServices() {
	hc.AvailableServices = []string{}
	for _, svc := range hc.ServiceList {
		if hc.IsServiceHealthy[svc] {
			hc.AvailableServices = append(hc.AvailableServices, svc)
		}
	}
	
	// 如果所有服务都不健康，使用全部服务（降级）
	if len(hc.AvailableServices) == 0 {
		log.Warn("All services are unhealthy, using all services as fallback")
		hc.AvailableServices = hc.ServiceList
	}
}

// removeFromAvailableServices 从可用服务列表中移除指定服务
func (hc *ClusterHealthChecker) removeFromAvailableServices(serviceName string) {
	newList := []string{}
	for _, svc := range hc.AvailableServices {
		if svc != serviceName {
			newList = append(newList, svc)
		}
	}
	hc.AvailableServices = newList
	
	log.Debugf("Service %s removed from available services, remaining: %v", 
		serviceName, hc.AvailableServices)
}

// getRandomAvailableService 从可用服务列表中随机选择一个
func (hc *ClusterHealthChecker) getRandomAvailableService() string {
	if len(hc.AvailableServices) == 0 {
		// 降级：使用所有服务
		if len(hc.ServiceList) > 0 {
			return hc.ServiceList[rand.Intn(len(hc.ServiceList))]
		}
		return ""
	}
	
	return hc.AvailableServices[rand.Intn(len(hc.AvailableServices))]
}

// getHealthyServices 获取健康的服务列表（保留用于兼容）
func (hc *ClusterHealthChecker) getHealthyServices() []string {
	if !hc.HealthCheck.Enabled {
		return hc.ServiceList
	}
	
	return hc.AvailableServices
}

// onHttpRequestHeaders 请求头处理阶段
func onHttpRequestHeaders(ctx wrapper.HttpContext, config ClusterHealthChecker) types.Action {
	// ⭐ 从可用服务列表中随机选择一个健康的服务
	selectedService := config.getRandomAvailableService()
	
	if selectedService == "" {
		log.Error("No available services")
		return types.ActionContinue
	}
	
	// 设置目标服务Header（供后续路由使用）
	proxywasm.ReplaceHttpRequestHeader("x-higress-target-cluster", selectedService)
	ctx.SetContext("selected_service", selectedService)
	
	// 同时设置健康服务列表Header（供其他插件参考）
	healthyServicesStr := ""
	for i, svc := range config.AvailableServices {
		if i > 0 {
			healthyServicesStr += ","
		}
		healthyServicesStr += svc
	}
	proxywasm.ReplaceHttpRequestHeader("x-cluster-healthy-services", healthyServicesStr)
	ctx.SetContext("healthy_services", config.AvailableServices)
	
	log.Debugf("Selected service: %s, Available services: %v", 
		selectedService, config.AvailableServices)
	
	return types.ActionContinue
}

// onHttpResponseHeaders 响应头处理阶段
func onHttpResponseHeaders(ctx wrapper.HttpContext, config ClusterHealthChecker) types.Action {
	statusCode, _ := proxywasm.GetHttpResponseHeader(":status")
	ctx.SetContext("statusCode", statusCode)
	
	// ⭐ 根据实际请求的目标服务更新健康状态
	targetCluster, _ := ctx.GetContext("selected_service").(string)
	if targetCluster == "" {
		// 备选：从Header获取
		targetCluster, _ = proxywasm.GetHttpRequestHeader("x-higress-target-cluster")
	}
	
	if targetCluster != "" {
		isSuccess := statusCode == "200"
		
		// 更新健康状态（会自动维护AvailableServices列表）
		config.updateHealthStatus(targetCluster, isSuccess)
		
		log.Debugf("Health status updated for %s: success=%v, status=%s",
			targetCluster, isSuccess, statusCode)
	}
	
	return types.ActionContinue
}

// onHttpStreamingResponseBody 流式响应体处理阶段
func onHttpStreamingResponseBody(ctx wrapper.HttpContext, config ClusterHealthChecker, data []byte, endOfStream bool) []byte {
	// 流式响应不需要再次更新健康状态，已在响应头阶段处理
	// 这里可以添加流式数据的监控逻辑（可选）
	return data
}

// onHttpStreamDone 流结束阶段
func onHttpStreamDone(ctx wrapper.HttpContext, config ClusterHealthChecker) {
	// 输出健康状态摘要
	if log.GetLogLevel() <= log.DebugLevel {
		healthyCount := 0
		unhealthyCount := 0
		
		for _, svc := range config.ServiceList {
			if config.IsServiceHealthy[svc] {
				healthyCount++
			} else {
				unhealthyCount++
			}
		}
		
		log.Debugf("Health check summary - Healthy: %d, Unhealthy: %d, Total: %d",
			healthyCount, unhealthyCount, len(config.ServiceList))
	}
}
