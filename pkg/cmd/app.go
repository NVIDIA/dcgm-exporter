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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/prerequisites"
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
	CLINoHostname                       = "no-hostname"
	CLIUseFakeGPUs                      = "fake-gpus"
	CLIConfigMapData                    = "configmap-data"
	CLIWebSystemdSocket                 = "web-systemd-socket"
	CLIWebConfigFile                    = "web-config-file"
	CLIWebReadTimeout                   = "web-read-timeout"
	CLIWebWriteTimeout                  = "web-write-timeout"
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
	validatePrerequisitesFunc   = prerequisites.Validate
	initializeDCGMProviderFunc  = dcgmprovider.Initialize
	initializeNVMLProviderFunc  = nvmlprovider.Initialize
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
			Name:    CLICollectInterval,
			Aliases: []string{"c"},
			Value:   30000,
			Usage:   "Interval of time at which point metrics are collected. Unit is milliseconds (ms).",
			EnvVars: []string{"DCGM_EXPORTER_INTERVAL"},
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

	// Validate prerequisites once
	if !config.DisableStartupValidate {
		err = validatePrerequisitesFunc()
		if err != nil {
			return err
		}
	}

	// Initialize DCGM Provider Instance (once)
	initializeDCGMProviderFunc(config)

	// Create cleanup function that calls the CURRENT provider's Cleanup method
	// This is critical to avoid closure capture bugs when reinitializing DCGM
	// during GPU bind/unbind cycles.
	dcgmCleanup := func() {
		dcgmprovider.Client().Cleanup()
	}

	// NOTE: dcgmCleanup is managed by GPU topology change handler if GPU watching is enabled
	// Otherwise, defer cleanup for normal shutdown
	if !config.EnableGPUBindUnbindWatch {
		defer dcgmCleanup()
	}

	// Initialize NVML Provider Instance only if Kubernetes mode is enabled
	// NVML is only needed for MIG device UUID parsing in Kubernetes environments
	if config.Kubernetes {
		err = initializeNVMLProviderFunc()
		if err != nil && !config.DisableStartupValidate {
			return err
		}
		defer nvmlprovider.Client().Cleanup()
		slog.Info("NVML provider successfully initialized for Kubernetes MIG support")
	} else {
		slog.Info("NVML provider skipped (not running in Kubernetes mode)")
	}

	slog.Info("DCGM successfully initialized!")

	ctx := lifecycleCtx

	// Construct and seed the reload coordinator in one step. The seeding
	// call is load-bearing: without it, coord.dcp stays nil until the first
	// GPU topology event and a CSV reload in that window would drop profiling
	// metrics. Extracted as its own function so tests can assert the
	// seeding happens without driving the full startup pipeline.
	coord := initReloadCoordinator(c, dcgmCleanup, config)

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

	// Start watchers
	watcherCtx, watcherCancel := context.WithCancel(lifecycleCtx)
	var watcherWg sync.WaitGroup

	// Reload coordinator goroutine. Must be started BEFORE watchers so
	// early-fired events are not lost.
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

	// GPU bind/unbind watcher (optional) — trigger a topology change.
	if config.EnableGPUBindUnbindWatch {
		gpuWatcher := newGPUWatcherLifecycle(watcherCtx, func() *watcher.GPUBindUnbindWatcher {
			return newGPUBindUnbindWatcherFunc(
				watcher.WithPollInterval(config.GPUBindUnbindPollInterval),
			)
		}, func() {
			slog.DebugContext(watcherCtx, "GPU topology change detected")
			coord.Trigger(evTopologyChanged)
		})
		coord.setGPUWatcher(gpuWatcher)
		runGPUWatcher(gpuWatcher, &watcherWg)
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

	// If GPU watching is enabled, cleanup DCGM manually (not deferred)
	if config.EnableGPUBindUnbindWatch {
		slog.Info("Cleaning up DCGM on shutdown")
		dcgmCleanup()
	}

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

// reloadEvent is the mailbox payload. Values are ordered: higher ordinals
// dominate lower ones when both are pending, via the CAS-upgrade in Trigger.
type reloadEvent int32

const (
	evNone            reloadEvent = -1 // sentinel for an empty mailbox
	evConfigChanged   reloadEvent = 0  // CSV file change OR SIGHUP — same handler
	evTopologyChanged reloadEvent = 1  // strongest; dominates the mailbox
)

// String satisfies fmt.Stringer for readable logs and test names.
func (e reloadEvent) String() string {
	switch e {
	case evNone:
		return "none"
	case evConfigChanged:
		return "configChanged"
	case evTopologyChanged:
		return "topologyChanged"
	default:
		return fmt.Sprintf("reloadEvent(%d)", int32(e))
	}
}

// reloadCoordinator owns all reload state. Every piece of that state is
// either written only by producers via the documented Trigger path (the
// mailbox) or accessed only from the Run goroutine (dcp and the apply
// seams). No atomic dances, no in-progress flag races, no replay loops.
//
// Single-threaded ownership replaces the previous atomics-plus-ordering
// defenses: a topology event arriving during a config reload simply updates
// the mailbox; the coordinator picks it up at the start of the next loop
// iteration with no "too soon" rate-limit to defeat.
type reloadCoordinator struct {
	server       *server.MetricsServer
	c            *cli.Context
	dcgmCleanup  func()
	reloadConfig *appconfig.Config

	// mailbox holds the strongest pending event, or evNone if empty. Writers
	// CAS-upgrade; the consumer swaps it out at the start of each iteration.
	mailbox atomic.Int32
	// wake is a single-slot signal. Multiple Triggers between consumptions
	// collapse to one wakeup; a spurious wake (mailbox empty at swap) is
	// harmless.
	wake chan struct{}

	// Single-goroutine-owned state (accessed only from Run).
	dcp *dcpCapabilities

	// Test seams — default to the real implementations. Both receive the
	// already-constructed reload config so tests can inspect exactly what
	// the rebuild would use without driving the full DCGM stack.
	applyConfigReload   func(ctx context.Context, cfg *appconfig.Config, reloadID uint64)
	applyTopologyChange func(ctx context.Context, reloadID uint64)
	buildRegistry       func(ctx context.Context, c *cli.Context, cfg *appconfig.Config) (*registry.Registry, devicewatchlistmanager.Manager, error)
	initializeDCGM      func(cfg *appconfig.Config)
	gpuWatcher          gpuWatcherLifecycle
}

// newReloadCoordinator builds a coordinator ready to Trigger. Call setServer
// and seed DCP via queryDCPMetrics before Run.
func newReloadCoordinator(c *cli.Context, dcgmCleanup func()) *reloadCoordinator {
	r := &reloadCoordinator{
		c:           c,
		dcgmCleanup: dcgmCleanup,
		wake:        make(chan struct{}, 1),
	}
	r.mailbox.Store(int32(evNone))
	r.applyConfigReload = r.doConfigReload
	r.applyTopologyChange = r.doTopologyChange
	r.buildRegistry = buildRegistryFunc
	r.initializeDCGM = initializeDCGMProviderFunc
	return r
}

// setServer installs the metrics server. Must be called before Run; not safe
// to call concurrently with Run.
func (r *reloadCoordinator) setServer(s *server.MetricsServer) { r.server = s }

// setGPUWatcher installs the optional GPU watcher lifecycle. Must be called
// before Run; not safe to call concurrently with Run.
func (r *reloadCoordinator) setGPUWatcher(w gpuWatcherLifecycle) { r.gpuWatcher = w }

// initReloadCoordinator constructs a reload coordinator and seeds its DCP
// capabilities in one step, which is the load-bearing startup sequence.
// Extracted from runDCGMExporter so TestInitReloadCoordinator
// can assert that startup seeding actually happens, instead of relying on
// a test that manually calls queryDCPMetrics.
func initReloadCoordinator(c *cli.Context, dcgmCleanup func(), config *appconfig.Config) *reloadCoordinator {
	coord := newReloadCoordinator(c, dcgmCleanup)
	coord.reloadConfig = config.Clone()
	coord.queryDCPMetrics(config, 0)
	return coord
}

// Trigger is safe to call from any goroutine. O(1), non-blocking, zero
// allocation. CAS-upgrades the mailbox to max(current, ev) so a stronger
// event is never lost behind weaker ones.
func (r *reloadCoordinator) Trigger(ev reloadEvent) {
	for {
		cur := reloadEvent(r.mailbox.Load())
		if cur != evNone && cur >= ev {
			// Mailbox already holds an equal-or-stronger event; don't
			// downgrade. Still signal in case the consumer missed it.
			break
		}
		if r.mailbox.CompareAndSwap(int32(cur), int32(ev)) {
			break
		}
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run processes events serially until ctx is cancelled.
func (r *reloadCoordinator) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			ev := reloadEvent(r.mailbox.Swap(int32(evNone)))
			if ev == evNone {
				continue // spurious wake
			}
			r.handle(ctx, ev)
		}
	}
}

// handle dispatches a single event. Sets the server's reload-in-progress
// flag so /health reports accurately, and recovers from handler panics so
// the coordinator outlives them.
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
		}
	}()

	r.server.SetReloadInProgress(true)
	defer r.server.SetReloadInProgress(false)

	switch ev {
	case evConfigChanged:
		cfg, err := r.buildReloadConfig()
		if err != nil {
			slog.ErrorContext(ctx, "Failed to build reload config",
				slog.Uint64("reload_id", reloadID),
				slog.String("error", err.Error()))
			return
		}
		r.applyConfigReload(ctx, cfg, reloadID)
	case evTopologyChanged:
		r.applyTopologyChange(ctx, reloadID)
	}
}

// buildReloadConfig returns a fresh appconfig.Config for reload paths by cloning
// the startup snapshot so startup-only YAML is not re-read.
// The latest DCP capabilities are also overlaid: config reloads consume that
// snapshot directly, while topology reloads overwrite it by re-querying DCP
// after DCGM is stable.
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

// doConfigReload rebuilds the registry after a CSV file change or SIGHUP.
// During rebuild, /metrics continues serving the last-good registry until a
// replacement has been built successfully. Does NOT reset DCGM — it reuses the
// DCP capabilities most recently discovered during startup or a topology
// change.
func (r *reloadCoordinator) doConfigReload(ctx context.Context, cfg *appconfig.Config, reloadID uint64) {
	slog.InfoContext(ctx, "Hot reload triggered - building new registry",
		slog.Uint64("reload_id", reloadID))
	startTime := time.Now()

	newRegistry, deviceWatchListMgr, err := r.buildRegistry(ctx, r.c, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build new registry during hot reload",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()),
			slog.String("metrics_state", "preserving last-good registry"))
		return
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
}

// doTopologyChange handles a GPU bind/unbind/swap event: reuse the startup
// config snapshot, cleanup old registry, reset DCGM, re-query DCP, rebuild.
// Works for all scenarios:
//   - GPU unbind: cleanup succeeds, reinit fails (no GPU), /metrics returns empty
//   - GPU bind: cleanup succeeds, reinit succeeds, /metrics serves new GPU
//   - GPU swap: cleanup succeeds, reinit succeeds with new GPU
func (r *reloadCoordinator) doTopologyChange(ctx context.Context, reloadID uint64) {
	slog.InfoContext(ctx, "GPU topology change detected - full reset",
		slog.Uint64("reload_id", reloadID))

	cfg, err := r.buildReloadConfig()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build topology reload config",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
		return
	}

	// Pessimistically invalidate the DCP snapshot. The hardware topology is
	// about to change; any capabilities published for the previous topology
	// must not survive into a subsequent config reload. queryDCPMetrics
	// below republishes on success; if we return early or a later step
	// panics, the next config reload sees a disabled snapshot rather than
	// stale capabilities from the pre-change hardware.
	r.dcp = &dcpCapabilities{}

	if r.gpuWatcher != nil {
		r.gpuWatcher.Stop()
	}

	slog.InfoContext(ctx, "Clearing registry - /metrics will return empty during reset",
		slog.Uint64("reload_id", reloadID))
	oldRegistry := r.server.ClearRegistry()
	if oldRegistry != nil {
		oldRegistry.Cleanup()
	}

	slog.InfoContext(ctx, "Cleaning up DCGM resources",
		slog.Uint64("reload_id", reloadID))
	r.dcgmCleanup()

	slog.InfoContext(ctx, "Reinitializing DCGM",
		slog.Uint64("reload_id", reloadID))
	r.initializeDCGM(cfg)
	if r.gpuWatcher != nil {
		r.gpuWatcher.Start()
	}

	if cfg.Kubernetes && cfg.KubernetesVirtualGPUs {
		slog.InfoContext(ctx, "Cleaning up NVML resources", slog.Uint64("reload_id", reloadID))
		nvmlprovider.Client().Cleanup()

		slog.InfoContext(ctx, "Reinitializing NVML", slog.Uint64("reload_id", reloadID))
		if err := nvmlprovider.Initialize(); err != nil {
			slog.ErrorContext(ctx, "Failed to reinitialize NVML",
				slog.Uint64("reload_id", reloadID),
				slog.String("error", err.Error()))
		}
	}

	// Safe to re-query DCP: the GPU is stable after a topology change.
	r.queryDCPMetrics(cfg, reloadID)

	slog.InfoContext(ctx, "Building registry for current GPU topology",
		slog.Uint64("reload_id", reloadID))

	startTime := time.Now()
	newRegistry, deviceWatchListMgr, err := r.buildRegistry(ctx, r.c, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build registry",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
		return
	}

	r.server.SwapMetricsRuntime(newRegistry, deviceWatchListMgr)
	duration := time.Since(startTime)

	slog.InfoContext(ctx, "GPU topology change complete",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("total_time", duration))

	logTopologyInfo(reloadID, deviceWatchListMgr, duration)
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
// first buildRegistry) and from doTopologyChange after DCGM reinitialises.
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

// runGPUWatcher runs the managed GPU bind/unbind watcher lifecycle.
func runGPUWatcher(w *gpuWatcherLifecycleController, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.Run()
	}()
}

type gpuWatcherLifecycle interface {
	Start()
	Stop()
}

type gpuWatcherLifecycleController struct {
	ctx        context.Context
	newWatcher func() *watcher.GPUBindUnbindWatcher
	onChange   func()

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newGPUWatcherLifecycle(
	ctx context.Context,
	newWatcher func() *watcher.GPUBindUnbindWatcher,
	onChange func(),
) *gpuWatcherLifecycleController {
	return &gpuWatcherLifecycleController{
		ctx:        ctx,
		newWatcher: newWatcher,
		onChange:   onChange,
	}
}

func (g *gpuWatcherLifecycleController) Run() {
	g.Start()
	<-g.ctx.Done()
	g.Stop()
}

func (g *gpuWatcherLifecycleController) Start() {
	select {
	case <-g.ctx.Done():
		return
	default:
	}

	g.mu.Lock()
	if g.cancel != nil {
		g.mu.Unlock()
		return
	}

	watchCtx, cancel := context.WithCancel(g.ctx)
	done := make(chan struct{})
	g.cancel = cancel
	g.done = done
	w := g.newWatcher()
	g.mu.Unlock()

	go func() {
		defer close(done)
		err := w.Watch(watchCtx, g.onChange)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(watchCtx, "GPU watcher failed", slog.String("error", err.Error()))
		}
	}()
}

func (g *gpuWatcherLifecycleController) Stop() {
	g.mu.Lock()
	cancel := g.cancel
	done := g.done
	g.cancel = nil
	g.done = nil
	g.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-done
}
