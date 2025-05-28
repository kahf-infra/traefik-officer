package main

import (
	"bufio"
	"context"
	"fmt"
	"sync"
	"time"

	logger "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func int64Ptr(i int64) *int64 {
	return &i
}

const (
	maxRetries     = 10
	initialBackoff = 1 * time.Second
	maxBackoff     = 5 * time.Minute
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
	podStreams map[string]*podStream
	podMutex   sync.Mutex

	// For graceful shutdown
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewKubernetesLogSource creates a new Kubernetes-based log source
func NewKubernetesLogSource(namespace, containerName, labelSelector string) (*KubernetesLogSource, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to kubeconfig if not in cluster
		logger.Info("Not in cluster, trying kubeconfig...")
		return nil, fmt.Errorf("kubernetes config error: %v", err)
	}

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client error: %v", err)
	}

	return &KubernetesLogSource{
		clientSet:     clientSet,
		namespace:     namespace,
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

	for {
		select {
		case <-kls.stopCh:
			return
		default:
			// Continue with the sync
		}

		err := wait.ExponentialBackoff(backoff, func() (bool, error) {
			return kls.syncPods()
		})

		if err != nil {
			logger.Errorf("Failed to sync pods after %d attempts: %v", maxRetries, err)
			// Reset backoff and try again
			time.Sleep(initialBackoff)
		}
	}
}

// syncPods synchronizes the current state of pods with the desired state
func (kls *KubernetesLogSource) syncPods() (bool, error) {
	// List all pods matching the label selector
	pods, err := kls.clientSet.CoreV1().Pods(kls.namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: kls.labelSelector,
	})

	if err != nil {
		logger.Errorf("Error listing pods: %v", err)
		return false, fmt.Errorf("error listing pods: %v", err)
	}

	if len(pods.Items) == 0 {
		logger.Warnf("No pods found with selector: %s", kls.labelSelector)
		return false, fmt.Errorf("no pods found with selector: %s", kls.labelSelector)
	}

	logger.Infof("Found %d pods with selector %s", len(pods.Items), kls.labelSelector)

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
			err := kls.streamPodLogs(ctx, podName)
			if err != nil {
				if wait.Interrupted(err) {
					logger.Infof("Stopping log streaming for pod %s", podName)
					return
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

// streamPodLogs handles the actual log streaming for a single pod
func (kls *KubernetesLogSource) streamPodLogs(ctx context.Context, podName string) error {
	req := kls.clientSet.CoreV1().Pods(kls.namespace).GetLogs(podName, &v1.PodLogOptions{
		Container: kls.containerName,
		Follow:    true,
		TailLines: int64Ptr(0), // Start from now
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
