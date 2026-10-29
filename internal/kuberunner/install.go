package kuberunner

import (
	"context"
	"fmt"
)

// Install uses ordinary Kubernetes resources. It does not provision a cluster,
// modify cloud IAM, expose a listener, or create provider credentials.
func (c Connector) Install(ctx context.Context) error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	if _, err := c.get(ctx, "runtimeclass", "gvisor"); err != nil {
		return fmt.Errorf("a working gvisor RuntimeClass is required: %w", err)
	}
	objects := c.InstallObjects()
	for _, object := range objects {
		if err := c.json(ctx, "apply", object); err != nil {
			return err
		}
	}
	return nil
}

func (c Connector) InstallObjects() []map[string]any {
	object := func(kind, name string, spec any) map[string]any {
		return map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": c.Config.Namespace}, "spec": spec}
	}
	selector := func(app string) map[string]any { return map[string]any{"matchLabels": map[string]string{"app": app}} }
	port := func(protocol string, number int) map[string]any {
		return map[string]any{"protocol": protocol, "port": number}
	}
	dns := map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, "podSelector": map[string]any{"matchLabels": map[string]string{"k8s-app": "kube-dns"}}}, map[string]any{"ipBlock": map[string]string{"cidr": "169.254.20.10/32"}}}, "ports": []any{port("UDP", 53), port("TCP", 53)}}
	namespace := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": c.Config.Namespace, "labels": map[string]string{"pod-security.kubernetes.io/enforce": "restricted", "pod-security.kubernetes.io/enforce-version": "latest"}}}
	agentPolicy := object("NetworkPolicy", "agent-isolation", map[string]any{"podSelector": selector("machinist-agent"), "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{}, "egress": []any{dns, map[string]any{"to": []any{map[string]any{"podSelector": selector("machinist-egress")}}, "ports": []any{port("TCP", 3128)}}}})
	proxyPolicy := object("NetworkPolicy", "proxy-isolation", map[string]any{"podSelector": selector("machinist-egress"), "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": selector("machinist-agent")}}, "ports": []any{port("TCP", 3128)}}}, "egress": []any{dns, map[string]any{"to": []any{map[string]any{"ipBlock": map[string]any{"cidr": "0.0.0.0/0", "except": []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"}}}}, "ports": []any{port("TCP", 443)}}}})
	labels := map[string]string{"app": "machinist-egress"}
	proxy := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "machinist-egress", "namespace": c.Config.Namespace}, "spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": labels}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"runtimeClassName": "gvisor", "automountServiceAccountToken": false, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 1000, "runAsGroup": 1000, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}, "containers": []any{map[string]any{"name": "proxy", "image": c.Config.Image, "command": []string{"machinist", "sandbox", "proxy", "--allow=chatgpt.com,auth.openai.com,api.openai.com,api.anthropic.com,registry.npmjs.org,proxy.golang.org,storage.googleapis.com,sum.golang.org,pypi.org,files.pythonhosted.org,github.com,codeload.github.com"}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "resources": map[string]any{"requests": map[string]string{"cpu": "250m", "memory": "512Mi", "ephemeral-storage": "1Gi"}, "limits": map[string]string{"cpu": "250m", "memory": "512Mi", "ephemeral-storage": "1Gi"}}, "readinessProbe": map[string]any{"tcpSocket": map[string]int{"port": 3128}}, "livenessProbe": map[string]any{"tcpSocket": map[string]int{"port": 3128}}}}}}}}
	service := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "machinist-egress", "namespace": c.Config.Namespace}, "spec": map[string]any{"selector": labels, "ports": []any{map[string]any{"port": 3128, "targetPort": 3128}}}}
	return []map[string]any{namespace, agentPolicy, proxyPolicy, proxy, service}
}
