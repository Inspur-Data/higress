package main

import (
	"fmt"
	"math/rand"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

func main() {}

// 全局实例：所有请求共享同一个健康检查器实例
var globalHealthChecker *ClusterHealthChecker

func init() {
	globalHealthChecker = &ClusterHealthChecker{}
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
	Enabled                      bool
	ConsecutiveFailuresThreshold int
	ProbeProbability             float64 // 探针概率：不健康服务被选中的概率（0.0~1.0），默认0.1
}

// ClusterHealthChecker 集群健康检查器
type ClusterHealthChecker struct {
	ServiceList []string

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
	// 注意：config 参数是框架传入的副本，不能跨请求持久化状态
	// 因此所有状态读写都通过全局变量 globalHealthChecker
	globalHealthChecker.ConsecutiveFailures = make(map[string]int)
	globalHealthChecker.IsServiceHealthy = make(map[string]bool)
	globalHealthChecker.AvailableServices = []string{}
	globalHealthChecker.ServiceList = []string{}

	// 解析健康检查配置
	globalHealthChecker.HealthCheck.Enabled = true
	if json.Get("health_check.enabled").Exists() {
		globalHealthChecker.HealthCheck.Enabled = json.Get("health_check.enabled").Bool()
	}

	globalHealthChecker.HealthCheck.ConsecutiveFailuresThreshold = int(json.Get("health_check.consecutive_failures_threshold").Int())
	if globalHealthChecker.HealthCheck.ConsecutiveFailuresThreshold == 0 {
		globalHealthChecker.HealthCheck.ConsecutiveFailuresThreshold = 3 // 默认连续3次失败标记为不健康
	}

	globalHealthChecker.HealthCheck.ProbeProbability = float64(json.Get("health_check.probe_probability").Float())
	if globalHealthChecker.HealthCheck.ProbeProbability == 0 {
		globalHealthChecker.HealthCheck.ProbeProbability = 0.1 // 默认10%概率探测不健康服务
	} else if globalHealthChecker.HealthCheck.ProbeProbability > 1.0 {
		globalHealthChecker.HealthCheck.ProbeProbability = 1.0
	} else if globalHealthChecker.HealthCheck.ProbeProbability < 0 {
		globalHealthChecker.HealthCheck.ProbeProbability = 0
	}

	// 解析服务列表
	serviceList := json.Get("service_list")
	if !serviceList.Exists() || !serviceList.IsArray() {
		return fmt.Errorf("service_list is required and must be an array")
	}

	for _, svc := range serviceList.Array() {
		serviceName := svc.String()
		globalHealthChecker.ServiceList = append(globalHealthChecker.ServiceList, serviceName)
		globalHealthChecker.ConsecutiveFailures[serviceName] = 0
		globalHealthChecker.IsServiceHealthy[serviceName] = true // 初始认为健康
		globalHealthChecker.AvailableServices = append(globalHealthChecker.AvailableServices, serviceName)
	}

	if globalHealthChecker.HealthCheck.Enabled {
		log.Infof("Cluster health check enabled, services: %v, consecutive failures threshold: %d, probe probability: %.2f",
			globalHealthChecker.ServiceList, globalHealthChecker.HealthCheck.ConsecutiveFailuresThreshold, globalHealthChecker.HealthCheck.ProbeProbability)
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

	log.Warnf("Service %s removed from available services, remaining: %v",
		serviceName, hc.AvailableServices)
}

// getRandomAvailableService 从可用服务列表中随机选择一个
// 如果存在不健康服务，会以 probeProbability 的概率选择一个不健康服务做探针探测
func (hc *ClusterHealthChecker) getRandomAvailableService() string {
	// 探针机制：以一定概率从不健康服务中选一个做探测，给恢复的服务重新加入的机会
	if hc.HealthCheck.Enabled && hc.HealthCheck.ProbeProbability > 0 {
		// 收集所有不健康服务
		unhealthyServices := []string{}
		for _, svc := range hc.ServiceList {
			if !hc.IsServiceHealthy[svc] {
				unhealthyServices = append(unhealthyServices, svc)
			}
		}

		// 如果有不健康服务，以 probeProbability 概率选一个做探针
		if len(unhealthyServices) > 0 && rand.Float64() < hc.HealthCheck.ProbeProbability {
			probeTarget := unhealthyServices[rand.Intn(len(unhealthyServices))]
			log.Warnf("Probe: selected unhealthy service %s for health check probe", probeTarget)
			return probeTarget
		}
	}

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
	selectedService := globalHealthChecker.getRandomAvailableService()

	if selectedService == "" {
		log.Error("No available services")
		return types.ActionContinue
	}

	// 设置目标服务Header（供后续路由使用）
	proxywasm.ReplaceHttpRequestHeader("x-higress-target-cluster", selectedService)
	ctx.SetContext("selected_service", selectedService)

	// 同时设置健康服务列表Header（供其他插件参考）
	healthyServicesStr := ""
	for i, svc := range globalHealthChecker.AvailableServices {
		if i > 0 {
			healthyServicesStr += ","
		}
		healthyServicesStr += svc
	}
	log.Warnf("Available services: %v", globalHealthChecker.AvailableServices)
	proxywasm.ReplaceHttpRequestHeader("x-cluster-healthy-services", healthyServicesStr)
	ctx.SetContext("healthy_services", globalHealthChecker.AvailableServices)

	log.Warnf("Selected service: %s, Available services: %v",
		selectedService, globalHealthChecker.AvailableServices)

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
		globalHealthChecker.updateHealthStatus(targetCluster, isSuccess)

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
	// 输出健康状态摘要（调试用）
	healthyCount := 0
	unhealthyCount := 0

	for _, svc := range globalHealthChecker.ServiceList {
		if globalHealthChecker.IsServiceHealthy[svc] {
			healthyCount++
		} else {
			unhealthyCount++
		}
	}

	log.Debugf("Health check summary - Healthy: %d, Unhealthy: %d, Total: %d",
		healthyCount, unhealthyCount, len(globalHealthChecker.ServiceList))
}
