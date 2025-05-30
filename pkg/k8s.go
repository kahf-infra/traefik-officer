package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	logger "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	maxRetries          = 10
	initialBackoff      = 1 * time.Second
	maxBackoff          = 1 * time.Minute  // Reduced from 5 minutes
	syncInterval        = 10 * time.Second // Reduced from 30s for faster recovery
	podDiscoveryTimeout = 15 * time.Second // Reduced from 5m for faster pod discovery
)

// podStream represents a running log stream for a pod
type podStream struct {
	cancelFunc context.CancelFunc
	podName    string
}

// KubernetesLogSource reads from Kubernetes pod logs
type KubernetesLogSource struct {
	clientSet     *kubernetes.Clientset
	namespace     string
	containerName string
	labelSelector string
	lines         chan LogLine

	// For managing pod streams
	podStreams  map[string]*podStream
	podMutex    sync.Mutex
	lastPodSync time.Time
	lastPodList []v1.Pod

	// For graceful shutdown
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// K8SConfig holds the Kubernetes configuration options
type K8SConfig struct {
	InCluster     bool
	KubeConfig    string
	Context       string
	Namespace     string
	LabelSelector string
}

// NewKubernetesConfig creates a new Kubernetes client configuration
func NewKubernetesConfig(config K8SConfig) (*rest.Config, error) {
	var kubeconfig *string
	if config.KubeConfig != "" {
		kubeconfig = &config.KubeConfig
	} else if home := homeDir(); home != "" {
		if _, err := os.Stat(filepath.Join(home, ".kube", "config")); err == nil {
			defaultKubeConfig := filepath.Join(home, ".kube", "config")
			kubeconfig = &defaultKubeConfig
		}
	}

	// If in-cluster is explicitly set or we're running in a pod
	if config.InCluster || (kubeconfig == nil && os.Getenv("KUBERNETES_SERVICE_HOST") != "") {
		return rest.InClusterConfig()
	}

	// Use the current context in kubeconfig
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != nil {
		loadingRules.ExplicitPath = *kubeconfig
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if config.Context != "" {
		configOverrides.CurrentContext = config.Context
	}

	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		configOverrides,
	).ClientConfig()
}

// NewKubernetesClientset creates a new Kubernetes clientset
func NewKubernetesClientset(config K8SConfig) (*kubernetes.Clientset, error) {
	kubeConfig, err := NewKubernetesConfig(config)
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(kubeConfig)
}

// NewKubernetesLogSource creates a new Kubernetes-based log source
func NewKubernetesLogSource(k8sConfig *K8SConfig, containerName, labelSelector string) (*KubernetesLogSource, error) {
	clientSet, err := NewKubernetesClientset(*k8sConfig)
	if err != nil {
		log.Fatalf("Error creating Kubernetes client: %v", err)
	}

	return &KubernetesLogSource{
		clientSet:     clientSet,
		namespace:     k8sConfig.Namespace,
		containerName: containerName,
		labelSelector: labelSelector,
		lines:         make(chan LogLine, 1000),
		podStreams:    make(map[string]*podStream),
		stopCh:        make(chan struct{}),
	}, nil
}

func (kls *KubernetesLogSource) ReadLines() <-chan LogLine {
	return kls.lines
}

// startStreaming starts the log streaming process
func (kls *KubernetesLogSource) startStreaming() error {
	// Start the pod watcher in the background
	kls.wg.Add(1)
	go kls.watchPods()
	// Initial sync of pods
	_, err := kls.syncPods()
	return err
}

// watchPods watches for pod changes and updates log streams accordingly
func (kls *KubernetesLogSource) watchPods() {
	defer kls.wg.Done()

	backoff := wait.Backoff{
		Steps:    maxRetries,
		Duration: initialBackoff,
		Factor:   2.0,
		Jitter:   0.1,
		Cap:      maxBackoff,
	}

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-kls.stopCh:
			return
		case <-ticker.C:
			// Only sync if we haven't synced recently
			if time.Since(kls.lastPodSync) < syncInterval {
				continue
			}

			err := wait.ExponentialBackoff(backoff, func() (bool, error) {
				success, err := kls.syncPods()
				if success {
					kls.lastPodSync = time.Now()
				}
				return success, err
			})

			if err != nil {
				logger.Warnf("Failed to sync pods: %v", err)
			}
		}
	}
}

// syncPods synchronizes the current state of pods with the desired state
func (kls *KubernetesLogSource) syncPods() (bool, error) {
	// Only use cache if we have a recent successful sync
	if !kls.lastPodSync.IsZero() && time.Since(kls.lastPodSync) < podDiscoveryTimeout {
		return true, nil
	}

	// List all pods matching the label selector
	pods, err := kls.clientSet.CoreV1().Pods(kls.namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: kls.labelSelector,
	})

	if err != nil {
		logger.Warnf("Error listing pods: %v", err)
		return false, fmt.Errorf("error listing pods: %v", err)
	}

	if len(pods.Items) == 0 {
		logger.Warnf("No pods found with selector: %s", kls.labelSelector)
		return false, fmt.Errorf("no pods found with selector: %s", kls.labelSelector)
	}

	if logger.GetLevel() >= logger.DebugLevel {
		logger.Debugf("Found %d pods with selector %s", len(pods.Items), kls.labelSelector)
	}

	// Update the cached pod list and sync time
	kls.podMutex.Lock()
	kls.lastPodList = pods.Items
	kls.podMutex.Unlock()
	kls.lastPodSync = time.Now()

	// Track current pods to detect removed ones
	currentPods := make(map[string]bool)

	// Ensure log streams for all running pods
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning && isContainerReady(&pod, kls.containerName) {
			podName := pod.Name
			currentPods[podName] = true
			kls.ensurePodStream(podName)
		}
	}

	// Clean up streams for pods that no longer exist
	kls.podMutex.Lock()
	defer kls.podMutex.Unlock()

	for podName, stream := range kls.podStreams {
		if !currentPods[podName] {
			logger.Infof("Removing log stream for pod %s (pod no longer exists)", podName)
			stream.cancelFunc()
			delete(kls.podStreams, podName)
		}
	}

	return true, nil
}

// isContainerReady checks if the specified container in the pod is ready
func isContainerReady(pod *v1.Pod, containerName string) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == containerName {
			return status.Ready
		}
	}
	return false
}

// ensurePodStream ensures that a pod's logs are being streamed
func (kls *KubernetesLogSource) ensurePodStream(podName string) {
	kls.podMutex.Lock()
	defer kls.podMutex.Unlock()

	// Skip if already streaming this pod
	if _, exists := kls.podStreams[podName]; exists {
		return
	}

	// Set up context for this pod's log stream
	ctx, cancel := context.WithCancel(context.Background())
	stream := &podStream{
		cancelFunc: cancel,
		podName:    podName,
	}
	kls.podStreams[podName] = stream

	// Start the log stream in a goroutine
	kls.wg.Add(1)
	go func() {
		defer kls.wg.Done()
		kls.streamPodLogsWithRetry(ctx, podName)
	}()

	logger.Infof("Started log streaming for pod: %s", podName)
}

// streamPodLogsWithRetry handles retries for pod log streaming
func (kls *KubernetesLogSource) streamPodLogsWithRetry(ctx context.Context, podName string) {
	backoff := wait.Backoff{
		Steps:    maxRetries,
		Duration: initialBackoff,
		Factor:   2.0,
		Jitter:   0.1,
		Cap:      maxBackoff,
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Check if pod still exists before trying to stream
			exists, err := kls.podExists(podName)
			if err != nil {
				logger.Warnf("Error checking pod %s existence: %v", podName, err)
			}
			if !exists {
				logger.Infof("Pod %s no longer exists, stopping log stream", podName)
				return
			}

			err = kls.streamPodLogs(ctx, podName)
			if err != nil {
				if wait.Interrupted(err) {
					logger.Infof("Stopping log streaming for pod %s", podName)
					return
				}

				// If pod is not found, force a pod resync
				if strings.Contains(err.Error(), "not found") {
					logger.Debugf("Pod %s not found, forcing pod resync", podName)
					kls.forcePodResync()
				}

				// Log the error and retry with backoff
				delay := backoff.Step()
				logger.Warnf("Error streaming logs from pod %s (retrying in %v): %v", podName, delay, err)
				time.Sleep(delay)
				continue
			}

			// If we get here, the stream ended unexpectedly but without an error
			logger.Debugf("Log stream ended for pod %s, reconnecting...", podName)
			time.Sleep(time.Second)
		}
	}
}

// podExists checks if a pod exists in the cluster
func (kls *KubernetesLogSource) podExists(podName string) (bool, error) {
	_, err := kls.clientSet.CoreV1().Pods(kls.namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// forcePodResync forces an immediate pod resync by clearing the last sync time
func (kls *KubernetesLogSource) forcePodResync() {
	kls.podMutex.Lock()
	defer kls.podMutex.Unlock()
	kls.lastPodSync = time.Time{} // Zero time will force a resync
}

// streamPodLogs handles the actual log streaming for a single pod
func (kls *KubernetesLogSource) streamPodLogs(ctx context.Context, podName string) error {
	// Get current time to only stream logs from this point forward
	sinceTime := metav1.NewTime(time.Now())

	req := kls.clientSet.CoreV1().Pods(kls.namespace).GetLogs(podName, &v1.PodLogOptions{
		Container: kls.containerName,
		Follow:    true,
		SinceTime: &sinceTime, // Only get logs from this time forward
	})

	podLogs, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("error opening log stream for pod %s: %v", podName, err)
	}
	defer func() {
		if err := podLogs.Close(); err != nil {
			logger.Warnf("Error closing log stream for pod %s: %v", podName, err)
		}
	}()

	scanner := bufio.NewScanner(podLogs)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
			kls.lines <- LogLine{
				Text: fmt.Sprintf("[%s] %s", podName, scanner.Text()),
				Time: time.Now(),
				Err:  nil,
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading log stream from pod %s: %v", podName, err)
	}

	return nil
}

func (kls *KubernetesLogSource) Close() error {
	// Signal all goroutines to stop
	close(kls.stopCh)

	// Cancel all pod streams
	kls.podMutex.Lock()
	defer kls.podMutex.Unlock()

	for podName, stream := range kls.podStreams {
		logger.Infof("Stopping log stream for pod: %s", podName)
		stream.cancelFunc()
	}

	// Wait for all goroutines to finish
	kls.wg.Wait()
	return nil
}

// AddKubernetesFlags adds Kubernetes-related command line flags
func AddKubernetesFlags(flags *flag.FlagSet) *K8SConfig {
	config := &K8SConfig{}

	flags.BoolVar(&config.InCluster, "in-cluster", false,
		"Use in-cluster Kubernetes configuration")
	flags.StringVar(&config.KubeConfig, "kubeconfig", "",
		"Path to kubeconfig file (default is $HOME/.kube/config)")
	flags.StringVar(&config.Context, "kube-context", "",
		"Kubernetes context to use (default is current context)")
	flags.StringVar(&config.Namespace, "namespace", "ingress-controller",
		"Kubernetes namespace to monitor")
	flags.StringVar(&config.LabelSelector, "label-selector", "",
		"Label selector for pods (e.g., 'app=myapp')")

	return config
}
