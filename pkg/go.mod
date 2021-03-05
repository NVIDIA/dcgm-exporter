module dcgm-exporter

go 1.14

replace github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm => ../bindings/go/dcgm

require (
	github.com/Masterminds/semver v1.5.0 // indirect
	github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm v0.0.0-00010101000000-000000000000
	github.com/gorilla/mux v1.8.0
	github.com/sirupsen/logrus v1.7.0
	github.com/urfave/cli/v2 v2.3.0
	google.golang.org/grpc v1.35.0
	k8s.io/kubelet v0.20.2
)
