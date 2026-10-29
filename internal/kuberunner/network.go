package kuberunner

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// ProbeNetwork runs in the credential-free init container. Missing enforcement
// fails before the agent container starts or receives its model credential.
func ProbeNetwork(ctx context.Context, provider string) error {
	api := net.JoinHostPort(os.Getenv("KUBERNETES_SERVICE_HOST"), "443")
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return fmt.Errorf("Kubernetes API address is unavailable for isolation verification")
	}
	for _, address := range []string{"169.254.169.254:80", api, "1.1.1.1:443"} {
		connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", address)
		if err == nil {
			connection.Close()
			return fmt.Errorf("sandbox egress isolation is not enforced")
		}
	}
	proxy, err := url.Parse(os.Getenv("HTTPS_PROXY"))
	if err != nil || proxy.Host == "" {
		return fmt.Errorf("sandbox proxy configuration is missing")
	}
	for _, host := range []string{"example.com", provider} {
		connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", proxy.Host)
		if err != nil {
			return fmt.Errorf("sandbox proxy is unreachable")
		}
		_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
		_, err = fmt.Fprintf(connection, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", host, host)
		if err != nil {
			connection.Close()
			return fmt.Errorf("sandbox proxy probe failed")
		}
		response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: "CONNECT"})
		connection.Close()
		if err != nil || (host == "example.com" && response.StatusCode != 403) || (host == provider && response.StatusCode != 200) {
			return fmt.Errorf("sandbox proxy allowlist is not enforced or provider is unavailable")
		}
	}
	return nil
}
