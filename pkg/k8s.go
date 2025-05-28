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
	podName       string
	namespace     string
	containerName string
	lines         chan LogLine
	cancel        context.CancelFunc
}

// NewKubernetesLogSource creates a new Kubernetes-based log source
func NewKubernetesLogSource(podName, namespace, containerName string) (*KubernetesLogSource, error) {
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
		podName:       podName,
		namespace:     namespace,
		containerName: containerName,
		lines:         make(chan LogLine, 100),
	}

	return kls, nil
}

func (kls *KubernetesLogSource) ReadLines() <-chan LogLine {
	return kls.lines
}

func (kls *KubernetesLogSource) startStreaming() error {
	ctx, cancel := context.WithCancel(context.Background())
	kls.cancel = cancel

	req := kls.clientSet.CoreV1().Pods(kls.namespace).GetLogs(kls.podName, &v1.PodLogOptions{
		Container: kls.containerName,
		Follow:    true,
		TailLines: int64Ptr(0), // Start from now
	})

	podLogs, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("error opening log stream: %v", err)
	}

	go func() {
		defer close(kls.lines)
		defer podLogs.Close()

		scanner := bufio.NewScanner(podLogs)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
				kls.lines <- LogLine{
					Text: scanner.Text(),
					Time: time.Now(),
					Err:  nil,
				}
			}
		}

		if err := scanner.Err(); err != nil {
			kls.lines <- LogLine{Text: "", Time: time.Now(), Err: err}
		}
	}()

	return nil
}

func (kls *KubernetesLogSource) Close() error {
	if kls.cancel != nil {
		kls.cancel()
	}
	return nil
}

// Add this function
func discoverTraefikPod(clientset *kubernetes.Clientset, namespace string) (string, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=traefik", // Adjust based on your Traefik labels
	})
	if err != nil {
		return "", err
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no Traefik pods found")
	}

	return pods.Items[0].Name, nil
}
