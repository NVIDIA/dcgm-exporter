module github.com/NVIDIA/dcgm-exporter

go 1.20

replace (
	k8s.io/api => k8s.io/api v0.28.1
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.28.1
	k8s.io/apimachinery => k8s.io/apimachinery v0.28.1
	k8s.io/apiserver => k8s.io/apiserver v0.28.1
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.28.1
	k8s.io/client-go => k8s.io/client-go v0.28.1
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.28.1
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.28.1
	k8s.io/code-generator => k8s.io/code-generator v0.28.1
	k8s.io/component-base => k8s.io/component-base v0.28.1
	k8s.io/cri-api => k8s.io/cri-api v0.28.1
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.28.1
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.28.1
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.28.1
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.28.1
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.28.1
	k8s.io/kubectl => k8s.io/kubectl v0.28.1
	k8s.io/kubelet => k8s.io/kubelet v0.28.1
	k8s.io/kubernetes => k8s.io/kubernetes v1.28.1
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.28.1
	k8s.io/metrics => k8s.io/metrics v0.28.1
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.28.1
)

require (
	github.com/NVIDIA/go-dcgm v0.0.0-20230816170901-d898cc7820fe
	github.com/NVIDIA/gpu-monitoring-tools v0.0.0-20211102125545-5a2c58442e48
	github.com/gorilla/mux v1.8.0
	github.com/sirupsen/logrus v1.9.3
	github.com/stretchr/testify v1.8.2
	github.com/urfave/cli/v2 v2.25.7
	google.golang.org/grpc v1.58.0
	k8s.io/api v0.28.1
	k8s.io/apimachinery v0.28.1
	k8s.io/client-go v1.5.2
	k8s.io/kubelet v0.28.1
	k8s.io/kubernetes v0.0.0-00010101000000-000000000000
)

require (
	github.com/Masterminds/semver v1.5.0 // indirect
	github.com/Microsoft/go-winio v0.6.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bits-and-blooms/bitset v1.8.0 // indirect
	github.com/blang/semver v3.5.1+incompatible // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/emicklei/go-restful/v3 v3.9.0 // indirect
	github.com/evanphx/json-patch v5.6.0+incompatible // indirect
	github.com/go-logr/logr v1.2.4 // indirect
	github.com/go-openapi/jsonpointer v0.19.6 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.22.3 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/google/gnostic-models v0.6.8 // indirect
	github.com/google/go-cmp v0.5.9 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/googleapis/gnostic v0.5.5 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_golang v1.16.0 // indirect
	github.com/prometheus/client_model v0.4.0 // indirect
	github.com/prometheus/common v0.44.0 // indirect
	github.com/prometheus/procfs v0.10.1 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/xrash/smetrics v0.0.0-20201216005158-039620a65673 // indirect
	golang.org/x/crypto v0.13.0 // indirect
	golang.org/x/mod v0.10.0 // indirect
	golang.org/x/net v0.15.0 // indirect
	golang.org/x/oauth2 v0.12.0 // indirect
	golang.org/x/sys v0.12.0 // indirect
	golang.org/x/term v0.12.0 // indirect
	golang.org/x/text v0.13.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	golang.org/x/tools v0.8.0 // indirect
	google.golang.org/appengine v1.6.8 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230822172742-b8732ec3820d // indirect
	google.golang.org/protobuf v1.31.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/apiextensions-apiserver v0.0.0 // indirect
	k8s.io/apiserver v0.28.1 // indirect
	k8s.io/component-base v0.28.1 // indirect
	k8s.io/klog/v2 v2.100.1 // indirect
	k8s.io/kube-openapi v0.0.0-20230717233707-2695361300d9 // indirect
	k8s.io/utils v0.0.0-20230726121419-3b25d923346b // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.3.0 // indirect
	sigs.k8s.io/yaml v1.3.0 // indirect
)
