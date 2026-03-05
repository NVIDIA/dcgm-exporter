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
	undefinedConfigMapData = "none"
	deviceUsageTemplate    = `Specify which devices dcgm-exporter monitors.
	Possible values: {{.FlexKey}} or 
	                 {{.MajorKey}}[:id1[,-id2...] or 
	                 {{.MinorKey}}[:id1[,-id2...].
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

	NOTE 1: -i cannot be specified unless MIG mode is enabled.
	NOTE 2: Any time indices are specified, those indices must exist on the system.
	NOTE 3: In MIG mode, only -f or -i with a range can be specified. GPUs are not assigned to pods
		and therefore reporting must occur at the GPU instance level.`
)

const (
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
	CLIXIDCountWindowSize               = "xid-count-window-size"
	CLIReplaceBlanksInModelName         = "replace-blanks-in-model-name"
	CLIDebugMode                        = "debug"
	CLIClockEventsCountWindowSize       = "clock-events-count-window-size"
	CLIEnableDCGMLog                    = "enable-dcgm-log"
	CLIDCGMLogLevel                     = "dcgm-log-level"
	CLILogFormat                        = "log-format"
	CLIPodResourcesKubeletSocket        = "pod-resources-kubelet-socket"
	CLIHPCJobMappingDir                 = "hpc-job-mapping-dir"
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
	DeviceUsageStr := deviceUsageBuffer.String()

	c.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    CLIFieldsFile,
			Aliases: []string{"f"},
			Usage:   "Path to the file, that contains the DCGM fields to collect",
			Value:   "/etc/dcgm-exporter/default-counters.csv",
			EnvVars: []string{"DCGM_EXPORTER_COLLECTORS"},
		},
		&cli.StringFlag{
			Name:    CLIAddress,
			Aliases: []string{"a"},
			Value:   ":9400",
			Usage:   "Address",
			EnvVars: []string{"DCGM_EXPORTER_LISTEN"},
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
			Usage:   DeviceUsageStr,
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
			Usage:   "Connect to remote hostengine at <HOST>:<PORT>",
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
			Usage:   DeviceUsageStr,
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
			Usage:   DeviceUsageStr,
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
			Usage:   "Set time window size in milliseconds (ms) for counting active XID errors in DCGM Exporter.",
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
			Usage:   "Set time window size in milliseconds (ms) for counting clock events in DCGM Exporter.",
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

	c.Action = func(c *cli.Context) error {
		return action(c)
	}

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

func action(c *cli.Context) (err error) {
	return stdout.Capture(context.Background(), func() error {
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

// StartDCGMExporterWithSignalSource starts the exporter with a custom signal source.
// This variant allows dependency injection for testing.
func StartDCGMExporterWithSignalSource(c *cli.Context, sigSource SignalSource) error {
	if err := configureLogger(c); err != nil {
		return err
	}

	// Use OS signals if not provided (production path)
	if sigSource == nil {
		sigSource = NewOSSignalSource(syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	}
	defer sigSource.Cleanup()

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
		err = prerequisites.Validate()
		if err != nil {
			return err
		}
	}

	// Initialize DCGM Provider Instance (once)
	dcgmprovider.Initialize(config)

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
		err = nvmlprovider.Initialize()
		if err != nil && !config.DisableStartupValidate {
			return err
		}
		defer nvmlprovider.Client().Cleanup()
		slog.Info("NVML provider successfully initialized for Kubernetes MIG support")
	} else {
		slog.Info("NVML provider skipped (not running in Kubernetes mode)")
	}

	slog.Info("DCGM successfully initialized!")

	ctx := context.Background()

	// Query DCGM profiling metrics at startup
	// This is re-queried on every hot reload to handle GPU changes
	queryDCPMetrics(config, 0)

	// Build initial registry
	initialRegistry, deviceWatchListManager, err := buildRegistry(ctx, c, config)
	if err != nil {
		return err
	}
	defer initialRegistry.Cleanup()

	// Create metrics server (will run throughout entire lifecycle)
	metricsServer, serverCleanup, err := server.NewMetricsServer(config, deviceWatchListManager, initialRegistry)
	if err != nil {
		return err
	}
	defer serverCleanup()

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
	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	var watcherWg sync.WaitGroup

	// File watcher (config changes) - hot reload on change
	fileWatcher := watcher.NewFileWatcher(config.CollectorsFile)
	runWatcher(watcherCtx, fileWatcher, func() {
		slog.Info("Config file changed - triggering hot reload")
		if err := hotReload(watcherCtx, metricsServer, c, dcgmCleanup); err != nil {
			slog.Error("Hot reload failed", slog.String("error", err.Error()))
		}
	}, &watcherWg)

	// GPU bind/unbind watcher (optional) - handles GPU topology changes
	if config.EnableGPUBindUnbindWatch {
		gpuWatcher := watcher.NewGPUBindUnbindWatcher(
			watcher.WithPollInterval(config.GPUBindUnbindPollInterval),
		)
		runGPUWatcher(watcherCtx, gpuWatcher, metricsServer, c, dcgmCleanup, &watcherWg)
	}

	// Wait for shutdown signal (SIGTERM, SIGINT) - ignore SIGHUP for compatibility
	sigs := sigSource.Signals()
	for {
		sig := <-sigs
		slog.Info("Received signal", slog.String("signal", sig.String()))

		if sig == syscall.SIGHUP {
			// SIGHUP triggers hot reload instead of full restart
			slog.Info("SIGHUP received - triggering hot reload")
			if err := hotReload(watcherCtx, metricsServer, c, dcgmCleanup); err != nil {
				slog.Error("Hot reload failed", slog.String("error", err.Error()))
			}
			continue
		}

		// SIGTERM/SIGINT/SIGQUIT - graceful shutdown
		break
	}

	// Graceful shutdown
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

// startDCGMExporter starts the exporter with OS signal handling (production use).
func startDCGMExporter(c *cli.Context) error {
	return StartDCGMExporterWithSignalSource(c, nil)
}

// buildRegistry creates a new registry with current GPU topology.
// Called at: startup, hot reload (SIGHUP/file change), GPU bind event.
// Note: Does NOT query DCP metrics - caller must do this before calling.
func buildRegistry(ctx context.Context, _ *cli.Context, config *appconfig.Config) (*registry.Registry, devicewatchlistmanager.Manager, error) {
	slog.Info("Building registry for current GPU topology")

	cs := getCounters(ctx, config)

	deviceWatchListManager := startDeviceWatchListManager(cs, config)

	hostName, err := hostname.GetHostname(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get hostname: %w", err)
	}

	cf := collector.InitCollectorFactory(cs, deviceWatchListManager, hostName, config)

	cRegistry := registry.NewRegistry()
	for _, entityCollector := range cf.NewCollectors() {
		cRegistry.Register(entityCollector)
	}

	slog.Info("Registry built successfully",
		slog.Int("collector_count", len(cf.NewCollectors())))

	return cRegistry, deviceWatchListManager, nil
}

var (
	hotReloadCounter  atomic.Uint64
	lastReloadTime    atomic.Int64
	minReloadInterval = 2 * time.Second // Prevent rapid successive reloads while allowing reasonably fast recovery

	// Pending event tracking for GPU topology changes that occur during hot reload
	pendingGPUTopologyChange atomic.Bool
)

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

// processPendingEvents checks for and executes any pending GPU topology change events
// that were queued while a reload was in progress.
// Returns true if an event was processed, false otherwise.
func processPendingEvents(ctx context.Context, server *server.MetricsServer, c *cli.Context, dcgmCleanup func()) bool {
	if pendingGPUTopologyChange.Load() {
		pendingGPUTopologyChange.Store(false)
		slog.Info("Processing queued GPU topology change event")
		handleGPUTopologyChange(ctx, server, c, dcgmCleanup)
		return true
	}

	return false
}

// hotReload rebuilds the registry when configuration file changes (SIGHUP or file watcher).
// During rebuild, /metrics returns empty responses (HTTP 200, no metrics) for 2-3 seconds.
// Note: Does NOT reset DCGM connection (unlike handleGPUTopologyChange which does full reset).
func hotReload(ctx context.Context, server *server.MetricsServer, c *cli.Context, dcgmCleanup func()) (err error) {
	// Panic recovery for hot reload - critical to prevent exporter crash
	defer func() {
		if r := recover(); r != nil {
			// Capture stack trace for debugging
			stackBuf := make([]byte, 8192)
			stackSize := runtime.Stack(stackBuf, false)
			stack := string(stackBuf[:stackSize])

			// Log comprehensive panic information
			slog.Error("PANIC RECOVERED in hotReload",
				slog.String("panic_value", fmt.Sprintf("%v", r)),
				slog.String("panic_type", fmt.Sprintf("%T", r)),
				slog.Uint64("reload_id", hotReloadCounter.Load()),
				slog.String("stack_trace", stack))

			err = fmt.Errorf("hot reload panic: %v", r)
		}
	}()

	// Safeguard 1: Check if reload is already in progress
	if server.IsReloadInProgress() {
		slog.Warn("Hot reload already in progress - ignoring duplicate request")
		return nil
	}

	// Safeguard 2: Rate limiting - prevent rapid successive reloads
	now := time.Now()
	last := time.Unix(lastReloadTime.Load(), 0)
	timeSinceLast := now.Sub(last)

	if timeSinceLast < minReloadInterval {
		slog.Warn("Hot reload rate limited - too soon after previous reload",
			slog.Duration("time_since_last", timeSinceLast),
			slog.Duration("min_interval", minReloadInterval))
		return nil
	}

	reloadID := hotReloadCounter.Add(1)
	lastReloadTime.Store(now.Unix())
	startTime := time.Now()

	slog.Info("Hot reload triggered - building new registry in background",
		slog.Uint64("reload_id", reloadID))

	server.SetReloadInProgress(true)
	defer server.SetReloadInProgress(false)

	config, err := contextToConfig(c)
	if err != nil {
		return fmt.Errorf("failed to read config during hot reload: %w", err)
	}

	// Step 1: Cleanup old registry (ensures only one registry exists at a time)
	slog.Info("Clearing registry - /metrics will return empty until rebuild completes",
		slog.Uint64("reload_id", reloadID))
	oldRegistry := server.ClearRegistry()
	if oldRegistry != nil {
		slog.Debug("Waiting for in-flight /metrics requests to complete",
			slog.Uint64("reload_id", reloadID))
		oldRegistry.Cleanup() // Waits up to 2 seconds for active scrapes
	}

	// Step 2: Build new registry with current GPU topology
	slog.Info("Building new registry with updated GPU topology", slog.Uint64("reload_id", reloadID))

	// Note: DCP metrics are NOT re-queried during hot reload (use startup config)
	// This avoids profiling API segfaults during GPU state changes
	slog.Debug("Using DCP metrics from startup (not re-querying)",
		slog.Uint64("reload_id", reloadID))

	newRegistry, deviceWatchListMgr, err := buildRegistry(ctx, c, config)
	if err != nil {
		return fmt.Errorf("failed to build new registry during hot reload: %w", err)
	}

	// Step 3: Activate new registry (/metrics now serves GPU metrics again)
	slog.Info("Activating new registry - /metrics now serves updated GPU metrics",
		slog.Uint64("reload_id", reloadID))
	server.SetRegistry(newRegistry)
	duration := time.Since(startTime)

	slog.Info("Hot reload complete",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("downtime", duration))

	logTopologyInfo(reloadID, deviceWatchListMgr, duration)

	// Step 4: Process any GPU bind/unbind events that were queued during this reload
	// This ensures we don't miss hardware topology changes
	if processPendingEvents(ctx, server, c, dcgmCleanup) {
		slog.Info("Processed queued GPU event after hot reload completion",
			slog.Uint64("reload_id", reloadID))
	}

	return nil
}

// handleGPUTopologyChange handles any GPU topology change (bind, unbind, or hardware swap).
// It performs a full cleanup → reinitialize → rebuild cycle, ensuring system is always in sync.
// This unified approach works for all scenarios:
//   - GPU unbind: cleanup succeeds, reinit fails (no GPU), /metrics returns empty
//   - GPU bind: cleanup succeeds, reinit succeeds, /metrics serves new GPU
//   - GPU swap: cleanup succeeds, reinit succeeds with new GPU, /metrics serves new GPU
func handleGPUTopologyChange(ctx context.Context, server *server.MetricsServer, c *cli.Context, dcgmCleanup func()) {
	reloadID := hotReloadCounter.Add(1)

	slog.InfoContext(ctx, "GPU topology change detected - full reset",
		slog.Uint64("reload_id", reloadID))

	// Safeguard: Rate limiting to prevent reload thrashing
	lastReload := time.Unix(0, lastReloadTime.Load())
	if time.Since(lastReload) < minReloadInterval {
		slog.WarnContext(ctx, "Ignoring topology change - too soon after last reload",
			slog.Uint64("reload_id", reloadID),
			slog.Duration("time_since_last", time.Since(lastReload)))
		return
	}
	lastReloadTime.Store(time.Now().UnixNano())

	// Safeguard: Don't start if reload already in progress - queue the event instead
	if server.IsReloadInProgress() {
		slog.WarnContext(ctx, "Reload in progress - queuing topology change event",
			slog.Uint64("reload_id", reloadID))
		pendingGPUTopologyChange.Store(true)
		return
	}
	server.SetReloadInProgress(true)
	defer server.SetReloadInProgress(false)

	// Step 1: Cleanup old registry (wait for in-flight scrapes)
	slog.InfoContext(ctx, "Clearing registry - /metrics will return empty during reset",
		slog.Uint64("reload_id", reloadID))
	oldRegistry := server.ClearRegistry()
	if oldRegistry != nil {
		oldRegistry.Cleanup()
	}

	// Step 2: Cleanup DCGM completely (release all GPU resources)
	slog.InfoContext(ctx, "Cleaning up DCGM resources",
		slog.Uint64("reload_id", reloadID))
	dcgmCleanup()

	// Step 3: Reinitialize DCGM from scratch
	// This will succeed if GPU is present, fail gracefully if not
	config, err := contextToConfig(c)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to read config",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
		return
	}

	slog.InfoContext(ctx, "Reinitializing DCGM",
		slog.Uint64("reload_id", reloadID))
	dcgmprovider.Initialize(config)

	// Step 4: Query DCP metrics (safe now - GPU is stable after topology change)
	queryDCPMetrics(config, reloadID)

	// Step 5: Build new registry with current GPU topology
	// This will create empty registry if no GPUs present
	slog.InfoContext(ctx, "Building registry for current GPU topology",
		slog.Uint64("reload_id", reloadID))

	startTime := time.Now()
	newRegistry, deviceWatchListMgr, err := buildRegistry(ctx, c, config)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build registry",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
		// Keep registry as nil - /metrics will return empty
		return
	}

	// Step 6: Activate new registry (/metrics now serves current GPU state)
	slog.InfoContext(ctx, "Activating new registry - /metrics now serves current GPU topology",
		slog.Uint64("reload_id", reloadID))
	server.SetRegistry(newRegistry)
	duration := time.Since(startTime)

	slog.InfoContext(ctx, "GPU topology change complete",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("total_time", duration))

	logTopologyInfo(reloadID, deviceWatchListMgr, duration)
}

func startDeviceWatchListManager(
	cs *counters.CounterSet, config *appconfig.Config,
) devicewatchlistmanager.Manager {
	// Create a list containing DCGM Collector, Exp Collectors and all the label Collectors
	var allCounters counters.CounterList
	var deviceWatchListManager devicewatchlistmanager.Manager

	allCounters = append(allCounters, cs.DCGMCounters...)

	allCounters = appendDCGMXIDErrorsCountDependency(allCounters, cs)
	allCounters = appendDCGMClockEventsCountDependency(cs, allCounters)

	deviceWatchListManager = devicewatchlistmanager.NewWatchListManager(allCounters, config)
	deviceWatcher := devicewatcher.NewDeviceWatcher()

	for _, deviceType := range devicewatchlistmanager.DeviceTypesToWatch {
		err := deviceWatchListManager.CreateEntityWatchList(deviceType, deviceWatcher, int64(config.CollectInterval))
		if err != nil {
			slog.Info(fmt.Sprintf("Not collecting %s metrics; %s", deviceType.String(), err))
		}
	}
	return deviceWatchListManager
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

// appendDCGMXIDErrorsCountDependency appends DCGM counters required for the DCGM_EXP_CLOCK_EVENTS_COUNT metric
func appendDCGMClockEventsCountDependency(
	cs *counters.CounterSet, allCounters []counters.Counter,
) []counters.Counter {
	if len(cs.ExporterCounters) > 0 {
		if containsExporterField(cs.ExporterCounters, counters.DCGMClockEventsCount) &&
			!containsDCGMField(allCounters, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS) {
			allCounters = append(allCounters,
				counters.Counter{
					FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
				})
		}
	}
	return allCounters
}

// appendDCGMXIDErrorsCountDependency appends DCGM counters required for the DCGM_EXP_XID_ERRORS_COUNT metric
func appendDCGMXIDErrorsCountDependency(
	allCounters []counters.Counter, cs *counters.CounterSet,
) []counters.Counter {
	if len(cs.ExporterCounters) > 0 {
		if containsExporterField(cs.ExporterCounters, counters.DCGMXIDErrorsCount) &&
			!containsDCGMField(allCounters, dcgm.DCGM_FI_DEV_XID_ERRORS) {
			allCounters = append(allCounters,
				counters.Counter{
					FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS,
				})
		}
	}
	return allCounters
}

func getCounters(ctx context.Context, config *appconfig.Config) *counters.CounterSet {
	cs, err := counters.GetCounterSet(ctx, config)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}

	// Copy labels from DCGM Counters to ExporterCounters
	for i := range cs.DCGMCounters {
		if cs.DCGMCounters[i].PromType == "label" {
			cs.ExporterCounters = append(cs.ExporterCounters, cs.DCGMCounters[i])
		}
	}
	return cs
}

// queryDCPMetrics queries DCGM for supported profiling metric groups.
// Called at: startup, GPU bind event (NOT regular hot reload - uses startup config).
// If profiling not supported or query fails, DCP collection is disabled.
func queryDCPMetrics(config *appconfig.Config, reloadID uint64) {
	slog.Debug("Querying DCGM profiling metric groups", slog.Uint64("reload_id", reloadID))

	// Add panic recovery in case profiling API segfaults during query
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("Profiling API panic - DCP metrics disabled",
				slog.Uint64("reload_id", reloadID),
				slog.String("panic", fmt.Sprintf("%v", r)))
			config.CollectDCP = false
			config.MetricGroups = nil
		}
	}()

	groups, err := dcgmprovider.Client().GetSupportedMetricGroups(0)
	if err != nil {
		config.CollectDCP = false
		config.MetricGroups = nil
		slog.Info("Not collecting DCP metrics: " + err.Error())
		return
	}

	// Log GPU model for debugging (optional)
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

	config.MetricGroups = groups
	config.CollectDCP = true
}

func parseDeviceOptions(devices string) (appconfig.DeviceOptions, error) {
	var dOpt appconfig.DeviceOptions

	letterAndRange := strings.Split(devices, ":")
	count := len(letterAndRange)
	if count > 2 {
		return dOpt, fmt.Errorf("Invalid ranged device option '%s': there can only be one specified range", devices)
	}

	letter := letterAndRange[0]
	if letter == FlexKey {
		dOpt.Flex = true
		if count > 1 {
			return dOpt, fmt.Errorf("no range can be specified with the flex option 'f'")
		}
	} else if letter == MajorKey || letter == MinorKey {
		var indices []int
		if count == 1 {
			// No range means all present devices of the type
			indices = append(indices, -1)
		} else {
			numbers := strings.Split(letterAndRange[1], ",")
			for _, numberOrRange := range numbers {
				rangeTokens := strings.Split(numberOrRange, "-")
				rangeTokenCount := len(rangeTokens)
				if rangeTokenCount > 2 {
					return dOpt, fmt.Errorf("range can only be '<number>-<number>', but found '%s'", numberOrRange)
				} else if rangeTokenCount == 1 {
					number, err := strconv.Atoi(rangeTokens[0])
					if err != nil {
						return dOpt, err
					}
					indices = append(indices, number)
				} else {
					start, err := strconv.Atoi(rangeTokens[0])
					if err != nil {
						return dOpt, err
					}
					end, err := strconv.Atoi(rangeTokens[1])
					if err != nil {
						return dOpt, err
					}

					// Add the range to the indices
					for i := start; i <= end; i++ {
						indices = append(indices, i)
					}
				}
			}
		}

		if letter == MajorKey {
			dOpt.MajorRange = indices
		} else {
			dOpt.MinorRange = indices
		}
	} else {
		return dOpt, fmt.Errorf("the only valid options preceding ':<range>' are 'g' or 'i', but found '%s'", letter)
	}

	return dOpt, nil
}

func contextToConfig(c *cli.Context) (*appconfig.Config, error) {
	gOpt, err := parseDeviceOptions(c.String(CLIGPUDevices))
	if err != nil {
		return nil, err
	}

	sOpt, err := parseDeviceOptions(c.String(CLISwitchDevices))
	if err != nil {
		return nil, err
	}

	cOpt, err := parseDeviceOptions(c.String(CLICPUDevices))
	if err != nil {
		return nil, err
	}

	dcgmLogLevel := c.String(CLIDCGMLogLevel)
	if !slices.Contains(DCGMDbgLvlValues, dcgmLogLevel) {
		return nil, fmt.Errorf("invalid %s parameter value: %s", CLIDCGMLogLevel, dcgmLogLevel)
	}

	return &appconfig.Config{
		CollectorsFile:                   c.String(CLIFieldsFile),
		Address:                          c.String(CLIAddress),
		CollectInterval:                  c.Int(CLICollectInterval),
		Kubernetes:                       c.Bool(CLIKubernetes),
		KubernetesEnablePodLabels:        c.Bool(CLIKubernetesEnablePodLabels),
		KubernetesEnablePodUID:           c.Bool(CLIKubernetesEnablePodUID),
		KubernetesGPUIdType:              appconfig.KubernetesGPUIDType(c.String(CLIKubernetesGPUIDType)),
		KubernetesPodLabelAllowlistRegex: c.StringSlice(CLIKubernetesPodLabelAllowlistRegex),
		CollectDCP:                       true,
		UseOldNamespace:                  c.Bool(CLIUseOldNamespace),
		UseRemoteHE:                      c.IsSet(CLIRemoteHEInfo),
		RemoteHEInfo:                     c.String(CLIRemoteHEInfo),
		GPUDeviceOptions:                 gOpt,
		SwitchDeviceOptions:              sOpt,
		CPUDeviceOptions:                 cOpt,
		NoHostname:                       c.Bool(CLINoHostname),
		UseFakeGPUs:                      c.Bool(CLIUseFakeGPUs),
		ConfigMapData:                    c.String(CLIConfigMapData),
		WebSystemdSocket:                 c.Bool(CLIWebSystemdSocket),
		WebConfigFile:                    c.String(CLIWebConfigFile),
		XIDCountWindowSize:               c.Int(CLIXIDCountWindowSize),
		ReplaceBlanksInModelName:         c.Bool(CLIReplaceBlanksInModelName),
		Debug:                            c.Bool(CLIDebugMode),
		ClockEventsCountWindowSize:       c.Int(CLIClockEventsCountWindowSize),
		EnableDCGMLog:                    c.Bool(CLIEnableDCGMLog),
		DCGMLogLevel:                     dcgmLogLevel,
		PodResourcesKubeletSocket:        c.String(CLIPodResourcesKubeletSocket),
		HPCJobMappingDir:                 c.String(CLIHPCJobMappingDir),
		NvidiaResourceNames:              c.StringSlice(CLINvidiaResourceNames),
		KubernetesVirtualGPUs:            c.Bool(CLIKubernetesVirtualGPUs),
		DumpConfig: appconfig.DumpConfig{
			Enabled:     c.Bool(CLIDumpEnabled),
			Directory:   c.String(CLIDumpDirectory),
			Retention:   c.Int(CLIDumpRetention),
			Compression: c.Bool(CLIDumpCompression),
		},
		KubernetesEnableDRA:       c.Bool(CLIKubernetesEnableDRA),
		DisableStartupValidate:    c.Bool(CLIDisableStartupValidate),
		EnableGPUBindUnbindWatch:  c.Bool(CLIEnableGPUBindUnbindWatch),
		GPUBindUnbindPollInterval: parseDuration(c.String(CLIGPUBindUnbindPollInterval), 1*time.Second),
	}, nil
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

// runGPUWatcher runs the GPU bind/unbind watcher with unified topology change handler
func runGPUWatcher(ctx context.Context, w *watcher.GPUBindUnbindWatcher, server *server.MetricsServer, c *cli.Context, dcgmCleanup func(), wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := w.Watch(ctx, func() {
			// Any GPU topology change (bind or unbind) triggers full reset
			// This unified approach is simpler and handles all edge cases:
			// - Multiple rapid events: only last state matters
			// - Event during reload: queued and processed after
			// - GPU swap: always leaves system in correct state
			slog.DebugContext(ctx, "GPU topology change detected")
			handleGPUTopologyChange(ctx, server, c, dcgmCleanup)
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "GPU watcher failed", slog.String("error", err.Error()))
		}
	}()
}
