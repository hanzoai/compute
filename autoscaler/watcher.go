// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package autoscaler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	labelOrg     = "hanzo.ai/org"
	labelProject = "hanzo.ai/project"
	labelPool    = "hanzo.ai/pool"

	defaultCheckInterval     = 30 * time.Second
	defaultScaleUpCooldown   = 5 * time.Minute
	defaultScaleDownCooldown = 10 * time.Minute
	defaultPendingThreshold  = 60 * time.Second

	defaultMinNodes = 2
	defaultMaxNodes = 50

	scaleDownCPUThreshold = 30 // percent
	scaleDownMemThreshold = 40 // percent
	scaleDownIdleMinutes  = 10
)

type ClusterConfig struct {
	ClusterID    string
	ProviderName string
	Owner        string
}

type PodWatcher struct {
	K8sClient   kubernetes.Interface
	DOKSClients map[string]*service.DOKSClient // clusterID → client
	Clusters    []ClusterConfig

	CheckInterval     time.Duration
	ScaleUpCooldown   time.Duration
	ScaleDownCooldown time.Duration
	MinNodes          int
	MaxNodes          int

	mu              sync.Mutex
	lastScaleUp     map[string]time.Time // poolID → last scale-up time
	lastScaleDown   map[string]time.Time // poolID → last scale-down time
	pendingPodsSeen map[string]time.Time // pod UID → first seen time
}

func NewPodWatcher(clusters []ClusterConfig) (*PodWatcher, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		logs.Warning("autoscaler: not running in-cluster, pod watcher disabled: %v", err)
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create K8s client: %w", err)
	}

	doksClients := make(map[string]*service.DOKSClient)
	for _, cluster := range clusters {
		provider, err := object.GetProvider(fmt.Sprintf("%s/%s", cluster.Owner, cluster.ProviderName))
		if err != nil || provider == nil {
			logs.Warning("autoscaler: skipping cluster %s, provider not found: %v", cluster.ClusterID, err)
			continue
		}

		token := provider.ClientSecret
		if token == "" {
			token = provider.ClientId
		}

		client, err := service.NewDOKSClient(token, cluster.ClusterID)
		if err != nil {
			logs.Warning("autoscaler: failed to create DOKS client for cluster %s: %v", cluster.ClusterID, err)
			continue
		}
		doksClients[cluster.ClusterID] = client
	}

	return &PodWatcher{
		K8sClient:         clientset,
		DOKSClients:       doksClients,
		Clusters:          clusters,
		CheckInterval:     defaultCheckInterval,
		ScaleUpCooldown:   defaultScaleUpCooldown,
		ScaleDownCooldown: defaultScaleDownCooldown,
		MinNodes:          defaultMinNodes,
		MaxNodes:          defaultMaxNodes,
		lastScaleUp:       make(map[string]time.Time),
		lastScaleDown:     make(map[string]time.Time),
		pendingPodsSeen:   make(map[string]time.Time),
	}, nil
}

// Start begins the pod watcher loop. Call this in a goroutine.
func (w *PodWatcher) Start(ctx context.Context) {
	logs.Info("autoscaler: pod watcher started with %d cluster(s)", len(w.DOKSClients))

	checkTicker := time.NewTicker(w.CheckInterval)
	defer checkTicker.Stop()

	scaleDownTicker := time.NewTicker(w.ScaleDownCooldown)
	defer scaleDownTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			logs.Info("autoscaler: pod watcher stopped")
			return
		case <-checkTicker.C:
			w.checkPendingPods()
		case <-scaleDownTicker.C:
			w.checkScaleDown()
		}
	}
}

func (w *PodWatcher) checkPendingPods() {
	pods, err := w.K8sClient.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{
		FieldSelector: "status.phase=Pending",
	})
	if err != nil {
		logs.Warning("autoscaler: failed to list pending pods: %v", err)
		return
	}

	// Track currently pending pods and clean up stale entries
	currentPending := make(map[string]bool)
	var unschedulablePods []corev1.Pod

	for _, pod := range pods.Items {
		uid := string(pod.UID)
		currentPending[uid] = true

		// Check if Unschedulable
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse &&
				condition.Reason == "Unschedulable" {

				w.mu.Lock()
				if _, exists := w.pendingPodsSeen[uid]; !exists {
					w.pendingPodsSeen[uid] = time.Now()
				}
				firstSeen := w.pendingPodsSeen[uid]
				w.mu.Unlock()

				if time.Since(firstSeen) > defaultPendingThreshold {
					unschedulablePods = append(unschedulablePods, pod)
				}
				break
			}
		}
	}

	// Clean up pods that are no longer pending
	w.mu.Lock()
	for uid := range w.pendingPodsSeen {
		if !currentPending[uid] {
			delete(w.pendingPodsSeen, uid)
		}
	}
	w.mu.Unlock()

	if len(unschedulablePods) == 0 {
		return
	}

	logs.Info("autoscaler: %d unschedulable pods detected, evaluating scale-up", len(unschedulablePods))
	w.handleScaleUp(unschedulablePods)
}

func (w *PodWatcher) handleScaleUp(pods []corev1.Pod) {
	// Calculate total required resources
	var totalCPU, totalMem int64
	orgMap := make(map[string]string)     // track org attribution
	projectMap := make(map[string]string) // track project attribution

	for _, pod := range pods {
		for _, container := range pod.Spec.Containers {
			if cpu := container.Resources.Requests.Cpu(); cpu != nil {
				totalCPU += cpu.MilliValue()
			}
			if mem := container.Resources.Requests.Memory(); mem != nil {
				totalMem += mem.Value() / (1024 * 1024) // convert to MB
			}
		}

		// Extract org/project attribution from pod labels/annotations
		if orgID, ok := pod.Labels[labelOrg]; ok {
			orgMap[orgID] = orgID
		} else if orgID, ok := pod.Annotations[labelOrg]; ok {
			orgMap[orgID] = orgID
		}
		if projID, ok := pod.Labels[labelProject]; ok {
			projectMap[projID] = projID
		} else if projID, ok := pod.Annotations[labelProject]; ok {
			projectMap[projID] = projID
		}
	}

	// Determine best-fit node size
	sizeSlug := SizeForResources(totalCPU, totalMem)
	sizeCPU := SizeCPUMillis(sizeSlug)
	if sizeCPU == 0 {
		sizeCPU = 4000 // default assumption
	}
	sizeMem := SizeMemMB(sizeSlug)
	if sizeMem == 0 {
		sizeMem = 8192
	}

	// Calculate how many additional nodes we need
	nodesForCPU := (totalCPU + sizeCPU - 1) / sizeCPU
	nodesForMem := (totalMem + sizeMem - 1) / sizeMem
	additionalNodes := nodesForCPU
	if nodesForMem > additionalNodes {
		additionalNodes = nodesForMem
	}
	if additionalNodes < 1 {
		additionalNodes = 1
	}

	// Try to scale up existing pools or create a new one for each cluster
	for clusterID, doksClient := range w.DOKSClients {
		w.mu.Lock()
		lastScale, exists := w.lastScaleUp[clusterID]
		w.mu.Unlock()

		if exists && time.Since(lastScale) < w.ScaleUpCooldown {
			logs.Info("autoscaler: cluster %s is in scale-up cooldown, skipping", clusterID)
			continue
		}

		pools, err := doksClient.ListNodePools()
		if err != nil {
			logs.Warning("autoscaler: failed to list pools for cluster %s: %v", clusterID, err)
			continue
		}

		scaled := false
		for _, pool := range pools {
			if pool.Size == sizeSlug && pool.Count < w.MaxNodes {
				newCount := pool.Count + int(additionalNodes)
				if newCount > w.MaxNodes {
					newCount = w.MaxNodes
				}

				spec := &service.CreateNodePoolSpec{
					Name:      pool.Name,
					Size:      pool.Size,
					Count:     newCount,
					MinNodes:  pool.MinNodes,
					MaxNodes:  pool.MaxNodes,
					AutoScale: pool.AutoScale,
					Tags:      pool.Tags,
					Labels:    pool.Labels,
				}

				_, err := doksClient.UpdateNodePool(pool.ID, spec)
				if err != nil {
					logs.Warning("autoscaler: failed to scale pool %s: %v", pool.Name, err)
					continue
				}

				logs.Info("autoscaler: scaled pool %s from %d to %d nodes in cluster %s",
					pool.Name, pool.Count, newCount, clusterID)

				w.mu.Lock()
				w.lastScaleUp[clusterID] = time.Now()
				w.mu.Unlock()

				scaled = true
				break
			}
		}

		if !scaled {
			// Create a new auto-scale pool
			labels := map[string]string{
				"hanzo.ai/managed": "autoscaler",
			}
			for orgID := range orgMap {
				labels[labelOrg] = orgID
				break // use first org
			}
			for projID := range projectMap {
				labels[labelProject] = projID
				break
			}

			spec := &service.CreateNodePoolSpec{
				Name:      fmt.Sprintf("auto-%s-%d", sizeSlug, time.Now().Unix()),
				Size:      sizeSlug,
				Count:     int(additionalNodes),
				MinNodes:  w.MinNodes,
				MaxNodes:  w.MaxNodes,
				AutoScale: true,
				Tags:      []string{"hanzo-autoscaler"},
				Labels:    labels,
			}

			newPool, err := doksClient.CreateNodePool(spec)
			if err != nil {
				logs.Warning("autoscaler: failed to create new pool in cluster %s: %v", clusterID, err)
				continue
			}

			logs.Info("autoscaler: created new pool %s with %d nodes (size %s) in cluster %s",
				newPool.Name, newPool.Count, sizeSlug, clusterID)

			w.mu.Lock()
			w.lastScaleUp[clusterID] = time.Now()
			w.mu.Unlock()
		}
	}
}

func (w *PodWatcher) checkScaleDown() {
	nodes, err := w.K8sClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		logs.Warning("autoscaler: failed to list nodes: %v", err)
		return
	}

	for _, node := range nodes.Items {
		// Only consider nodes managed by autoscaler
		if _, ok := node.Labels["hanzo.ai/managed"]; !ok {
			continue
		}

		// Check node utilization
		allocatable := node.Status.Allocatable
		cpuCap := allocatable.Cpu().MilliValue()
		memCap := allocatable.Memory().Value()

		// Get pods on this node
		pods, err := w.K8sClient.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{
			FieldSelector: fmt.Sprintf("spec.nodeName=%s", node.Name),
		})
		if err != nil {
			continue
		}

		var cpuUsed, memUsed int64
		hasPods := false
		for _, pod := range pods.Items {
			// Skip DaemonSet pods
			for _, ownerRef := range pod.OwnerReferences {
				if ownerRef.Kind == "DaemonSet" {
					continue
				}
			}
			hasPods = true
			for _, container := range pod.Spec.Containers {
				if cpu := container.Resources.Requests.Cpu(); cpu != nil {
					cpuUsed += cpu.MilliValue()
				}
				if mem := container.Resources.Requests.Memory(); mem != nil {
					memUsed += mem.Value()
				}
			}
		}

		if hasPods {
			continue // has non-DaemonSet pods, don't scale down
		}

		cpuPercent := int64(0)
		if cpuCap > 0 {
			cpuPercent = (cpuUsed * 100) / cpuCap
		}
		memPercent := int64(0)
		if memCap > 0 {
			memPercent = (memUsed * 100) / memCap
		}

		_ = resource.Quantity{} // ensure import is used

		if cpuPercent < scaleDownCPUThreshold && memPercent < scaleDownMemThreshold {
			logs.Info("autoscaler: node %s is underutilized (CPU: %d%%, Mem: %d%%), considering scale-down",
				node.Name, cpuPercent, memPercent)

			// Find which pool this node belongs to and scale down
			for clusterID, doksClient := range w.DOKSClients {
				w.mu.Lock()
				lastScale, exists := w.lastScaleDown[clusterID]
				w.mu.Unlock()

				if exists && time.Since(lastScale) < w.ScaleDownCooldown {
					continue
				}

				pools, err := doksClient.ListNodePools()
				if err != nil {
					continue
				}

				for _, pool := range pools {
					for _, poolNode := range pool.Nodes {
						if poolNode.Name == node.Name && pool.Count > w.MinNodes {
							newCount := pool.Count - 1
							spec := &service.CreateNodePoolSpec{
								Name:      pool.Name,
								Size:      pool.Size,
								Count:     newCount,
								MinNodes:  pool.MinNodes,
								MaxNodes:  pool.MaxNodes,
								AutoScale: pool.AutoScale,
								Tags:      pool.Tags,
								Labels:    pool.Labels,
							}

							_, err := doksClient.UpdateNodePool(pool.ID, spec)
							if err != nil {
								logs.Warning("autoscaler: failed to scale down pool %s: %v", pool.Name, err)
								continue
							}

							logs.Info("autoscaler: scaled down pool %s from %d to %d nodes in cluster %s",
								pool.Name, pool.Count, newCount, clusterID)

							w.mu.Lock()
							w.lastScaleDown[clusterID] = time.Now()
							w.mu.Unlock()
						}
					}
				}
			}
		}
	}
}
