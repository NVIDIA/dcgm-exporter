package k8s

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const localPortForwardHost = "127.0.0.1"

// localPortForwardURL builds a URL for a service exposed through IPv4 loopback forwarding.
func localPortForwardURL(scheme string, port int, path string) string {
	return fmt.Sprintf("%s://%s/%s", scheme, net.JoinHostPort(localPortForwardHost, strconv.Itoa(port)), strings.TrimPrefix(path, "/"))
}
