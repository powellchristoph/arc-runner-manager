package k8s

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Client wraps the kubernetes clientset with helper methods for ARC operations.
type Client struct {
	cs     kubernetes.Interface
	logger *slog.Logger
}

// NewClient constructs a Client using the provided in-cluster REST config.
func NewClient(cfg *rest.Config, logger *slog.Logger) (*Client, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s client init: %w", err)
	}
	return &Client{cs: cs, logger: logger}, nil
}

// EnsureNamespace creates the namespace if it does not exist.
func (c *Client) EnsureNamespace(ctx context.Context, ns string) error {
	_, err := c.cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("get namespace %s: %w", ns, err)
	}

	obj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{ //#nosec G101 -- false positive: label values are not credentials
				"app.kubernetes.io/managed-by": "arc-runner-manager",
				"arc-runner-manager/team":      teamFromNS(ns),
			},
		},
	}
	_, err = c.cs.CoreV1().Namespaces().Create(ctx, obj, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", ns, err)
	}
	c.logger.Info("created namespace", "namespace", ns)
	return nil
}

// DeleteNamespace removes the namespace (and everything in it).
func (c *Client) DeleteNamespace(ctx context.Context, ns string) error {
	c.logger.Info("deleting namespace", "namespace", ns)
	err := c.cs.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete namespace %s: %w", ns, err)
	}
	return nil
}

// UpsertGitHubAppSecret creates or updates the Kubernetes Secret holding
// a team's GitHub App credentials. The secret is referenced by the ARC
// Helm chart via the githubConfigSecret value.
func (c *Client) UpsertGitHubAppSecret(
	ctx context.Context,
	namespace, secretName, appID, installationID, privateKey string,
) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
			Labels: map[string]string{ //#nosec G101 -- false positive: label values are not credentials
				"app.kubernetes.io/managed-by":   "arc-runner-manager",
				"arc-runner-manager/secret-type": "github-app",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"github_app_id":              appID,
			"github_app_installation_id": installationID,
			"github_app_private_key":     privateKey,
		},
	}

	existing, err := c.cs.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = c.cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create secret %s/%s: %w", namespace, secretName, err)
		}
		c.logger.Info("created github app secret", "namespace", namespace, "secret", secretName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	}

	// Update in place — only overwrite keys that were supplied.
	if existing.StringData == nil {
		existing.StringData = map[string]string{}
	}
	if appID != "" {
		existing.StringData["github_app_id"] = appID
	}
	if installationID != "" {
		existing.StringData["github_app_installation_id"] = installationID
	}
	if privateKey != "" {
		existing.StringData["github_app_private_key"] = privateKey
	}

	_, err = c.cs.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update secret %s/%s: %w", namespace, secretName, err)
	}
	c.logger.Info("updated github app secret", "namespace", namespace, "secret", secretName)
	return nil
}

// DeleteSecret removes a secret. Not-found is not an error.
func (c *Client) DeleteSecret(ctx context.Context, namespace, secretName string) error {
	err := c.cs.CoreV1().Secrets(namespace).Delete(ctx, secretName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete secret %s/%s: %w", namespace, secretName, err)
	}
	return nil
}

// SecretExists reports whether a named secret exists in the given namespace.
func (c *Client) SecretExists(ctx context.Context, namespace, secretName string) (bool, error) {
	_, err := c.cs.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// RunnerPodCounts returns the number of running and pending runner pods in a namespace.
// It selects pods with the ARC-managed runner label.
func (c *Client) RunnerPodCounts(ctx context.Context, namespace string) (running, pending int, err error) {
	pods, err := c.cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "actions-runner-controller-version",
	})
	if err != nil {
		// Namespace may not exist yet — return zeros.
		return 0, 0, nil
	}
	for _, p := range pods.Items {
		switch p.Status.Phase {
		case corev1.PodRunning:
			running++
		case corev1.PodPending:
			pending++
		}
	}
	return running, pending, nil
}

// NamespacedManagedBy returns true if the namespace exists and has our managed-by label.
func (c *Client) NamespacedManagedBy(ctx context.Context, ns string) (bool, error) {
	obj, err := c.cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return obj.Labels["app.kubernetes.io/managed-by"] == "arc-runner-manager", nil
}

func teamFromNS(ns string) string {
	const prefix = "arc-"
	if len(ns) > len(prefix) {
		return ns[len(prefix):]
	}
	return ns
}
