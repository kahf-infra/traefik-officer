package main

import (
	"bufio"
	"context"
	"fmt"
	logger "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"time"
)

func int64Ptr(i int64) *int64 {
	return &i
}

// KubernetesLogSource reads from Kubernetes pod logs
type KubernetesLogSource struct {
	clientSet     *kubernetes.Clientset
	namespace     string
	containerName string
	labelSelector string
	lines         chan LogLine
	cancelFuncs   []context.CancelFunc
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

	kls := &KubernetesLogSource{
		clientSet:     clientSet,
		namespace:     namespace,
		containerName: containerName,
		labelSelector: labelSelector,
		lines:         make(chan LogLine, 1000), // Increased buffer size for multiple pods
		cancelFuncs:   make([]context.CancelFunc, 0),
	}

	return kls, nil
}

func (kls *KubernetesLogSource) ReadLines() <-chan LogLine {
	return kls.lines
}

func (kls *KubernetesLogSource) startStreaming() error {
	// Find all pods matching the label selector
	pods, err := kls.clientSet.CoreV1().Pods(kls.namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: kls.labelSelector,
	})

	if err != nil {
		return fmt.Errorf("error listing pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found with selector: %s", kls.labelSelector)
	}

	logger.Infof("Found %d pods with selector %s", len(pods.Items), kls.labelSelector)

	// Start a log stream for each pod
	for _, pod := range pods.Items {
		podName := pod.Name
		ctx, cancel := context.WithCancel(context.Background())
		kls.cancelFuncs = append(kls.cancelFuncs, cancel)

		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					err := kls.streamPodLogs(ctx, podName)
					if err != nil {
						logger.Errorf("Error streaming logs from pod %s: %v. Will retry in 5 seconds...", podName, err)
						time.Sleep(5 * time.Second)
					} else {
						// If we get here, the stream ended unexpectedly
						time.Sleep(1 * time.Second)
					}
				}
			}
		}()
	}

	return nil
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
	defer podLogs.Close()

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
	for _, cancel := range kls.cancelFuncs {
		if cancel != nil {
			cancel()
		}
	}
	return nil
}

// discoverTraefikPod returns the name of a Traefik pod in the given namespace
// Deprecated: Use label selector with NewKubernetesLogSource instead
func discoverTraefikPod(clientset *kubernetes.Clientset, namespace string) (string, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=traefik",
	})

	if err != nil {
		return "", err
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no Traefik pods found in namespace %s", namespace)
	}

	return pods.Items[0].Name, nil
}
