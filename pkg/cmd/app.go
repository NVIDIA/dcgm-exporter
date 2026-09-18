package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/hostname"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/server"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/stdout"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
)

const (
	FlexKey                = "f" // Monitor all GPUs if MIG is disabled or all GPU instances if MIG is enabled
	MajorKey               = "g" // Monitor top-level entities: GPUs or NvSwitches or CPUs
	MinorKey               = "i" // Monitor sub-level entities: GPU instances/NvLinks/CPUCores - GPUI cannot be specified if MIG is disabled
	undefinedConfigMapData = appconfig.UndefinedConfigMapData
	deviceUsageTemplate    = `Specify which devices dcgm-exporter monitors.
	Possible values: {{.FlexKey}} or
	                 {{.MajorKey}}[:id1[,-id2...]] or
	                 {{.MinorKey}}[:id1[,-id2...]] or
	                 {{.MajorKey}}[:id1[,-id2...]]+{{.MinorKey}}[:id1[,-id2...]].
	If an id list is used, then devices with match IDs must exist on the system. For example:
		(default) = monitor all GPU instances in MIG mode, all GPUs if MIG mode is disabled. (See {{.FlexKey}})
		{{.MajorKey}} = Monitor all GPUs
		{{.MinorKey}} = Monitor all GPU instances
		{{.FlexKey}} = Monitor all GPUs if MIG is disabled, or all GPU instances if MIG is enabled.
                       Note: this rule will be applied to each GPU. If it has GPU instances, those
                             will be monitored. If it doesn't, then the GPU will be monitored.
                             This is our recommended option for single or mixed MIG Strategies.
		{{.MajorKey}}:0,1 = monitor GPUs 0 and 1
		{{.MinorKey}}:0,2-4 = monitor GPU instances 0, 2, 3, and 4.
		{{.MajorKey}}+{{.MinorKey}} = monitor all GPUs and GPU instances.

	NOTE 1: Any time indices are specified, those indices must exist on the system.
	NOTE 2: The flex option {{.FlexKey}} cannot be combined with {{.MajorKey}} or {{.MinorKey}}.`
	deviceGPUUsageNote = `

	NOTE 3: For GPU devices, i cannot be specified unless MIG mode is enabled.`
)

const (
	CLIConfigFile                       = "config-file"
	CLIFieldsFile                       = "collectors"
	CLIAddress                          = "address"
	CLICollectInterval                  = "collect-interval"
	CLIWatchMaxKeepAge                  = "watch-max-keep-age"
	CLIWatchMaxKeepSamples              = "watch-max-keep-samples"
	CLIKubernetes                       = "kubernetes"
	CLIKubernetesEnablePodLabels        = "kubernetes-enable-pod-labels"
	CLIKubernetesEnablePodUID           = "kubernetes-enable-pod-uid"
	CLIKubernetesGPUIDType              = "kubernetes-gpu-id-type"
	CLIKubernetesPodLabelAllowlistRegex = "kubernetes-pod-label-allowlist-regex"
	CLIUseOldNamespace                  = "use-old-namespace"
	CLIRemoteHEInfo                     = "remote-hostengine-info"
	CLIGPUDevices                       = "devices"
	CLISwitchDevices                    = "switch-devices"
	CLICPUDevices                       = "cpu-devices"
	CLIHealthRequireGPUs                = "health-require-gpus"
	CLINoHostname                       = "no-hostname"
	CLIUseFakeGPUs                      = "fake-gpus"
	CLIConfigMapData                    = "configmap-data"
	CLIWebSystemdSocket                 = "web-systemd-socket"
	CLIWebConfigFile                    = "web-config-file"
	CLIWebReadTimeout                   = "web-read-timeout"
	CLIWebWriteTimeout                  = "web-write-timeout"
	CLIMaxConcurrentScrapes             = "max-concurrent-scrapes"
	CLIEnableExporterMetrics            = "enable-exporter-metrics"
	CLIXIDCountWindowSize               = "xid-count-window-size"
	CLIReplaceBlanksInModelName         = "replace-blanks-in-model-name"
	CLIDebugMode                        = "debug"
	CLIClockEventsCountWindowSize       = "clock-events-count-window-size"
	CLIEnableDCGMLog                    = "enable-dcgm-log"
	CLIDCGMLogLevel                     = "dcgm-log-level"
	CLILogFormat                        = "log-format"
	CLIPodResourcesKubeletSocket        = "pod-resources-kubelet-socket"
	CLIHPCJobMappingDir                 = "hpc-job-mapping-dir"
	CLIContainerLabels                  = "container-labels"
	CLIContainerRuntimeSocket           = "container-runtime-socket"
	CLINvidiaResourceNames              = "nvidia-resource-names"
	CLIKubernetesVirtualGPUs            = "kubernetes-virtual-gpus"
	CLIDumpEnabled                      = "dump-enabled"
	CLIDumpDirectory                    = "dump-directory"
	CLIDumpRetention                    = "dump-retention"
	CLIDumpCompression                  = "dump-compression"
	CLIKubernetesEnableDRA              = "kubernetes-enable-dra"
	CLIDisableStartupValidate           = "disable-startup-validate"
	CLIEnableGPUBindUnbindWatch         = "enable-gpu-bind-unbind-watch"
	CLIGPUBindUnbindPollInterval        = "gpu-bind-unbind-poll-interval"
	CLIEnablePprof                      = "enable-pprof"
)

var (
	initializeDCGMProviderFunc  = dcgmprovider.Initialize
	initializeNVMLProviderFunc  = nvmlprovider.Initialize
	cleanupNVMLProviderFunc     = func() { nvmlprovider.Client().Cleanup() }
	buildRegistryFunc           = buildRegistry
	getCountersFunc             = getCounters
	startWatchListManagerFunc   = startDeviceWatchListManager
	getHostnameFunc             = hostname.GetHostname
	initCollectorFactoryFunc    = collector.InitCollectorFactory
	newMetricsServerFunc        = server.NewMetricsServer
	newFileWatcherFunc          = watcher.NewFileWatcher
	newGPUBindUnbindWatcherFunc = watcher.NewGPUBindUnbindWatcher
)

func NewApp(buildVersion ...string) *cli.App {
	c := cli.NewApp()
	c.Name = "DCGM Exporter"
	c.Usage = "Generates GPU metrics in the prometheus format"
	if len(buildVersion) == 0 {
		buildVersion = append(buildVersion, "")
	}
	c.Version = buildVersion[0]

	var deviceUsageBuffer bytes.Buffer
	t := template.Must(template.New("").Parse(deviceUsageTemplate))
	_ = t.Execute(&deviceUsageBuffer, map[string]string{"FlexKey": FlexKey, "MajorKey": MajorKey, "MinorKey": MinorKey})
	deviceUsageStr := deviceUsageBuffer.String()
	gpuDeviceUsageStr := deviceUsageStr + deviceGPUUsageNote

	c.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    CLIFieldsFile,
			Aliases: []string{"f"},
			Usage:   "Path to the file, that contains the DCGM fields to collect",
			Value:   appconfig.DefaultCollectorsFile,
			EnvVars: []string{"DCGM_EXPORTER_COLLECTORS"},
		},
		&cli.StringFlag{
			Name:    CLIConfigFile,
			Usage:   "Path to a YAML configuration file. Legacy flags and environment variables explicitly set on startup override YAML values.",
			Value:   "",
			EnvVars: []string{"DCGM_EXPORTER_CONFIG_FILE"},
		},
		&cli.StringFlag{
			Name:    CLIAddress,
			Aliases: []string{"a"},
			Value:   ":9400",
			Usage:   "Listen address as <HOST>:<PORT>. For IPv6, use \"[<IPv6_ADDR>]:<PORT>\" (e.g., \"[::]:9400\")",
			EnvVars: []string{"DCGM_EXPORTER_LISTEN"},
		},
		&cli.StringFlag{
			Name:    CLIWebReadTimeout,
			Value:   appconfig.DefaultWebReadTimeout.String(),
			Usage:   "Maximum duration for reading an HTTP request.",
			EnvVars: []string{"DCGM_EXPORTER_WEB_READ_TIMEOUT"},
		},
		&cli.StringFlag{
			Name:    CLIWebWriteTimeout,
			Value:   appconfig.DefaultWebWriteTimeout.String(),
			Usage:   "Maximum duration for generating and writing an HTTP response.",
			EnvVars: []string{"DCGM_EXPORTER_WEB_WRITE_TIMEOUT"},
		},
		&cli.IntFlag{
			Name:    CLIMaxConcurrentScrapes,
			Value:   appconfig.DefaultMaxConcurrentScrapes,
			Usage:   "Maximum concurrent scrape requests. Excess requests receive HTTP 503 while admitted overlapping requests are coalesced.",
			EnvVars: []string{"DCGM_EXPORTER_MAX_CONCURRENT_SCRAPES"},
		},
		&cli.BoolFlag{
			Name:    CLIEnableExporterMetrics,
			Value:   false,
			Usage:   "Expose Go runtime, process, and HTTP handler metrics about dcgm-exporter itself.",
			EnvVars: []string{"DCGM_EXPORTER_ENABLE_EXPORTER_METRICS"},
		},
		&cli.IntFlag{
			Name:    CLICollectInterval,
			Aliases: []string{"c"},
			Value:   30000,
			Usage:   "Interval of time at which point metrics are collected. Unit is milliseconds (ms).",
			EnvVars: []string{"DCGM_EXPORTER_INTERVAL"},
		},
		&cli.DurationFlag{
			Name:  CLIWatchMaxKeepAge,
			Value: appconfig.DefaultWatchMaxKeepAge,
			Usage: "Maximum age of samples retained by DCGM field watches. " +
				"Set to 0s to disable the age limit when a sample limit is configured.",
			EnvVars: []string{"DCGM_EXPORTER_WATCH_MAX_KEEP_AGE"},
		},
		&cli.Int64Flag{
			Name:  CLIWatchMaxKeepSamples,
			Value: appconfig.DefaultWatchMaxSamples,
			Usage: "Sample-count retention requested for DCGM field watches. " +
				"Set to 0 for no sample-count limit when an age limit is configured.",
			EnvVars: []string{"DCGM_EXPORTER_WATCH_MAX_KEEP_SAMPLES"},
		},
		&cli.BoolFlag{
			Name:    CLIKubernetes,
			Aliases: []string{"k"},
			Value:   false,
			Usage:   "Enable kubernetes mapping metrics to kubernetes pods",
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES"},
		},
		&cli.BoolFlag{
			Name:    CLIUseOldNamespace,
			Aliases: []string{"o"},
			Value:   false,
			Usage:   "Use old 1.x namespace",
			EnvVars: []string{"DCGM_EXPORTER_USE_OLD_NAMESPACE"},
		},
		&cli.StringFlag{
			Name:    CLICPUDevices,
			Aliases: []string{"p"},
			Value:   FlexKey,
			Usage:   deviceUsageStr,
			EnvVars: []string{"DCGM_EXPORTER_CPU_DEVICES_STR"},
		},
		&cli.StringFlag{
			Name:    CLIConfigMapData,
			Aliases: []string{"m"},
			Value:   undefinedConfigMapData,
			Usage:   "ConfigMap <NAMESPACE>:<NAME> for metric data",
			EnvVars: []string{"DCGM_EXPORTER_CONFIGMAP_DATA"},
		},
		&cli.StringFlag{
			Name:    CLIRemoteHEInfo,
			Aliases: []string{"r"},
			Value:   "localhost:5555",
			Usage:   "Connect to remote hostengine at <HOST>:<PORT> or a DCGM URI (tcp://<HOST>:<PORT>, unix:///<SOCKET_PATH>, vsock://<CID>:<PORT>). For IPv6, use \"[<IPv6_ADDR>]:<PORT>\" (e.g., \"[::1]:5555\")",
			EnvVars: []string{"DCGM_REMOTE_HOSTENGINE_INFO"},
		},
		&cli.BoolFlag{
			Name:    CLIKubernetesEnablePodLabels,
			Value:   false,
			Usage:   "Enable kubernetes pod labels in metrics. This parameter is effective only when the '--kubernetes' option is set to 'true'.",
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"},
		},
		&cli.BoolFlag{
			Name:    CLIKubernetesEnablePodUID,
			Value:   false,
			Usage:   "Enable kubernetes pod UID in metrics. This parameter is effective only when the '--kubernetes' option is set to 'true'.",
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_ENABLE_POD_UID"},
		},
		&cli.StringFlag{
			Name:  CLIKubernetesGPUIDType,
			Value: string(appconfig.GPUUID),
			Usage: fmt.Sprintf("Choose Type of GPU ID to use to map kubernetes resources to pods. Possible values: '%s', '%s'",
				appconfig.GPUUID, appconfig.DeviceName),
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE"},
		},
		&cli.StringSliceFlag{
			Name:    CLIKubernetesPodLabelAllowlistRegex,
			Value:   cli.NewStringSlice(),
			Usage:   "Regex patterns for filtering pod labels to include in metrics (comma-separated). Empty means include all labels. This parameter is effective only when '--kubernetes-enable-pod-labels' is true.",
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_POD_LABEL_ALLOWLIST_REGEX"},
		},
		&cli.StringFlag{
			Name:    CLIGPUDevices,
			Aliases: []string{"d"},
			Value:   FlexKey,
			Usage:   gpuDeviceUsageStr,
			EnvVars: []string{"DCGM_EXPORTER_DEVICES_STR"},
		},
		&cli.BoolFlag{
			Name:    CLINoHostname,
			Aliases: []string{"n"},
			Value:   false,
			Usage:   "Omit the hostname information from the output, matching older versions.",
			EnvVars: []string{"DCGM_EXPORTER_NO_HOSTNAME"},
		},
		&cli.BoolFlag{
			Name:  CLIHealthRequireGPUs,
			Value: false,
			Usage: "Report /health as unhealthy when no GPU collector is registered. " +
				"Off by default so nodes that legitimately expose no GPUs keep passing. " +
				"Enable where every instance is expected to see at least one GPU, so that " +
				"a liveness probe on /health restarts an exporter that came up against a " +
				"hostengine with no GPUs visible.",
			EnvVars: []string{"DCGM_EXPORTER_HEALTH_REQUIRE_GPUS"},
		},
		&cli.StringFlag{
			Name:    CLISwitchDevices,
			Aliases: []string{"s"},
			Value:   FlexKey,
			Usage:   deviceUsageStr,
			EnvVars: []string{"DCGM_EXPORTER_OTHER_DEVICES_STR"},
		},
		&cli.BoolFlag{
			Name:    CLIUseFakeGPUs,
			Value:   false,
			Usage:   "Accept GPUs that are fake, for testing purposes only",
			EnvVars: []string{"DCGM_EXPORTER_USE_FAKE_GPUS"},
		},
		&cli.StringFlag{
			Name:    CLIWebConfigFile,
			Value:   "",
			Usage:   "Web configuration file following webConfig spec: https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md.",
			EnvVars: []string{"DCGM_EXPORTER_WEB_CONFIG_FILE"},
		},
		&cli.IntFlag{
			Name:    CLIXIDCountWindowSize,
			Aliases: []string{"x"},
			Value:   int((5 * time.Minute).Milliseconds()),
			Usage:   "Window size in milliseconds (ms) for counting XID errors in DCGM_EXP_XID_ERRORS_COUNT.",
			EnvVars: []string{"DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE"},
		},
		&cli.BoolFlag{
			Name:    CLIReplaceBlanksInModelName,
			Aliases: []string{"rbmn"},
			Value:   false,
			Usage:   "Replace every blank space in the GPU model name with a dash, ensuring a continuous, space-free identifier.",
			EnvVars: []string{"DCGM_EXPORTER_REPLACE_BLANKS_IN_MODEL_NAME"},
		},
		&cli.BoolFlag{
			Name:    CLIDebugMode,
			Value:   false,
			Usage:   "Enable debug output",
			EnvVars: []string{"DCGM_EXPORTER_DEBUG"},
		},
		&cli.IntFlag{
			Name:    CLIClockEventsCountWindowSize,
			Value:   int((5 * time.Minute).Milliseconds()),
			Usage:   "Window size in milliseconds (ms) for counting clock events in DCGM_EXP_CLOCK_EVENTS_COUNT.",
			EnvVars: []string{"DCGM_EXPORTER_CLOCK_EVENTS_COUNT_WINDOW_SIZE"},
		},
		&cli.BoolFlag{
			Name:    CLIEnableDCGMLog,
			Value:   false,
			Usage:   "Enable writing DCGM logs to standard output (stdout).",
			EnvVars: []string{"DCGM_EXPORTER_ENABLE_DCGM_LOG"},
		},
		&cli.StringFlag{
			Name:    CLIDCGMLogLevel,
			Value:   DCGMDbgLvlNone,
			Usage:   "Specify the DCGM log verbosity level. This parameter is effective only when the '--enable-dcgm-log' option is set to 'true'. Possible values: NONE, FATAL, ERROR, WARN, INFO, DEBUG and VERB",
			EnvVars: []string{"DCGM_EXPORTER_DCGM_LOG_LEVEL"},
		},
		&cli.StringFlag{
			Name:    CLILogFormat,
			Value:   "text",
			Usage:   "Specify the log output format. Possible values: text, json",
			EnvVars: []string{"DCGM_EXPORTER_LOG_FORMAT"},
		},
		&cli.StringFlag{
			Name:    CLIPodResourcesKubeletSocket,
			Value:   "/var/lib/kubelet/pod-resources/kubelet.sock",
			Usage:   "Path to the kubelet pod-resources socket file.",
			EnvVars: []string{"DCGM_POD_RESOURCES_KUBELET_SOCKET"},
		},
		&cli.StringFlag{
			Name:    CLIHPCJobMappingDir,
			Value:   "",
			Usage:   "Path to HPC job mapping file directory used for mapping GPUs to jobs.",
			EnvVars: []string{"DCGM_HPC_JOB_MAPPING_DIR"},
		},
		&cli.BoolFlag{
			Name:    CLIContainerLabels,
			Value:   false,
			Usage:   "Enable runtime container labels in metrics.",
			EnvVars: []string{"DCGM_EXPORTER_CONTAINER_LABELS"},
		},
		&cli.StringFlag{
			Name:    CLIContainerRuntimeSocket,
			Value:   "",
			Usage:   "Path to a container runtime socket used for container labels.",
			EnvVars: []string{"DCGM_CONTAINER_RUNTIME_SOCKET"},
		},
		&cli.StringSliceFlag{
			Name:    CLINvidiaResourceNames,
			Value:   cli.NewStringSlice(),
			Usage:   "Nvidia resource names for specified GPU type like nvidia.com/a100, nvidia.com/a10.",
			EnvVars: []string{"NVIDIA_RESOURCE_NAMES"},
		},
		&cli.BoolFlag{
			Name:    CLIKubernetesVirtualGPUs,
			Value:   false,
			Usage:   "Capture metrics associated with virtual GPUs exposed by Kubernetes device plugins when using GPU sharing strategies, e.g. time-sharing or MPS.",
			EnvVars: []string{"KUBERNETES_VIRTUAL_GPUS"},
		},
		&cli.BoolFlag{
			Name:    CLIDumpEnabled,
			Value:   false,
			Usage:   "Enable file-based debugging dumps for troubleshooting",
			EnvVars: []string{"DCGM_EXPORTER_DUMP_ENABLED"},
		},
		&cli.StringFlag{
			Name:    CLIDumpDirectory,
			Value:   "/tmp/dcgm-exporter-debug",
			Usage:   "Directory to store debug dump files",
			EnvVars: []string{"DCGM_EXPORTER_DUMP_DIRECTORY"},
		},
		&cli.IntFlag{
			Name:    CLIDumpRetention,
			Value:   24,
			Usage:   "Retention period for debug dump files in hours (0 = no cleanup)",
			EnvVars: []string{"DCGM_EXPORTER_DUMP_RETENTION"},
		},
		&cli.BoolFlag{
			Name:    CLIDumpCompression,
			Value:   true,
			Usage:   "Use gzip compression for debug dump files",
			EnvVars: []string{"DCGM_EXPORTER_DUMP_COMPRESSION"},
		},
		&cli.BoolFlag{
			Name:    CLIKubernetesEnableDRA,
			Value:   false,
			Usage:   "Capture metrics associated with GPUs managed by Kubernetes Dynamic Resource Allocation (DRA) API.",
			EnvVars: []string{"KUBERNETES_ENABLE_DRA"},
		},
		&cli.BoolFlag{
			Name:    CLIDisableStartupValidate,
			Value:   false,
			Usage:   "Disable validation checks during startup. Can be useful for running in minimal environments or testing",
			EnvVars: []string{"DISABLE_STARTUP_VALIDATE"},
		},
		&cli.BoolFlag{
			Name:    CLIEnableGPUBindUnbindWatch,
			Value:   false,
			Usage:   "Enable watching for GPU bind/unbind events to trigger automatic reloads (requires DCGM 4.5+)",
			EnvVars: []string{"DCGM_EXPORTER_ENABLE_GPU_BIND_UNBIND_WATCH"},
		},
		&cli.StringFlag{
			Name:    CLIGPUBindUnbindPollInterval,
			Usage:   "Interval for polling GPU bind/unbind events (DCGM recommends 1s)",
			EnvVars: []string{"DCGM_EXPORTER_GPU_BIND_UNBIND_POLL_INTERVAL"},
			Value:   "1s",
		},
		&cli.BoolFlag{
			Name:    CLIEnablePprof,
			Value:   false,
			Usage:   "Enable /debug/pprof/ HTTP endpoints for profiling and debugging",
			EnvVars: []string{"DCGM_EXPORTER_ENABLE_PPROF"},
		},
	}

	if runtime.GOOS == "linux" {
		c.Flags = append(c.Flags, &cli.BoolFlag{
			Name:    CLIWebSystemdSocket,
			Value:   false,
			Usage:   "Use systemd socket activation listeners instead of port listeners (Linux only).",
			EnvVars: []string{"DCGM_EXPORTER_SYSTEMD_SOCKET"},
		})
	} else {
		err := "dcgm-exporter is only supported on Linux."
		slog.Error(err)
		fatal()
		return nil
	}

	c.Action = action

	return c
}

func fatal() {
	os.Exit(1)
}

func newOSWatcher(sigs ...os.Signal) (chan os.Signal, func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, sigs...)
	cleanup := func() {
		signal.Stop(sigChan)
		close(sigChan)
	}
	return sigChan, cleanup
}

func action(c *cli.Context) error {
	return stdout.Capture(context.Background(), func() (err error) {
		// The purpose of this function is to capture any panic that may occur
		// during initialization and return an error.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Encountered a failure.", slog.String(logging.StackTrace, string(debug.Stack())))
				err = fmt.Errorf("encountered a failure; err: %v", r)
			}
		}()
		return startDCGMExporter(c)
	})
}

func configureLogger(c *cli.Context) error {
	logFormat := c.String(CLILogFormat)
	logDebug := c.Bool(CLIDebugMode)
	var opts slog.HandlerOptions
	if logDebug {
		opts.Level = slog.LevelDebug
		defer slog.Debug("Debug output is enabled")
	}
	switch logFormat {
	case "text":
		logger := slog.New(slog.NewTextHandler(os.Stderr, &opts))
		slog.SetDefault(logger)
	case "json":
		// Use our custom JSON handler that properly handles complex structs
		logging.SetupGlobalLogger(os.Stderr, &opts)
	default:
		return fmt.Errorf("invalid %s parameter values: %s", CLILogFormat, logFormat)
	}
	return nil
}

// runDCGMExporter starts the exporter until lifecycleCtx is canceled.
// Reload requests trigger the same hot-reload path as SIGHUP in production.
func runDCGMExporter(lifecycleCtx context.Context, c *cli.Context, reloadRequests <-chan struct{}) error {
	if err := configureLogger(c); err != nil {
		return err
	}
	if lifecycleCtx == nil {
		return errors.New("lifecycle context is required")
	}

	var version string
	if c != nil && c.App != nil {
		version = c.App.Version
	}

	slog.Info("Starting dcgm-exporter", slog.String("Version", version))

	config, err := contextToConfig(c)
	if err != nil {
		return err
	}

	// Initialize DCGM Provider Instance (once)
	initializeDCGMProviderFunc(config)

	defer func() { dcgmprovider.Client().Cleanup() }()
	slog.Info("DCGM successfully initialized!")

	ctx := lifecycleCtx
	coord := initReloadCoordinator(c, config)
	watcherCtx, watcherCancel := context.WithCancel(lifecycleCtx)
	defer watcherCancel()
	var watcherWg sync.WaitGroup
	var gpuWatcher *restartableGPUWatcher

	// Register the lifecycle watch immediately after DCGM initialization. Trigger
	// safely queues any completion observed before later startup reads or before
	// the coordinator begins draining events.
	if config.EnableGPUBindUnbindWatch {
		bindUnbindWatcher := newGPUBindUnbindWatcherFunc(
			watcher.WithPollInterval(config.GPUBindUnbindPollInterval),
		)
		gpuWatcher = newRestartableGPUWatcher(bindUnbindWatcher.Start, func(state dcgm.BindUnbindEventState) {
			event, ok := reloadEventForGPUState(state)
			if !ok {
				return
			}
			coord.Trigger(event)
		})
		if err := gpuWatcher.Start(watcherCtx); err != nil {
			return fmt.Errorf("start GPU bind/unbind watcher: %w", err)
		}
		defer gpuWatcher.Stop()
		coord.setGPUWatcher(gpuWatcher)
	}

	// Initialize NVML as the preferred MIG profile-name source in every deployment mode.
	err = initializeNVMLProviderFunc()
	if err != nil {
		// Keep validated Kubernetes startup strict; other modes fall back to DCGM.
		if config.Kubernetes && !config.DisableStartupValidate {
			return err
		}
		slog.Warn("NVML provider unavailable; falling back to DCGM MIG profile names",
			slog.String("error", err.Error()))
	} else {
		slog.Info("NVML provider successfully initialized")
	}
	defer cleanupNVMLProviderFunc()

	// Seed DCP capability state only after the lifecycle cursor exists. Without
	// this snapshot, a CSV reload before the first GPU event could drop profiling
	// metrics.
	coord.queryDCPMetrics(config, 0)

	// Build initial registry
	initialRegistry, deviceWatchListManager, err := buildRegistryFunc(ctx, c, config)
	if err != nil {
		return err
	}
	cleanupRegistry := initialRegistry.Cleanup
	defer func() {
		cleanupRegistry()
	}()

	// Create metrics server (will run throughout entire lifecycle)
	metricsServer, serverCleanup, err := newMetricsServerFunc(config, deviceWatchListManager, initialRegistry)
	if err != nil {
		return err
	}
	defer serverCleanup()
	coord.setServer(metricsServer)
	cleanupRegistry = func() {
		currentRegistry := metricsServer.ClearRegistry()
		if currentRegistry != nil {
			currentRegistry.Cleanup()
		}
	}

	// Start HTTP server (runs continuously until shutdown signal)
	var serverWg sync.WaitGroup
	stop := make(chan interface{})

	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		metricsServer.Run(ctx, stop)
	}()

	slog.Info("HTTP server started - ready to serve metrics")

	// Start the coordinator after the server is ready. Any lifecycle completion
	// observed during startup is already queued by Trigger.
	watcherWg.Add(1)
	go func() {
		defer watcherWg.Done()
		coord.Run(watcherCtx)
	}()

	// File watcher (metric source changes) — trigger a config reload on change.
	// YAML config is startup-only; only a resolved CSV metric source is watched.
	if metricFile, ok := config.MetricFileWatcherPath(); ok {
		fileWatcher := newFileWatcherFunc(metricFile)
		runWatcher(watcherCtx, fileWatcher, func() {
			slog.Info("Metric file changed - triggering hot reload")
			coord.Trigger(evConfigChanged)
		}, &watcherWg)
	}

	// GPU bind/unbind watcher (optional) reports concrete DCGM lifecycle phases.
	if gpuWatcher != nil {
		runGPUWatcher(watcherCtx, gpuWatcher, &watcherWg)
	}

	// Wait for shutdown. Reload requests trigger the same handler as a CSV
	// change, matching SIGHUP production behavior.
	for {
		select {
		case <-lifecycleCtx.Done():
			slog.Info("Shutdown requested", slog.String("reason", lifecycleCtx.Err().Error()))
			goto shutdown
		case _, ok := <-reloadRequests:
			if !ok {
				reloadRequests = nil
				continue
			}
			slog.Info("Reload requested - triggering hot reload")
			coord.Trigger(evConfigChanged)
		}
	}

	// Graceful shutdown
shutdown:
	slog.Info("Shutting down gracefully...")

	// Stop watchers first
	watcherCancel()
	watcherWg.Wait()

	// Stop HTTP server
	close(stop)
	serverWg.Wait()

	slog.Info("Shutdown complete")
	return nil
}

// StartDCGMExporterWithSignalSource starts the exporter with injectable signal handling.
func StartDCGMExporterWithSignalSource(c *cli.Context, sigSource SignalSource) error {
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadRequests := make(chan struct{}, 1)
	if sigSource == nil {
		sigSource = NewOSSignalSource(syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	}
	defer sigSource.Cleanup()

	go func() {
		for {
			select {
			case sig, ok := <-sigSource.Signals():
				if !ok {
					return
				}
				slog.Info("Received signal", slog.String("signal", sig.String()))
				if sig == syscall.SIGHUP {
					slog.Info("SIGHUP received - triggering hot reload")
					queueReload(reloadRequests)
					continue
				}
				cancel()
				return
			case <-lifecycleCtx.Done():
				return
			}
		}
	}()

	return runDCGMExporter(lifecycleCtx, c, reloadRequests)
}

// startDCGMExporter starts the exporter with OS signal handling (production use).
func startDCGMExporter(c *cli.Context) error {
	return StartDCGMExporterWithSignalSource(c, nil)
}

// queueReload records one pending reload without blocking signal delivery.
func queueReload(reloadRequests chan<- struct{}) {
	select {
	case reloadRequests <- struct{}{}:
	default:
	}
}

// buildRegistry creates a new registry with current GPU topology.
// Called at: startup, hot reload (SIGHUP/file change), GPU bind event.
// Note: Does NOT query DCP metrics - caller must do this before calling.
func buildRegistry(ctx context.Context, _ *cli.Context, config *appconfig.Config) (*registry.Registry, devicewatchlistmanager.Manager, error) {
	slog.Info("Building registry for current GPU topology")

	cs, err := getCountersFunc(ctx, config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get counters: %w", err)
	}

	deviceWatchListManager, err := startWatchListManagerFunc(cs, config)
	if err != nil {
		return nil, nil, err
	}

	hostName := ""
	if !config.NoHostname {
		var err error
		hostName, err = getHostnameFunc(config)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get hostname: %w", err)
		}
	}

	cf := initCollectorFactoryFunc(cs, deviceWatchListManager, hostName, config)

	cRegistry := registry.NewRegistry()
	count := populateRegistry(cf, cRegistry)

	slog.Info("Registry built successfully", slog.Int("collector_count", count))

	return cRegistry, deviceWatchListManager, nil
}

// populateRegistry calls cf.NewCollectors exactly once and registers every
// returned collector. It returns the number of collectors registered.
//
// The exactly-once contract matters: each collector returned by NewCollectors
// installs DCGM field watches and GPU groups as a side effect, which are only
// released by (*registry.Registry).Cleanup once the collector is registered.
// A second call to NewCollectors without registering would leak DCGM
// resources, surfacing as "RemoveFieldWatch returned -16 (NOT_WATCHED)"
// warnings in nv-hostengine.log. This helper makes that contract both
// readable at the call site and testable in isolation.
func populateRegistry(cf collector.Factory, r *registry.Registry) int {
	collectors := cf.NewCollectors()
	for _, entityCollector := range collectors {
		r.Register(entityCollector)
	}
	return len(collectors)
}

// hotReloadCounter assigns a monotonically increasing ID to each reload for
// correlating logs across the reload paths.
var hotReloadCounter atomic.Uint64

// dcpCapabilities is an immutable snapshot of DCGM profiling capabilities
// discovered at runtime. Fields are unexported so the stored value cannot be
// mutated from outside this file; construct via newDCPCapabilities and read
// via applyTo.
//
// The snapshot exists because hot reload rebuilds an appconfig.Config from the
// CLI context, which cannot itself discover DCP capabilities — those come from
// DCGM and must be preserved across reloads. A fresh config with CollectDCP=true
// but MetricGroups=nil causes counters.fieldIsSupported to drop every
// DCGM_FI_PROF_* metric.
type dcpCapabilities struct {
	collectDCP   bool
	metricGroups []dcgm.MetricGroup
}

// newDCPCapabilities snapshots the DCP fields of c. The returned value owns
// its own copy of MetricGroups and nested FieldIds, so later mutation of c does
// not affect the snapshot.
func newDCPCapabilities(c *appconfig.Config) *dcpCapabilities {
	return &dcpCapabilities{
		collectDCP:   c.CollectDCP,
		metricGroups: cloneMetricGroups(c.MetricGroups),
	}
}

// applyTo overlays the snapshot onto c. c receives its own copy of the metric
// groups, so later mutation of c cannot reach back into the snapshot.
func (d *dcpCapabilities) applyTo(c *appconfig.Config) {
	c.CollectDCP = d.collectDCP
	c.MetricGroups = cloneMetricGroups(d.metricGroups)
}

// cloneMetricGroups deep-copies the slice and each element's FieldIds so that
// the caller and callee cannot share any mutable state.
func cloneMetricGroups(in []dcgm.MetricGroup) []dcgm.MetricGroup {
	if in == nil {
		return nil
	}
	out := make([]dcgm.MetricGroup, len(in))
	for i, g := range in {
		out[i] = g
		if g.FieldIds != nil {
			out[i].FieldIds = append([]uint(nil), g.FieldIds...)
		}
	}
	return out
}

// logTopologyInfo logs comprehensive information about the loaded GPU topology
func logTopologyInfo(reloadID uint64, deviceWatchListMgr devicewatchlistmanager.Manager, duration time.Duration) {
	var gpuCount, switchCount, cpuCount uint

	// Count GPUs
	if gpuWatchList, exists := deviceWatchListMgr.EntityWatchList(dcgm.FE_GPU); exists {
		gpuCount = gpuWatchList.DeviceInfo().GPUCount()
	}

	// Count Switches
	if switchWatchList, exists := deviceWatchListMgr.EntityWatchList(dcgm.FE_SWITCH); exists {
		switchCount = uint(len(switchWatchList.DeviceInfo().Switches()))
	}

	// Count CPUs
	if cpuWatchList, exists := deviceWatchListMgr.EntityWatchList(dcgm.FE_CPU); exists {
		cpuCount = uint(len(cpuWatchList.DeviceInfo().CPUs()))
	}

	slog.Info("System running with new topology",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("reload_duration", duration),
		slog.Uint64("gpus", uint64(gpuCount)),
		slog.Uint64("switches", uint64(switchCount)),
		slog.Uint64("cpus", uint64(cpuCount)))
}

// reloadEvent identifies one kind of reload work.
type reloadEvent int

const (
	evNone reloadEvent = iota
	evConfigChanged
	// evDRAResourceSliceChanged rebuilds the registry after a complete DRA pool topology changes.
	evDRAResourceSliceChanged
	// evDRAResourceSliceRetry is the one bounded replay for a failed
	// ResourceSlice-triggered registry build. A retry never schedules another retry.
	evDRAResourceSliceRetry
	evGPUReinitialized
	evGPURecoveryRetry
)

type gpuRecoveryStage int

const (
	gpuRecoveryIdle gpuRecoveryStage = iota
	gpuRecoveryRestartWatcher
	gpuRecoveryBuildRegistry
)

type pendingReload struct {
	configChanged bool
	// draResourceSliceEvent retains one DRA-triggered registry rebuild while other reload work is coalesced.
	draResourceSliceEvent reloadEvent
	latestGPUEvent        reloadEvent
}

func (p pendingReload) empty() bool {
	return !p.configChanged && p.draResourceSliceEvent == evNone && p.latestGPUEvent == evNone
}

// reloadCoordinator serializes config and GPU lifecycle work.
type reloadCoordinator struct {
	server       *server.MetricsServer
	c            *cli.Context
	reloadConfig *appconfig.Config

	pendingMu sync.Mutex
	pending   pendingReload
	wake      chan struct{}

	// Owned by the Run goroutine.
	dcp *dcpCapabilities

	// Existing test seams for config application and registry construction.
	applyConfigReload func(ctx context.Context, cfg *appconfig.Config, reloadID uint64)
	buildRegistry     func(ctx context.Context, c *cli.Context, cfg *appconfig.Config) (*registry.Registry, devicewatchlistmanager.Manager, error)
	cleanupDCGM       func()
	initializeDCGM    func(cfg *appconfig.Config)
	cleanupNVML       func()
	initializeNVML    func() error
	gpuWatcher        gpuWatcherLifecycle
	scheduleRetry     func(context.Context, time.Duration, func()) context.CancelFunc

	// Owned by the Run goroutine. Recovery retries resume at the failed stage
	// so DCGM and NVML are not repeatedly torn down while the coordinator
	// remains free to process config reloads and shutdown.
	gpuRecoveryStage       gpuRecoveryStage
	gpuRecoveryConfig      *appconfig.Config
	gpuRecoveryStartedAt   time.Time
	gpuRecoveryRetryDelay  time.Duration
	cancelGPURecoveryRetry context.CancelFunc
}

const (
	lifecycleRetryInitial = time.Second
	lifecycleRetryMax     = 30 * time.Second
)

func newReloadCoordinator(c *cli.Context) *reloadCoordinator {
	r := &reloadCoordinator{
		c:    c,
		wake: make(chan struct{}, 1),
	}
	r.applyConfigReload = func(ctx context.Context, cfg *appconfig.Config, reloadID uint64) {
		r.doConfigReload(ctx, cfg, reloadID)
	}
	r.buildRegistry = buildRegistryFunc
	r.cleanupDCGM = func() { dcgmprovider.Client().Cleanup() }
	r.initializeDCGM = initializeDCGMProviderFunc
	r.cleanupNVML = cleanupNVMLProviderFunc
	r.initializeNVML = initializeNVMLProviderFunc
	r.scheduleRetry = scheduleAfter
	r.gpuRecoveryRetryDelay = lifecycleRetryInitial
	return r
}

func scheduleAfter(ctx context.Context, delay time.Duration, callback func()) context.CancelFunc {
	retryCtx, cancel := context.WithCancel(ctx)
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-retryCtx.Done():
		case <-timer.C:
			callback()
		}
	}()
	return cancel
}

func nextLifecycleRetryDelay(delay time.Duration) time.Duration {
	delay *= 2
	if delay > lifecycleRetryMax {
		return lifecycleRetryMax
	}
	return delay
}

// setServer installs the metrics server before Run starts.
func (r *reloadCoordinator) setServer(s *server.MetricsServer) {
	r.server = s
}

// setGPUWatcher gives lifecycle resets control of the GPU watcher.
func (r *reloadCoordinator) setGPUWatcher(w gpuWatcherLifecycle) {
	r.gpuWatcher = w
}

func initReloadCoordinator(c *cli.Context, config *appconfig.Config) *reloadCoordinator {
	coord := newReloadCoordinator(c)
	config.SetDRAResourceSliceChangeCallback(func() {
		coord.Trigger(evDRAResourceSliceChanged)
	})
	coord.reloadConfig = config.Clone()
	return coord
}

// Trigger coalesces work into the bounded pending snapshot.
func (r *reloadCoordinator) Trigger(ev reloadEvent) {
	r.pendingMu.Lock()
	switch ev {
	case evConfigChanged:
		r.pending.configChanged = true
	case evDRAResourceSliceChanged:
		r.pending.draResourceSliceEvent = evDRAResourceSliceChanged
	case evDRAResourceSliceRetry:
		if r.pending.draResourceSliceEvent == evNone {
			r.pending.draResourceSliceEvent = evDRAResourceSliceRetry
		}
	case evGPUReinitialized:
		r.pending.latestGPUEvent = evGPUReinitialized
	case evGPURecoveryRetry:
		if r.pending.latestGPUEvent == evNone {
			r.pending.latestGPUEvent = evGPURecoveryRetry
		}
	}
	r.pendingMu.Unlock()

	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *reloadCoordinator) takePending() pendingReload {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()

	pending := r.pending
	r.pending = pendingReload{}
	return pending
}

func (r *reloadCoordinator) handlePending(ctx context.Context, pending pendingReload) {
	// A completed physical lifecycle is safe to reset. The watcher deliberately
	// suppresses the earlier reinitializing state because its topology is not
	// ready for exporter reinitialization yet.
	// A new lifecycle reset rereads the metric source and consumes a coalesced
	// config or DRA change. Retry events do not: registry-only reloads remain
	// serviceable while recovery is waiting on a watcher or registry dependency.
	if pending.latestGPUEvent != evGPUReinitialized {
		switch {
		case pending.draResourceSliceEvent != evNone:
			// Both registry-only paths use the same config snapshot. Prioritize DRA
			// work so a failed topology rebuild retains its one bounded retry.
			r.handle(ctx, pending.draResourceSliceEvent)
		case pending.configChanged:
			r.handle(ctx, evConfigChanged)
		}
	}
	if pending.latestGPUEvent == evGPUReinitialized || pending.latestGPUEvent == evGPURecoveryRetry {
		r.handle(ctx, pending.latestGPUEvent)
	}
}

// Run drains pending work while preserving the latest GPU lifecycle state.
func (r *reloadCoordinator) Run(ctx context.Context) {
	defer r.cancelRecoveryRetry()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			pending := r.takePending()
			if pending.empty() {
				continue
			}
			r.handlePending(ctx, pending)
		}
	}
}

// handle runs one event with panic recovery so later pending work can still run.
func (r *reloadCoordinator) handle(ctx context.Context, ev reloadEvent) {
	reloadID := hotReloadCounter.Add(1)

	defer func() {
		if p := recover(); p != nil {
			stackBuf := make([]byte, 8192)
			n := runtime.Stack(stackBuf, false)
			slog.ErrorContext(ctx, "PANIC RECOVERED in reload",
				slog.String("panic_value", fmt.Sprintf("%v", p)),
				slog.String("panic_type", fmt.Sprintf("%T", p)),
				slog.Uint64("reload_id", reloadID),
				slog.String("stack_trace", string(stackBuf[:n])))
			if (ev == evGPUReinitialized || ev == evGPURecoveryRetry) &&
				r.gpuRecoveryStage != gpuRecoveryIdle && r.gpuRecoveryConfig != nil && ctx.Err() == nil {
				r.scheduleRecoveryRetry(
					ctx,
					reloadID,
					"GPU lifecycle recovery panicked",
					fmt.Errorf("panic: %v", p),
				)
			} else if ev == evDRAResourceSliceChanged && ctx.Err() == nil {
				// Match the bounded error path: the first DRA-triggered attempt gets
				// one replay even when registry construction panics.
				r.Trigger(evDRAResourceSliceRetry)
			}
		}
	}()

	r.server.SetReloadInProgress(true)
	defer func() {
		if r.gpuRecoveryStage == gpuRecoveryIdle {
			r.server.SetReloadInProgress(false)
		}
	}()

	switch ev {
	case evConfigChanged, evDRAResourceSliceChanged, evDRAResourceSliceRetry:
		cfg, err := r.buildReloadConfig()
		if err != nil {
			slog.ErrorContext(ctx, "Failed to build reload config",
				slog.Uint64("reload_id", reloadID),
				slog.String("error", err.Error()))
			return
		}
		if ev == evConfigChanged {
			if r.gpuRecoveryStage != gpuRecoveryIdle {
				// A later recovery retry must not replace this reload with the
				// older snapshot captured when the GPU event began.
				r.gpuRecoveryConfig = cfg.Clone()
			}
			r.applyConfigReload(ctx, cfg, reloadID)
			return
		}
		if !r.doConfigReload(ctx, cfg, reloadID) && ev == evDRAResourceSliceChanged {
			// One immediate, coalesced retry covers a transient registry build
			// failure without creating an unbounded or tight retry loop.
			r.Trigger(evDRAResourceSliceRetry)
		}
	case evGPUReinitialized:
		r.doGPULifecycleReset(ctx, reloadID)
	case evGPURecoveryRetry:
		r.retryGPULifecycleRecovery(ctx, reloadID)
	}
}

func (r *reloadCoordinator) buildReloadConfig() (*appconfig.Config, error) {
	if r.reloadConfig != nil {
		cfg := r.reloadConfig.Clone()
		if r.dcp != nil {
			r.dcp.applyTo(cfg)
		}
		return cfg, nil
	}

	return nil, fmt.Errorf("buildReloadConfig: no startup config snapshot; coordinator was not initialized via initReloadCoordinator")
}

// doConfigReload preserves the last-good registry until its replacement exists.
// It reports whether a replacement was installed so DRA topology reloads can
// schedule their single bounded retry after a transient build failure.
func (r *reloadCoordinator) doConfigReload(ctx context.Context, cfg *appconfig.Config, reloadID uint64) bool {
	slog.InfoContext(ctx, "Hot reload triggered - building new registry",
		slog.Uint64("reload_id", reloadID))
	startTime := time.Now()

	newRegistry, deviceWatchListMgr, err := r.buildRegistry(ctx, r.c, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build new registry during hot reload",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()),
			slog.String("metrics_state", "preserving last-good registry"))
		return false
	}

	oldRegistry := r.server.SwapMetricsRuntime(newRegistry, deviceWatchListMgr)
	if oldRegistry != nil {
		oldRegistry.Cleanup()
	}

	duration := time.Since(startTime)
	slog.InfoContext(ctx, "Hot reload complete",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("reload_duration", duration))
	logTopologyInfo(reloadID, deviceWatchListMgr, duration)
	return true
}

func (r *reloadCoordinator) clearMetricsRegistry() {
	oldRegistry := r.server.ClearRegistry()
	if oldRegistry != nil {
		oldRegistry.Cleanup()
	}
}

// doGPULifecycleReset replaces provider and registry state after a completed GPU bind or unbind.
func (r *reloadCoordinator) doGPULifecycleReset(ctx context.Context, reloadID uint64) {
	slog.InfoContext(ctx, "GPU lifecycle event detected - resetting providers",
		slog.Uint64("reload_id", reloadID))
	r.gpuRecoveryStartedAt = time.Now()
	r.cancelRecoveryRetry()
	r.gpuRecoveryStage = gpuRecoveryIdle
	r.gpuRecoveryConfig = nil
	r.gpuRecoveryRetryDelay = lifecycleRetryInitial

	cfg, err := r.buildReloadConfig()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build GPU lifecycle reload config",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
		return
	}

	r.dcp = &dcpCapabilities{}

	// Stop the watcher before releasing the DCGM resources it uses.
	watcherStopped := false
	if r.gpuWatcher != nil {
		r.gpuWatcher.Stop()
		watcherStopped = true
		defer func() {
			if watcherStopped {
				if err := r.gpuWatcher.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.ErrorContext(ctx, "Failed to restore GPU watcher after interrupted lifecycle reset",
						slog.Uint64("reload_id", reloadID),
						slog.String("error", err.Error()))
				}
			}
		}()
	}

	slog.InfoContext(ctx, "Clearing registry - metrics remain empty during GPU lifecycle reset",
		slog.Uint64("reload_id", reloadID))
	r.clearMetricsRegistry()

	// Release providers in reverse startup order, then bring DCGM back first.
	slog.InfoContext(ctx, "Cleaning up exporter NVML resources",
		slog.Uint64("reload_id", reloadID))
	r.cleanupNVML()

	slog.InfoContext(ctx, "Cleaning up DCGM resources",
		slog.Uint64("reload_id", reloadID))
	r.cleanupDCGM()

	slog.InfoContext(ctx, "Reinitializing DCGM",
		slog.Uint64("reload_id", reloadID))
	r.initializeDCGM(cfg)
	r.gpuRecoveryConfig = cfg
	if watcherStopped {
		r.gpuRecoveryStage = gpuRecoveryRestartWatcher
		watcherStopped = false
	} else {
		r.prepareRegistryRecovery(ctx, cfg, reloadID)
	}
	r.continueGPULifecycleRecovery(ctx, reloadID)
}

func (r *reloadCoordinator) prepareRegistryRecovery(ctx context.Context, cfg *appconfig.Config, reloadID uint64) {
	// Match startup scope: refresh optional NVML state on every lifecycle reset.
	if err := r.initializeNVML(); err != nil {
		level := slog.LevelWarn
		if cfg.Kubernetes && !cfg.DisableStartupValidate {
			level = slog.LevelError
		}
		slog.LogAttrs(ctx, level, "Failed to reinitialize NVML",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
	}

	r.queryDCPMetrics(cfg, reloadID)
	r.gpuRecoveryStage = gpuRecoveryBuildRegistry
}

func (r *reloadCoordinator) retryGPULifecycleRecovery(ctx context.Context, reloadID uint64) {
	r.cancelRecoveryRetry()
	if r.gpuRecoveryStage == gpuRecoveryIdle || r.gpuRecoveryConfig == nil {
		return
	}
	r.continueGPULifecycleRecovery(ctx, reloadID)
}

func (r *reloadCoordinator) continueGPULifecycleRecovery(ctx context.Context, reloadID uint64) {
	if ctx.Err() != nil || r.gpuRecoveryConfig == nil {
		return
	}

	if r.gpuRecoveryStage == gpuRecoveryRestartWatcher {
		if err := r.gpuWatcher.Start(ctx); err != nil {
			if ctx.Err() == nil {
				r.scheduleRecoveryRetry(ctx, reloadID, "Failed to restart GPU watcher after lifecycle reset", err)
			}
			return
		}
		r.prepareRegistryRecovery(ctx, r.gpuRecoveryConfig, reloadID)
	}

	if r.gpuRecoveryStage != gpuRecoveryBuildRegistry {
		return
	}

	// Rebuild metrics only after the providers reflect the new topology. A
	// failed attempt schedules another coordinator event without blocking
	// config reloads or repeating provider teardown.
	newRegistry, deviceWatchListMgr, err := r.buildRegistry(ctx, r.c, r.gpuRecoveryConfig)
	if err != nil {
		r.scheduleRecoveryRetry(ctx, reloadID, "Failed to build registry after GPU lifecycle reset", err)
		return
	}
	if ctx.Err() != nil {
		newRegistry.Cleanup()
		return
	}

	oldRegistry := r.server.SwapMetricsRuntime(newRegistry, deviceWatchListMgr)
	if oldRegistry != nil {
		oldRegistry.Cleanup()
	}

	duration := time.Since(r.gpuRecoveryStartedAt)
	slog.InfoContext(ctx, "GPU metrics resumed after lifecycle reset",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("reload_duration", duration))
	logTopologyInfo(reloadID, deviceWatchListMgr, duration)
	r.finishGPURecovery()
}

func (r *reloadCoordinator) scheduleRecoveryRetry(
	ctx context.Context,
	reloadID uint64,
	message string,
	err error,
) {
	delay := r.gpuRecoveryRetryDelay
	slog.ErrorContext(ctx, message,
		slog.Uint64("reload_id", reloadID),
		slog.String("error", err.Error()),
		slog.Duration("retry_delay", delay),
		slog.String("metrics_state", "recovery pending"))
	r.cancelRecoveryRetry()
	r.cancelGPURecoveryRetry = r.scheduleRetry(ctx, delay, func() {
		r.Trigger(evGPURecoveryRetry)
	})
	r.gpuRecoveryRetryDelay = nextLifecycleRetryDelay(delay)
}

func (r *reloadCoordinator) cancelRecoveryRetry() {
	if r.cancelGPURecoveryRetry != nil {
		r.cancelGPURecoveryRetry()
		r.cancelGPURecoveryRetry = nil
	}
}

func (r *reloadCoordinator) finishGPURecovery() {
	r.cancelRecoveryRetry()
	r.gpuRecoveryStage = gpuRecoveryIdle
	r.gpuRecoveryConfig = nil
	r.gpuRecoveryRetryDelay = lifecycleRetryInitial
	r.server.SetReloadInProgress(false)
}

func startDeviceWatchListManager(
	cs *counters.CounterSet, config *appconfig.Config,
) (devicewatchlistmanager.Manager, error) {
	// Create a list containing DCGM Collector, Exp Collectors and all the label Collectors
	var allCounters counters.CounterList
	var deviceWatchListManager devicewatchlistmanager.Manager

	allCounters = append(allCounters, cs.DCGMCounters...)

	allCounters = appendDCGMXIDErrorsDependency(allCounters, cs)
	allCounters = appendDCGMClockEventsDependency(cs, allCounters)

	if err := devicewatchlistmanager.ValidateWatchGroups(allCounters, config.WatchGroups); err != nil {
		return nil, err
	}

	deviceWatchListManager = devicewatchlistmanager.NewWatchListManager(allCounters, config)
	deviceWatcher := devicewatcher.NewDeviceWatcher()

	for _, deviceType := range devicewatchlistmanager.DeviceTypesToWatch {
		err := deviceWatchListManager.CreateEntityWatchList(deviceType, deviceWatcher, int64(config.CollectInterval))
		if err != nil {
			slog.Info(fmt.Sprintf("Not collecting %s metrics; %s", deviceType.String(), err))
		}
	}
	return deviceWatchListManager, nil
}

func containsDCGMField(slice []counters.Counter, fieldID dcgm.Short) bool {
	return slices.ContainsFunc(slice, func(counter counters.Counter) bool {
		return uint16(counter.FieldID) == uint16(fieldID)
	})
}

func containsExporterField(slice []counters.Counter, fieldID counters.ExporterCounter) bool {
	return slices.ContainsFunc(slice, func(counter counters.Counter) bool {
		return uint16(counter.FieldID) == uint16(fieldID)
	})
}

// appendDCGMClockEventsDependency appends DCGM counters required for clock event exporter metrics.
func appendDCGMClockEventsDependency(
	cs *counters.CounterSet, allCounters []counters.Counter,
) []counters.Counter {
	if len(cs.ExporterCounters) > 0 {
		if (containsExporterField(cs.ExporterCounters, counters.DCGMClockEventsCount) ||
			containsExporterField(cs.ExporterCounters, counters.DCGMClockEventsTotal)) &&
			!containsDCGMField(allCounters, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS) {
			allCounters = append(allCounters,
				counters.Counter{
					FieldID:   dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
					FieldName: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS",
				})
		}
	}
	return allCounters
}

// appendDCGMXIDErrorsDependency appends DCGM counters required for XID exporter metrics.
func appendDCGMXIDErrorsDependency(
	allCounters []counters.Counter, cs *counters.CounterSet,
) []counters.Counter {
	if len(cs.ExporterCounters) > 0 {
		if (containsExporterField(cs.ExporterCounters, counters.DCGMXIDErrorsCount) ||
			containsExporterField(cs.ExporterCounters, counters.DCGMXIDErrorsTotal)) &&
			!containsDCGMField(allCounters, dcgm.DCGM_FI_DEV_XID_ERRORS) {
			allCounters = append(allCounters,
				counters.Counter{
					FieldID:   dcgm.DCGM_FI_DEV_XID_ERRORS,
					FieldName: "DCGM_FI_DEV_XID_ERRORS",
				})
		}
	}
	return allCounters
}

func getCounters(ctx context.Context, config *appconfig.Config) (*counters.CounterSet, error) {
	cs, err := counters.GetCounterSet(ctx, config)
	if err != nil {
		return nil, err
	}

	// Copy labels from DCGM Counters to ExporterCounters
	for i := range cs.DCGMCounters {
		if cs.DCGMCounters[i].PromType == "label" {
			cs.ExporterCounters = append(cs.ExporterCounters, cs.DCGMCounters[i])
		}
	}
	return cs, nil
}

// queryDCPMetrics queries DCGM for supported profiling metric groups,
// mutates cfg to reflect the result, and publishes the resulting snapshot to
// r.dcp so later config reloads can apply it without re-querying (which can
// segfault during GPU state transitions).
//
// Called at: startup (from runDCGMExporter before the
// first buildRegistry) and from doGPULifecycleReset after DCGM is reinitialized.
// Config reloads do NOT call this; they apply r.dcp instead.
//
// Single deferred epilogue: recover from any profiling API panic, then
// publish whatever DCP state remains in cfg. Explicit sequencing within one
// defer guarantees that error and panic paths publish a disabled snapshot
// — overwriting any prior enabled snapshot — without depending on LIFO
// declaration order.
func (r *reloadCoordinator) queryDCPMetrics(cfg *appconfig.Config, reloadID uint64) {
	slog.Debug("Querying DCGM profiling metric groups", slog.Uint64("reload_id", reloadID))

	defer func() {
		if p := recover(); p != nil {
			slog.Warn("Profiling API panic - DCP metrics disabled",
				slog.Uint64("reload_id", reloadID),
				slog.String("panic", fmt.Sprintf("%v", p)))
			cfg.CollectDCP = false
			cfg.MetricGroups = nil
		}
		r.dcp = newDCPCapabilities(cfg)
	}()

	groups, err := dcgmprovider.Client().GetSupportedMetricGroups(0)
	if err != nil {
		cfg.CollectDCP = false
		cfg.MetricGroups = nil
		slog.Info("Not collecting DCP metrics: " + err.Error())
		return
	}

	gpuModel := "unknown"
	if gpuCount, err := dcgmprovider.Client().GetAllDeviceCount(); err == nil && gpuCount > 0 {
		if gpuInfo, err := dcgmprovider.Client().GetDeviceInfo(0); err == nil {
			gpuModel = gpuInfo.Identifiers.Model
		}
	}

	slog.Info("Successfully queried DCGM profiling metric groups",
		slog.Uint64("reload_id", reloadID),
		slog.Int("count", len(groups)),
		slog.String("gpu_model", gpuModel))

	cfg.MetricGroups = groups
	cfg.CollectDCP = true
}

// parseDeviceOptions parses the flex, major, minor, and combined selector grammar used by device flags.
func parseDeviceOptions(devices string) (appconfig.DeviceOptions, error) {
	var dOpt appconfig.DeviceOptions

	parts := strings.Split(devices, "+")
	for _, part := range parts {
		if part == "" {
			return dOpt, fmt.Errorf("invalid device option '%s': empty selector", devices)
		}

		letter, rangeSpec, hasRange := strings.Cut(part, ":")
		if strings.Contains(rangeSpec, ":") {
			return dOpt, fmt.Errorf("invalid ranged device option '%s': there can only be one specified range", devices)
		}

		switch letter {
		case FlexKey:
			if len(parts) > 1 {
				return dOpt, fmt.Errorf("the flex option 'f' cannot be combined with other device options")
			}
			if hasRange {
				return dOpt, fmt.Errorf("no range can be specified with the flex option 'f'")
			}
			dOpt.Flex = true
		case MajorKey:
			if dOpt.MajorRange != nil {
				return dOpt, fmt.Errorf("duplicate device option '%s'", MajorKey)
			}
			indices, err := parseDeviceRange(rangeSpec, hasRange, int(dcgm.MAX_NUM_CPU_CORES))
			if err != nil {
				return dOpt, err
			}
			dOpt.MajorRange = indices
		case MinorKey:
			if dOpt.MinorRange != nil {
				return dOpt, fmt.Errorf("duplicate device option '%s'", MinorKey)
			}
			indices, err := parseDeviceRange(rangeSpec, hasRange, int(dcgm.MAX_NUM_CPU_CORES))
			if err != nil {
				return dOpt, err
			}
			dOpt.MinorRange = indices
		default:
			return dOpt, fmt.Errorf("the only valid options are 'f', 'g', or 'i', but found '%s'", letter)
		}
	}

	return dOpt, nil
}

// parseDeviceRange expands an optional comma-separated selector range into device indices.
func parseDeviceRange(rangeSpec string, hasRange bool, limit int) ([]int, error) {
	if !hasRange {
		// No range means all present devices of the type.
		if limit < 1 {
			return nil, fmt.Errorf("device selector expands to more than %d indices", dcgm.MAX_NUM_CPU_CORES)
		}
		return []int{-1}, nil
	}

	var indices []int
	numbers := strings.Split(rangeSpec, ",")
	for _, numberOrRange := range numbers {
		rangeTokens := strings.Split(numberOrRange, "-")
		rangeTokenCount := len(rangeTokens)
		switch rangeTokenCount {
		case 1:
			number, err := strconv.Atoi(rangeTokens[0])
			if err != nil {
				return nil, err
			}
			if len(indices) >= limit {
				return nil, fmt.Errorf("device selector expands to more than %d indices", dcgm.MAX_NUM_CPU_CORES)
			}
			indices = append(indices, number)
		case 2:
			start, err := strconv.Atoi(rangeTokens[0])
			if err != nil {
				return nil, err
			}
			end, err := strconv.Atoi(rangeTokens[1])
			if err != nil {
				return nil, err
			}

			if start > end {
				return nil, fmt.Errorf("invalid range '%s': start (%d) must not exceed end (%d)", numberOrRange, start, end)
			}

			// Add the range to the indices.
			for i := start; i <= end; i++ {
				if len(indices) >= limit {
					return nil, fmt.Errorf("device selector expands to more than %d indices", dcgm.MAX_NUM_CPU_CORES)
				}
				indices = append(indices, i)
			}
		default:
			return nil, fmt.Errorf("range can only be '<number>-<number>', but found '%s'", numberOrRange)
		}
	}

	return indices, nil
}

func contextToConfig(c *cli.Context) (*appconfig.Config, error) {
	config, err := defaultConfig()
	if err != nil {
		return nil, err
	}

	if c.IsSet(CLIConfigFile) {
		config.ConfigFile = c.String(CLIConfigFile)
		yamlConfig, err := appconfig.LoadYAMLConfigFile(config.ConfigFile)
		if err != nil {
			return nil, err
		}
		if err := yamlConfig.ApplyTo(config); err != nil {
			return nil, err
		}
	}

	if err := applyExplicitConfigOverrides(c, config); err != nil {
		return nil, err
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

// defaultConfig builds the runtime defaults used before YAML and explicit CLI overrides are applied.
func defaultConfig() (*appconfig.Config, error) {
	gOpt, err := parseDeviceOptions(FlexKey)
	if err != nil {
		return nil, err
	}
	sOpt, err := parseDeviceOptions(FlexKey)
	if err != nil {
		return nil, err
	}
	cOpt, err := parseDeviceOptions(FlexKey)
	if err != nil {
		return nil, err
	}

	return &appconfig.Config{
		CollectorsFile:                   appconfig.DefaultCollectorsFile,
		Address:                          ":9400",
		CollectInterval:                  30000,
		WatchRetention:                   appconfig.DefaultWatchRetention(),
		Kubernetes:                       false,
		KubernetesEnablePodLabels:        false,
		KubernetesEnablePodUID:           false,
		KubernetesGPUIdType:              appconfig.GPUUID,
		KubernetesPodLabelAllowlistRegex: nil,
		CollectDCP:                       true,
		UseOldNamespace:                  false,
		UseRemoteHE:                      false,
		RemoteHEInfo:                     "localhost:5555",
		GPUDeviceOptions:                 gOpt,
		SwitchDeviceOptions:              sOpt,
		CPUDeviceOptions:                 cOpt,
		HealthRequireGPUs:                false,
		NoHostname:                       false,
		UseFakeGPUs:                      false,
		ConfigMapData:                    undefinedConfigMapData,
		MetricSource: appconfig.MetricSource{
			Kind: appconfig.MetricSourceFile,
			File: appconfig.DefaultCollectorsFile,
		},
		WebSystemdSocket:           false,
		WebConfigFile:              "",
		WebReadTimeout:             appconfig.DefaultWebReadTimeout,
		WebWriteTimeout:            appconfig.DefaultWebWriteTimeout,
		MaxConcurrentScrapes:       appconfig.DefaultMaxConcurrentScrapes,
		EnableExporterMetrics:      false,
		XIDCountWindowSize:         int((5 * time.Minute).Milliseconds()),
		ReplaceBlanksInModelName:   false,
		Debug:                      false,
		ClockEventsCountWindowSize: int((5 * time.Minute).Milliseconds()),
		EnableDCGMLog:              false,
		DCGMLogLevel:               DCGMDbgLvlNone,
		PodResourcesKubeletSocket:  "/var/lib/kubelet/pod-resources/kubelet.sock",
		HPCJobMappingDir:           "",
		ContainerLabels:            false,
		ContainerRuntimeSocket:     "",
		NvidiaResourceNames:        nil,
		KubernetesVirtualGPUs:      false,
		DumpConfig: appconfig.DumpConfig{
			Enabled:     false,
			Directory:   "/tmp/dcgm-exporter-debug",
			Retention:   24,
			Compression: true,
		},
		KubernetesEnableDRA:       false,
		DisableStartupValidate:    false,
		EnableGPUBindUnbindWatch:  false,
		GPUBindUnbindPollInterval: time.Second,
		EnablePprof:               false,
	}, nil
}

// applyExplicitConfigOverrides applies only startup flags and env vars that were explicitly set.
func applyExplicitConfigOverrides(c *cli.Context, config *appconfig.Config) error {
	if c.IsSet(CLIFieldsFile) {
		applyMetricFileSource(config, c.String(CLIFieldsFile))
	}
	if c.IsSet(CLIAddress) {
		config.Address = c.String(CLIAddress)
	}
	if c.IsSet(CLICollectInterval) {
		config.CollectInterval = c.Int(CLICollectInterval)
	}
	if c.IsSet(CLIWatchMaxKeepAge) {
		config.WatchRetention.MaxAge = c.Duration(CLIWatchMaxKeepAge)
	}
	if c.IsSet(CLIWatchMaxKeepSamples) {
		config.WatchRetention.MaxSamples = c.Int64(CLIWatchMaxKeepSamples)
	}
	if c.IsSet(CLIKubernetes) {
		config.Kubernetes = c.Bool(CLIKubernetes)
	}
	if c.IsSet(CLIKubernetesEnablePodLabels) {
		config.KubernetesEnablePodLabels = c.Bool(CLIKubernetesEnablePodLabels)
	}
	if c.IsSet(CLIKubernetesEnablePodUID) {
		config.KubernetesEnablePodUID = c.Bool(CLIKubernetesEnablePodUID)
	}
	if c.IsSet(CLIKubernetesGPUIDType) {
		config.KubernetesGPUIdType = appconfig.KubernetesGPUIDType(c.String(CLIKubernetesGPUIDType))
	}
	if c.IsSet(CLIKubernetesPodLabelAllowlistRegex) {
		config.KubernetesPodLabelAllowlistRegex = c.StringSlice(CLIKubernetesPodLabelAllowlistRegex)
	}
	if c.IsSet(CLIUseOldNamespace) {
		config.UseOldNamespace = c.Bool(CLIUseOldNamespace)
	}
	if c.IsSet(CLIRemoteHEInfo) {
		config.UseRemoteHE = true
		config.RemoteHEInfo = c.String(CLIRemoteHEInfo)
	}
	if c.IsSet(CLIGPUDevices) {
		opt, err := parseDeviceOptions(c.String(CLIGPUDevices))
		if err != nil {
			return err
		}
		config.GPUDeviceOptions = opt
	}
	if c.IsSet(CLISwitchDevices) {
		opt, err := parseDeviceOptions(c.String(CLISwitchDevices))
		if err != nil {
			return err
		}
		config.SwitchDeviceOptions = opt
	}
	if c.IsSet(CLICPUDevices) {
		opt, err := parseDeviceOptions(c.String(CLICPUDevices))
		if err != nil {
			return err
		}
		config.CPUDeviceOptions = opt
	}
	if c.IsSet(CLIHealthRequireGPUs) {
		config.HealthRequireGPUs = c.Bool(CLIHealthRequireGPUs)
	}
	if c.IsSet(CLINoHostname) {
		config.NoHostname = c.Bool(CLINoHostname)
	}
	if c.IsSet(CLIUseFakeGPUs) {
		config.UseFakeGPUs = c.Bool(CLIUseFakeGPUs)
	}
	if c.IsSet(CLIConfigMapData) {
		if err := applyConfigMapDataSource(config, c.String(CLIConfigMapData)); err != nil {
			return err
		}
	}
	if c.IsSet(CLIWebSystemdSocket) {
		config.WebSystemdSocket = c.Bool(CLIWebSystemdSocket)
	}
	if c.IsSet(CLIWebConfigFile) {
		config.WebConfigFile = c.String(CLIWebConfigFile)
	}
	if c.IsSet(CLIWebReadTimeout) {
		config.WebReadTimeout = parseDuration(c.String(CLIWebReadTimeout), appconfig.DefaultWebReadTimeout)
	}
	if c.IsSet(CLIWebWriteTimeout) {
		config.WebWriteTimeout = parseDuration(c.String(CLIWebWriteTimeout), appconfig.DefaultWebWriteTimeout)
	}
	if c.IsSet(CLIMaxConcurrentScrapes) {
		config.MaxConcurrentScrapes = c.Int(CLIMaxConcurrentScrapes)
	}
	if c.IsSet(CLIEnableExporterMetrics) {
		config.EnableExporterMetrics = c.Bool(CLIEnableExporterMetrics)
	}
	if c.IsSet(CLIXIDCountWindowSize) {
		config.XIDCountWindowSize = c.Int(CLIXIDCountWindowSize)
	}
	if c.IsSet(CLIReplaceBlanksInModelName) {
		config.ReplaceBlanksInModelName = c.Bool(CLIReplaceBlanksInModelName)
	}
	if c.IsSet(CLIDebugMode) {
		config.Debug = c.Bool(CLIDebugMode)
	}
	if c.IsSet(CLIClockEventsCountWindowSize) {
		config.ClockEventsCountWindowSize = c.Int(CLIClockEventsCountWindowSize)
	}
	if c.IsSet(CLIEnableDCGMLog) {
		config.EnableDCGMLog = c.Bool(CLIEnableDCGMLog)
	}
	if c.IsSet(CLIDCGMLogLevel) {
		config.DCGMLogLevel = c.String(CLIDCGMLogLevel)
	}
	if c.IsSet(CLIPodResourcesKubeletSocket) {
		config.PodResourcesKubeletSocket = c.String(CLIPodResourcesKubeletSocket)
	}
	if c.IsSet(CLIHPCJobMappingDir) {
		config.HPCJobMappingDir = c.String(CLIHPCJobMappingDir)
	}
	if c.IsSet(CLIContainerLabels) {
		config.ContainerLabels = c.Bool(CLIContainerLabels)
	}
	if c.IsSet(CLIContainerRuntimeSocket) {
		config.ContainerRuntimeSocket = c.String(CLIContainerRuntimeSocket)
	}
	if c.IsSet(CLINvidiaResourceNames) {
		config.NvidiaResourceNames = c.StringSlice(CLINvidiaResourceNames)
	}
	if c.IsSet(CLIKubernetesVirtualGPUs) {
		config.KubernetesVirtualGPUs = c.Bool(CLIKubernetesVirtualGPUs)
	}
	if c.IsSet(CLIDumpEnabled) {
		config.DumpConfig.Enabled = c.Bool(CLIDumpEnabled)
	}
	if c.IsSet(CLIDumpDirectory) {
		config.DumpConfig.Directory = c.String(CLIDumpDirectory)
	}
	if c.IsSet(CLIDumpRetention) {
		config.DumpConfig.Retention = c.Int(CLIDumpRetention)
	}
	if c.IsSet(CLIDumpCompression) {
		config.DumpConfig.Compression = c.Bool(CLIDumpCompression)
	}
	if c.IsSet(CLIKubernetesEnableDRA) {
		config.KubernetesEnableDRA = c.Bool(CLIKubernetesEnableDRA)
	}
	if c.IsSet(CLIDisableStartupValidate) {
		config.DisableStartupValidate = c.Bool(CLIDisableStartupValidate)
	}
	if c.IsSet(CLIEnableGPUBindUnbindWatch) {
		config.EnableGPUBindUnbindWatch = c.Bool(CLIEnableGPUBindUnbindWatch)
	}
	if c.IsSet(CLIGPUBindUnbindPollInterval) {
		config.GPUBindUnbindPollInterval = parseDuration(c.String(CLIGPUBindUnbindPollInterval), time.Second)
	}
	if c.IsSet(CLIEnablePprof) {
		config.EnablePprof = c.Bool(CLIEnablePprof)
	}

	return nil
}

// applyMetricFileSource selects a file-backed metric source and clears compatibility ConfigMap data.
func applyMetricFileSource(config *appconfig.Config, file string) {
	config.CollectorsFile = file
	config.ConfigMapData = undefinedConfigMapData
	config.MetricSource = appconfig.MetricSource{
		Kind: appconfig.MetricSourceFile,
		File: file,
	}
}

// applyConfigMapDataSource selects the compatibility API-backed ConfigMap metric source.
func applyConfigMapDataSource(config *appconfig.Config, configMapData string) error {
	config.ConfigMapData = configMapData
	if configMapData == "" || configMapData == undefinedConfigMapData {
		applyMetricFileSource(config, config.CollectorsFile)
		return nil
	}

	parts := strings.Split(configMapData, ":")
	if len(parts) != 2 {
		return fmt.Errorf("malformed configmap-data %q", configMapData)
	}
	config.MetricSource = appconfig.MetricSource{
		Kind: appconfig.MetricSourceConfigMap,
		ConfigMap: appconfig.ConfigMapMetricSource{
			Namespace: parts[0],
			Name:      parts[1],
		},
	}
	return nil
}

// validateConfig checks cross-field runtime requirements after all config sources are applied.
func validateConfig(config *appconfig.Config) error {
	if config.MaxConcurrentScrapes <= 0 {
		return fmt.Errorf(
			"invalid %s parameter value: %d, must be greater than 0",
			CLIMaxConcurrentScrapes,
			config.MaxConcurrentScrapes,
		)
	}

	if err := config.WatchRetention.Validate(); err != nil {
		return fmt.Errorf("invalid field watch retention: %w", err)
	}
	for i, watchGroup := range config.WatchGroups {
		if err := watchGroup.Retention.Resolve(config.WatchRetention).Validate(); err != nil {
			return fmt.Errorf("invalid watch group %q retention at index %d: %w", watchGroup.Name, i, err)
		}
	}
	if !slices.Contains(DCGMDbgLvlValues, config.DCGMLogLevel) {
		return fmt.Errorf("invalid %s parameter value: %s", CLIDCGMLogLevel, config.DCGMLogLevel)
	}
	if config.EnableGPUBindUnbindWatch && config.GPUBindUnbindPollInterval <= 0 {
		return fmt.Errorf(
			"invalid %s parameter value: %q, must be greater than 0",
			CLIGPUBindUnbindPollInterval,
			config.GPUBindUnbindPollInterval.String(),
		)
	}
	if config.EnablePprof && strings.TrimSpace(config.WebConfigFile) == "" {
		return fmt.Errorf(
			"%s requires %s so profiling endpoints are protected by exporter-toolkit auth or TLS",
			CLIEnablePprof,
			CLIWebConfigFile,
		)
	}
	if config.ContainerLabels && config.Kubernetes {
		slog.Warn(
			"container runtime labels are ignored when kubernetes mode is enabled",
			slog.String("flag", CLIContainerLabels),
			slog.String("mode", CLIKubernetes),
		)
	}
	if config.ContainerLabels && !config.Kubernetes && strings.TrimSpace(config.ContainerRuntimeSocket) == "" {
		return fmt.Errorf(
			"%s requires %s or DCGM_CONTAINER_RUNTIME_SOCKET",
			CLIContainerLabels,
			CLIContainerRuntimeSocket,
		)
	}

	return nil
}

// parseDuration parses a duration string and returns the parsed duration.
// If parsing fails, returns the default value.
func parseDuration(s string, defaultValue time.Duration) time.Duration {
	if s == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Warn("Failed to parse duration, using default",
			slog.String("input", s),
			slog.Duration("default", defaultValue),
			slog.String("error", err.Error()))
		return defaultValue
	}
	return d
}

// runWatcher starts a file watcher in a goroutine and manages its lifecycle.
func runWatcher(ctx context.Context, w watcher.Watcher, onChange func(), wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := w.Watch(ctx, onChange)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Watcher failed", slog.String("error", err.Error()))
		}
	}()
}

func reloadEventForGPUState(state dcgm.BindUnbindEventState) (reloadEvent, bool) {
	switch state {
	case dcgm.DcgmBUEventStateSystemReinitializationCompleted:
		return evGPUReinitialized, true
	default:
		return evNone, false
	}
}
