// Package k8s provides Kubernetes client helpers for node and pod operations.
// Optimized for edge environments with support for autonomous operation during partitions.
package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps the Kubernetes clientset with edge-specific operations.
type Client struct {
	clientset *kubernetes.Clientset
	nodeName  string
}

// NewClient creates a new Kubernetes client.
// It tries in-cluster config first, then falls back to kubeconfig.
func NewClient(nodeName string) (*Client, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig
		config, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		if err != nil {
			return nil, fmt.Errorf("failed to build config: %w", err)
		}
	}

	// Reduce timeout for edge environments
	config.Timeout = 10 * time.Second

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	return &Client{
		clientset: clientset,
		nodeName:  nodeName,
	}, nil
}

// NewClientWithConfig creates a client with explicit config (for testing).
func NewClientWithConfig(nodeName string, config *rest.Config) (*Client, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Client{
		clientset: clientset,
		nodeName:  nodeName,
	}, nil
}

// IsControlPlaneReachable checks if the Kubernetes API server is reachable.
func (c *Client) IsControlPlaneReachable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := c.clientset.CoreV1().Nodes().Get(ctx, c.nodeName, metav1.GetOptions{})
	return err == nil
}

// GetNode returns the node object for this node.
func (c *Client) GetNode(ctx context.Context) (*corev1.Node, error) {
	return c.clientset.CoreV1().Nodes().Get(ctx, c.nodeName, metav1.GetOptions{})
}

// CordonNode marks the node as unschedulable.
func (c *Client) CordonNode(ctx context.Context) error {
	node, err := c.GetNode(ctx)
	if err != nil {
		return err
	}

	if node.Spec.Unschedulable {
		return nil // Already cordoned
	}

	node.Spec.Unschedulable = true
	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// UncordonNode marks the node as schedulable.
func (c *Client) UncordonNode(ctx context.Context) error {
	node, err := c.GetNode(ctx)
	if err != nil {
		return err
	}

	if !node.Spec.Unschedulable {
		return nil // Already uncordoned
	}

	node.Spec.Unschedulable = false
	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// TaintNode adds a taint to the node.
func (c *Client) TaintNode(ctx context.Context, key, value string, effect corev1.TaintEffect) error {
	node, err := c.GetNode(ctx)
	if err != nil {
		return err
	}

	// Check if taint already exists
	for _, t := range node.Spec.Taints {
		if t.Key == key {
			return nil // Taint already exists
		}
	}

	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    key,
		Value:  value,
		Effect: effect,
	})

	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// RemoveTaint removes a taint from the node.
func (c *Client) RemoveTaint(ctx context.Context, key string) error {
	node, err := c.GetNode(ctx)
	if err != nil {
		return err
	}

	var newTaints []corev1.Taint
	for _, t := range node.Spec.Taints {
		if t.Key != key {
			newTaints = append(newTaints, t)
		}
	}

	if len(newTaints) == len(node.Spec.Taints) {
		return nil // Taint not found
	}

	node.Spec.Taints = newTaints
	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// ListPodsOnNode returns all pods running on this node.
func (c *Client) ListPodsOnNode(ctx context.Context) (*corev1.PodList, error) {
	return c.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{
			"spec.nodeName": c.nodeName,
		}).String(),
	})
}

// ListEvictablePods returns pods that can be evicted (not DaemonSet, not mirror pods).
func (c *Client) ListEvictablePods(ctx context.Context) ([]corev1.Pod, error) {
	pods, err := c.ListPodsOnNode(ctx)
	if err != nil {
		return nil, err
	}

	var evictable []corev1.Pod
	for _, pod := range pods.Items {
		if isEvictable(&pod) {
			evictable = append(evictable, pod)
		}
	}
	return evictable, nil
}

// EvictPod evicts a pod using the Eviction API.
func (c *Client) EvictPod(ctx context.Context, pod *corev1.Pod, gracePeriod int64) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: &gracePeriod,
		},
	}

	return c.clientset.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction)
}

// DrainNode cordons the node and evicts all evictable pods.
func (c *Client) DrainNode(ctx context.Context, gracePeriod int64) (*DrainResult, error) {
	result := &DrainResult{
		NodeName: c.nodeName,
		Started:  time.Now(),
	}

	// Cordon first
	if err := c.CordonNode(ctx); err != nil {
		return result, fmt.Errorf("failed to cordon: %w", err)
	}
	result.Cordoned = true

	// Get evictable pods
	pods, err := c.ListEvictablePods(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to list pods: %w", err)
	}
	result.TotalPods = len(pods)

	// Evict each pod
	for _, pod := range pods {
		if err := c.EvictPod(ctx, &pod, gracePeriod); err != nil {
			result.FailedEvictions = append(result.FailedEvictions, PodEvictionError{
				Pod:   pod.Namespace + "/" + pod.Name,
				Error: err.Error(),
			})
		} else {
			result.EvictedPods = append(result.EvictedPods, pod.Namespace+"/"+pod.Name)
		}
	}

	result.Completed = time.Now()
	result.Success = len(result.FailedEvictions) == 0
	return result, nil
}

// DrainResult contains the result of a drain operation.
type DrainResult struct {
	NodeName        string             `json:"node_name"`
	Started         time.Time          `json:"started"`
	Completed       time.Time          `json:"completed"`
	Cordoned        bool               `json:"cordoned"`
	TotalPods       int                `json:"total_pods"`
	EvictedPods     []string           `json:"evicted_pods"`
	FailedEvictions []PodEvictionError `json:"failed_evictions,omitempty"`
	Success         bool               `json:"success"`
}

// PodEvictionError records a failed pod eviction.
type PodEvictionError struct {
	Pod   string `json:"pod"`
	Error string `json:"error"`
}

// isEvictable returns true if the pod can be evicted.
func isEvictable(pod *corev1.Pod) bool {
	// Skip mirror pods (managed by kubelet directly)
	if _, ok := pod.Annotations[corev1.MirrorPodAnnotationKey]; ok {
		return false
	}

	// Skip DaemonSet pods
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return false
		}
	}

	// Skip pods that are already terminating
	if pod.DeletionTimestamp != nil {
		return false
	}

	// Skip pods in terminal states
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}

	return true
}

// PodPriority represents the priority of a pod for eviction ordering.
type PodPriority int

const (
	PriorityLow    PodPriority = iota // BestEffort QoS
	PriorityMedium                    // Burstable QoS
	PriorityHigh                      // Guaranteed QoS
)

// GetPodPriority returns the eviction priority of a pod.
// Lower priority pods should be evicted first.
func GetPodPriority(pod *corev1.Pod) PodPriority {
	// Check QoS class
	switch pod.Status.QOSClass {
	case corev1.PodQOSBestEffort:
		return PriorityLow
	case corev1.PodQOSBurstable:
		return PriorityMedium
	case corev1.PodQOSGuaranteed:
		return PriorityHigh
	}
	return PriorityMedium
}

// SortPodsForEviction sorts pods by eviction priority (lowest priority first).
func SortPodsForEviction(pods []corev1.Pod) []corev1.Pod {
	// Simple bubble sort (pods list is typically small)
	sorted := make([]corev1.Pod, len(pods))
	copy(sorted, pods)

	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			if GetPodPriority(&sorted[j]) > GetPodPriority(&sorted[j+1]) {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}
	return sorted
}
