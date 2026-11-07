package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/google/uuid"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
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
)

const (
	CLIConfigFile                       = appconfig.FlagConfigFile
	CLIFieldsFile                       = appconfig.FlagFieldsFile
	CLIAddress                          = appconfig.FlagAddress
	CLICollectInterval                  = appconfig.FlagCollectInterval
	CLIWatchMaxKeepAge                  = appconfig.FlagWatchMaxKeepAge
	CLIWatchMaxKeepSamples              = appconfig.FlagWatchMaxKeepSamples
	CLIKubernetes                       = appconfig.FlagKubernetes
	CLIKubernetesEnablePodLabels        = appconfig.FlagKubernetesEnablePodLabels
	CLIKubernetesEnablePodUID           = appconfig.FlagKubernetesEnablePodUID
	CLIKubernetesGPUIDType              = appconfig.FlagKubernetesGPUIDType
	CLIKubernetesPodLabelAllowlistRegex = appconfig.FlagKubernetesPodLabelAllowlistRegex
	CLIUseOldNamespace                  = appconfig.FlagUseOldNamespace
	CLIRemoteHEInfo                     = appconfig.FlagRemoteHEInfo
	CLIGPUDevices                       = appconfig.FlagGPUDevices
	CLISwitchDevices                    = appconfig.FlagSwitchDevices
	CLICPUDevices                       = appconfig.FlagCPUDevices
	CLIHealthRequireGPUs                = appconfig.FlagHealthRequireGPUs
	CLINoHostname                       = appconfig.FlagNoHostname
	CLIUseFakeGPUs                      = appconfig.FlagUseFakeGPUs
	CLIConfigMapData                    = appconfig.FlagConfigMapData
	CLIWebSystemdSocket                 = appconfig.FlagWebSystemdSocket
	CLIWebConfigFile                    = appconfig.FlagWebConfigFile
	CLIWebReadTimeout                   = appconfig.FlagWebReadTimeout
	CLIWebWriteTimeout                  = appconfig.FlagWebWriteTimeout
	CLIMaxConcurrentScrapes             = appconfig.FlagMaxConcurrentScrapes
	CLIEnableExporterMetrics            = appconfig.FlagEnableExporterMetrics
	CLIXIDCountWindowSize               = appconfig.FlagXIDCountWindowSize
	CLIReplaceBlanksInModelName         = appconfig.FlagReplaceBlanksInModelName
	CLIDebugMode                        = appconfig.FlagDebugMode
	CLIClockEventsCountWindowSize       = appconfig.FlagClockEventsCountWindowSize
	CLIEnableDCGMLog                    = appconfig.FlagEnableDCGMLog
	CLIDCGMLogLevel                     = appconfig.FlagDCGMLogLevel
	CLILogFormat                        = appconfig.FlagLogFormat
	CLIPodResourcesKubeletSocket        = appconfig.FlagPodResourcesKubeletSocket
	CLIHPCJobMappingDir                 = appconfig.FlagHPCJobMappingDir
	CLIHPCJobMappingMaxFileBytes        = appconfig.FlagHPCJobMappingMaxFileBytes
	CLIHPCJobMappingMaxJobIDBytes       = appconfig.FlagHPCJobMappingMaxJobIDBytes
	CLIHPCJobMappingMaxJobsPerFile      = appconfig.FlagHPCJobMappingMaxJobsPerFile
	CLIContainerLabels                  = appconfig.FlagContainerLabels
	CLIContainerRuntimeSocket           = appconfig.FlagContainerRuntimeSocket
	CLINvidiaResourceNames              = appconfig.FlagNvidiaResourceNames
	CLIKubernetesVirtualGPUs            = appconfig.FlagKubernetesVirtualGPUs
	CLIDumpEnabled                      = appconfig.FlagDumpEnabled
	CLIDumpDirectory                    = appconfig.FlagDumpDirectory
	CLIDumpRetention                    = appconfig.FlagDumpRetention
	CLIDumpCompression                  = appconfig.FlagDumpCompression
	CLIDumpMinInterval                  = appconfig.FlagDumpMinInterval
	CLIDumpMaxFileBytes                 = appconfig.FlagDumpMaxFileBytes
	CLIDumpMaxTotalBytes                = appconfig.FlagDumpMaxTotalBytes
	CLIDumpMaxFiles                     = appconfig.FlagDumpMaxFiles
	CLIKubernetesEnableDRA              = appconfig.FlagKubernetesEnableDRA
	CLIDisableStartupValidate           = appconfig.FlagDisableStartupValidate
	CLIEnableGPUBindUnbindWatch         = appconfig.FlagEnableGPUBindUnbindWatch
	CLIGPUBindUnbindPollInterval        = appconfig.FlagGPUBindUnbindPollInterval
	CLIDCGMCleanupTimeout               = appconfig.FlagDCGMCleanupTimeout
	CLIEnablePprof                      = appconfig.FlagEnablePprof
)

// CLI categories are shared display labels, separate from option names.
const (
	httpServerCategory        = "HTTP server"
	workloadLabelsCategory    = "Workload labels"
	devicesHostengineCategory = "Devices and hostengine"
)

var (
	initializeDCGMProviderFunc  = dcgmprovider.Initialize
	beginDCGMShutdownFunc       = dcgmprovider.BeginShutdown
	initializeNVMLProviderFunc  = nvmlprovider.Initialize
	cleanupNVMLProviderFunc     = func() { nvmlprovider.Client().Cleanup() }
	buildRegistryFunc           = buildRegistry
	getCountersFunc             = getCounters
	startWatchListManagerFunc   = startDeviceWatchListManager
	getHostnameFunc             = hostname.GetHostname
	initCollectorFactoryFunc    = collector.InitCollectorFactory
	newMetricsServerFunc        = server.NewMetricsServerWithDRAChangeCallback
	newFileWatcherFunc          = watcher.NewFileWatcher
	newGPUBindUnbindWatcherFunc = newGenerationGPUBindUnbindWatcher
)

// newGenerationGPUBindUnbindWatcher creates a watcher that reads the current
// DCGM client when each run starts. The restartable wrapper stops a running
// watch before the coordinator retires and replaces its native session.
func newGenerationGPUBindUnbindWatcher(
	opts ...watcher.GPUBindUnbindWatcherOption,
) *watcher.GPUBindUnbindWatcher {
	return watcher.NewGPUBindUnbindWatcher(opts...)
}

// NewApp configures the dcgm-exporter command. main calls Run on the returned
// app and is the only layer that turns its returned error into a process exit.
func NewApp(buildVersion ...string) *cli.App {
	defaults := appconfig.DefaultConfig()
	c := cli.NewApp()
	c.Name = "DCGM Exporter"
	c.Usage = "Generates GPU metrics in the prometheus format"
	if len(buildVersion) == 0 {
		buildVersion = append(buildVersion, "")
	}

	c.Version = buildVersion[0]

	c.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:     CLIFieldsFile,
			Category: "Metrics",
			Aliases:  []string{"f"},
			Usage:    "Read DCGM metric definitions from CSV `PATH`",
			Value:    defaults.MetricSource.File,
			EnvVars:  []string{"DCGM_EXPORTER_COLLECTORS"},
		},

		&cli.StringFlag{
			Name:     CLIConfigFile,
			Category: "Configuration",
			Usage:    "Read startup configuration from YAML `PATH`. Legacy flags and environment variables explicitly set on startup override YAML values.",
			Value:    defaults.ConfigFile,
			EnvVars:  []string{"DCGM_EXPORTER_CONFIG_FILE"},
		},

		&cli.StringFlag{
			Name:     CLIAddress,
			Category: httpServerCategory,
			Aliases:  []string{"a"},
			Value:    defaults.Address,
			Usage:    "Listen on `ADDRESS` (<HOST>:<PORT>). For IPv6, use \"[<IPv6_ADDR>]:<PORT>\" (e.g., \"[::]:9400\")",
			EnvVars:  []string{"DCGM_EXPORTER_LISTEN"},
		},

		&cli.StringFlag{
			Name:     CLIWebReadTimeout,
			Category: httpServerCategory,
			Value:    defaults.WebReadTimeout.String(),
			Usage:    "Maximum `DURATION` for reading an HTTP request.",
			EnvVars:  []string{"DCGM_EXPORTER_WEB_READ_TIMEOUT"},
		},

		&cli.StringFlag{
			Name:     CLIWebWriteTimeout,
			Category: httpServerCategory,
			Value:    defaults.WebWriteTimeout.String(),
			Usage:    "Maximum `DURATION` for generating and writing an HTTP response.",
			EnvVars:  []string{"DCGM_EXPORTER_WEB_WRITE_TIMEOUT"},
		},

		&cli.IntFlag{
			Name:     CLIMaxConcurrentScrapes,
			Category: httpServerCategory,
			Value:    defaults.MaxConcurrentScrapes,
			Usage:    "Admit up to `COUNT` concurrent scrape requests. Excess requests receive HTTP 503 while admitted overlapping requests are coalesced.",
			EnvVars:  []string{"DCGM_EXPORTER_MAX_CONCURRENT_SCRAPES"},
		},

		&cli.BoolFlag{
			Name:               CLIEnableExporterMetrics,
			Category:           "Metrics",
			Value:              defaults.EnableExporterMetrics,
			Usage:              "Expose Go runtime, process, and HTTP handler metrics about dcgm-exporter itself.",
			EnvVars:            []string{"DCGM_EXPORTER_ENABLE_EXPORTER_METRICS"},
			DisableDefaultText: true,
		},

		&cli.IntFlag{
			Name:     CLICollectInterval,
			Category: "Metrics",
			Aliases:  []string{"c"},
			Value:    defaults.CollectInterval,
			Usage:    "Collect metrics every `MILLISECONDS`.",
			EnvVars:  []string{"DCGM_EXPORTER_INTERVAL"},
		},

		&cli.DurationFlag{
			Name:     CLIWatchMaxKeepAge,
			Category: "Metrics",
			Value:    defaults.WatchRetention.MaxAge,
			Usage: "Maximum `DURATION` of samples retained by DCGM field watches. " +
				"Set to 0s to disable the age limit when a sample limit is configured.",
			EnvVars: []string{"DCGM_EXPORTER_WATCH_MAX_KEEP_AGE"},
		},

		&cli.Int64Flag{
			Name:     CLIWatchMaxKeepSamples,
			Category: "Metrics",
			Value:    defaults.WatchRetention.MaxSamples,
			Usage: "Retain up to `COUNT` samples in DCGM field watches. " +
				"Set to 0 for no sample-count limit when an age limit is configured.",
			EnvVars: []string{"DCGM_EXPORTER_WATCH_MAX_KEEP_SAMPLES"},
		},

		&cli.BoolFlag{
			Name:               CLIKubernetes,
			Category:           workloadLabelsCategory,
			Aliases:            []string{"k"},
			Value:              defaults.Kubernetes,
			Usage:              "Enable kubernetes mapping metrics to kubernetes pods",
			EnvVars:            []string{"DCGM_EXPORTER_KUBERNETES"},
			DisableDefaultText: true,
		},

		&cli.BoolFlag{
			Name:               CLIUseOldNamespace,
			Category:           "Metrics",
			Aliases:            []string{"o"},
			Value:              defaults.UseOldNamespace,
			Usage:              "Use old 1.x namespace.",
			EnvVars:            []string{"DCGM_EXPORTER_USE_OLD_NAMESPACE"},
			DisableDefaultText: true,
		},

		&cli.StringFlag{
			Name:     CLICPUDevices,
			Category: devicesHostengineCategory,
			Aliases:  []string{"p"},
			Value:    deviceOptionsFlagValue(defaults.CPUDeviceOptions),
			Usage:    "Select CPUs and CPU cores using `SELECTOR` (see device selectors below).",
			EnvVars:  []string{"DCGM_EXPORTER_CPU_DEVICES_STR"},
		},

		&cli.StringFlag{
			Name:     CLIConfigMapData,
			Category: "Metrics",
			Aliases:  []string{"m"},
			Value:    undefinedConfigMapData,
			Usage:    deprecatedUsage(CLIConfigMapData, "Read metrics from ConfigMap `NAMESPACE:NAME`."),
			EnvVars:  []string{"DCGM_EXPORTER_CONFIGMAP_DATA"},
		},

		&cli.StringFlag{
			Name:     CLIRemoteHEInfo,
			Category: devicesHostengineCategory,
			Aliases:  []string{"r"},
			Value:    defaults.RemoteHEInfo,
			Usage: "Connect to remote hostengine at `ADDRESS` (<HOST>:<PORT> or a DCGM URI) " +
				"(tcp://<HOST>:<PORT>, unix:///<SOCKET_PATH>, vsock://<CID>:<PORT>). For IPv6, use \"[<IPv6_ADDR>]:<PORT>\" (e.g., \"[::1]:5555\")",
			EnvVars: []string{"DCGM_REMOTE_HOSTENGINE_INFO"},
		},

		&cli.BoolFlag{
			Name:               CLIKubernetesEnablePodLabels,
			Category:           workloadLabelsCategory,
			Value:              defaults.KubernetesEnablePodLabels,
			Usage:              "Enable kubernetes pod labels in metrics. This parameter is effective only when the '--kubernetes' option is set to 'true'.",
			EnvVars:            []string{"DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"},
			DisableDefaultText: true,
		},

		&cli.BoolFlag{
			Name:               CLIKubernetesEnablePodUID,
			Category:           workloadLabelsCategory,
			Value:              defaults.KubernetesEnablePodUID,
			Usage:              "Enable kubernetes pod UID in metrics. This parameter is effective only when the '--kubernetes' option is set to 'true'.",
			EnvVars:            []string{"DCGM_EXPORTER_KUBERNETES_ENABLE_POD_UID"},
			DisableDefaultText: true,
		},

		&cli.StringFlag{
			Name:     CLIKubernetesGPUIDType,
			Category: workloadLabelsCategory,
			Value:    string(defaults.KubernetesGPUIdType),
			Usage: fmt.Sprintf("Map Kubernetes resources to pods using GPU ID `TYPE`. Possible values: '%s', '%s'",
				appconfig.GPUUID, appconfig.DeviceName),
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE"},
		},

		&cli.StringSliceFlag{
			Name:     CLIKubernetesPodLabelAllowlistRegex,
			Category: workloadLabelsCategory,
			Value:    cli.NewStringSlice(defaults.KubernetesPodLabelAllowlistRegex...),
			Usage: "Include pod labels matching regex `PATTERN` (comma-separated). Required when pod labels are enabled; " +
				"use .* to include all labels. " +
				"This parameter is effective only when '--kubernetes-enable-pod-labels' is true.",
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_POD_LABEL_ALLOWLIST_REGEX"},
		},

		&cli.StringFlag{
			Name:     CLIGPUDevices,
			Category: devicesHostengineCategory,
			Aliases:  []string{"d"},
			Value:    deviceOptionsFlagValue(defaults.GPUDeviceOptions),
			Usage:    "Select GPUs and MIG GPU instances using `SELECTOR`; i is unavailable unless MIG mode is enabled.",
			EnvVars:  []string{"DCGM_EXPORTER_DEVICES_STR"},
		},

		&cli.BoolFlag{
			Name:               CLINoHostname,
			Category:           "Metrics",
			Aliases:            []string{"n"},
			Value:              defaults.NoHostname,
			Usage:              "Omit the hostname information from the output, matching older versions.",
			EnvVars:            []string{"DCGM_EXPORTER_NO_HOSTNAME"},
			DisableDefaultText: true,
		},

		&cli.BoolFlag{
			Name:     CLIHealthRequireGPUs,
			Category: httpServerCategory,
			Value:    defaults.HealthRequireGPUs,
			Usage: "Report /health as unhealthy when no GPU collector is registered. " +
				"Off by default so nodes that legitimately expose no GPUs keep passing. " +
				"Enable where every instance is expected to see at least one GPU, so that " +
				"a liveness probe on /health restarts an exporter that came up against a " +
				"hostengine with no GPUs visible.",
			EnvVars:            []string{"DCGM_EXPORTER_HEALTH_REQUIRE_GPUS"},
			DisableDefaultText: true,
		},

		&cli.StringFlag{
			Name:     CLISwitchDevices,
			Category: devicesHostengineCategory,
			Aliases:  []string{"s"},
			Value:    deviceOptionsFlagValue(defaults.SwitchDeviceOptions),
			Usage:    "Select switches and links using `SELECTOR` (see device selectors below).",
			EnvVars:  []string{"DCGM_EXPORTER_OTHER_DEVICES_STR"},
		},

		&cli.BoolFlag{
			Name:               CLIUseFakeGPUs,
			Category:           devicesHostengineCategory,
			Value:              defaults.UseFakeGPUs,
			Usage:              "Accept GPUs that are fake, for testing purposes only",
			EnvVars:            []string{"DCGM_EXPORTER_USE_FAKE_GPUS"},
			DisableDefaultText: true,
		},

		&cli.StringFlag{
			Name:     CLIWebConfigFile,
			Category: httpServerCategory,
			Value:    defaults.WebConfigFile,
			Usage:    "Web config file `PATH` following the exporter-toolkit spec: https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md.",
			EnvVars:  []string{"DCGM_EXPORTER_WEB_CONFIG_FILE"},
		},

		&cli.IntFlag{
			Name:     CLIXIDCountWindowSize,
			Category: "Metrics",
			Aliases:  []string{"x"},
			Value:    defaults.XIDCountWindowSize,
			Usage:    "Window `MILLISECONDS` for counting sampled XID error observations in DCGM_EXP_XID_ERRORS_COUNT. Must be greater than 0.",
			EnvVars:  []string{"DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE"},
		},

		&cli.BoolFlag{
			Name:               CLIReplaceBlanksInModelName,
			Category:           "Metrics",
			Aliases:            []string{"rbmn"},
			Value:              defaults.ReplaceBlanksInModelName,
			Usage:              "Replace every blank space in the GPU model name with a dash, ensuring a continuous, space-free identifier.",
			EnvVars:            []string{"DCGM_EXPORTER_REPLACE_BLANKS_IN_MODEL_NAME"},
			DisableDefaultText: true,
		},

		&cli.BoolFlag{
			Name:               CLIDebugMode,
			Category:           "Diagnostics",
			Value:              defaults.Debug,
			Usage:              "Enable debug output",
			EnvVars:            []string{"DCGM_EXPORTER_DEBUG"},
			DisableDefaultText: true,
		},

		&cli.IntFlag{
			Name:     CLIClockEventsCountWindowSize,
			Category: "Metrics",
			Value:    defaults.ClockEventsCountWindowSize,
			Usage:    "Window `MILLISECONDS` for counting sampled clock-event observations in DCGM_EXP_CLOCK_EVENTS_COUNT. Must be greater than 0.",
			EnvVars:  []string{"DCGM_EXPORTER_CLOCK_EVENTS_COUNT_WINDOW_SIZE"},
		},

		&cli.BoolFlag{
			Name:               CLIEnableDCGMLog,
			Category:           "Diagnostics",
			Value:              defaults.EnableDCGMLog,
			Usage:              "Enable writing DCGM logs to standard output (stdout). Embedded hostengine only; not valid with --remote-hostengine-info.",
			EnvVars:            []string{"DCGM_EXPORTER_ENABLE_DCGM_LOG"},
			DisableDefaultText: true,
		},

		&cli.StringFlag{
			Name:     CLIDCGMLogLevel,
			Category: "Diagnostics",
			Value:    defaults.DCGMLogLevel,
			Usage: "DCGM log verbosity `LEVEL`. This parameter is effective only when the '--enable-dcgm-log' option is set to 'true'. " +
				"Possible values: NONE, FATAL, ERROR, WARN, INFO, DEBUG and VERB",
			EnvVars: []string{"DCGM_EXPORTER_DCGM_LOG_LEVEL"},
		},

		&cli.StringFlag{
			Name:     CLILogFormat,
			Category: "Diagnostics",
			Value:    defaults.LogFormat,
			Usage:    "Log output `FORMAT`. Possible values: text, json",
			EnvVars:  []string{"DCGM_EXPORTER_LOG_FORMAT"},
		},

		&cli.StringFlag{
			Name:     CLIPodResourcesKubeletSocket,
			Category: workloadLabelsCategory,
			Value:    defaults.PodResourcesKubeletSocket,
			Usage:    "Kubelet pod-resources socket `PATH`.",
			EnvVars:  []string{"DCGM_POD_RESOURCES_KUBELET_SOCKET"},
		},

		&cli.StringFlag{
			Name:     CLIHPCJobMappingDir,
			Category: workloadLabelsCategory,
			Value:    defaults.HPCJobMappingDir,
			Usage:    "Read GPU to job mapping files from `DIRECTORY`.",
			EnvVars:  []string{"DCGM_HPC_JOB_MAPPING_DIR"},
		},

		&cli.Int64Flag{
			Name:     CLIHPCJobMappingMaxFileBytes,
			Category: workloadLabelsCategory,
			Value:    defaults.HPCJobMappingMaxFileBytes,
			Usage:    "Ignore an HPC job mapping file larger than `BYTES`.",
			EnvVars:  []string{"DCGM_HPC_JOB_MAPPING_MAX_FILE_BYTES"},
		},

		&cli.Int64Flag{
			Name:     CLIHPCJobMappingMaxJobIDBytes,
			Category: workloadLabelsCategory,
			Value:    defaults.HPCJobMappingMaxJobIDBytes,
			Usage:    "Ignore an HPC job mapping file with a job ID longer than `BYTES`.",
			EnvVars:  []string{"DCGM_HPC_JOB_MAPPING_MAX_JOB_ID_BYTES"},
		},

		&cli.Int64Flag{
			Name:     CLIHPCJobMappingMaxJobsPerFile,
			Category: workloadLabelsCategory,
			Value:    defaults.HPCJobMappingMaxJobsPerFile,
			Usage:    "Ignore an HPC job mapping file with more than `COUNT` distinct jobs.",
			EnvVars:  []string{"DCGM_HPC_JOB_MAPPING_MAX_JOBS_PER_FILE"},
		},

		&cli.BoolFlag{
			Name:               CLIContainerLabels,
			Category:           workloadLabelsCategory,
			Value:              defaults.ContainerLabels,
			Usage:              "Enable runtime container labels in metrics.",
			EnvVars:            []string{"DCGM_EXPORTER_CONTAINER_LABELS"},
			DisableDefaultText: true,
		},

		&cli.StringFlag{
			Name:     CLIContainerRuntimeSocket,
			Category: workloadLabelsCategory,
			Value:    defaults.ContainerRuntimeSocket,
			Usage:    "Container runtime socket `PATH` used for container labels. Empty means /var/run/docker.sock when container labels are on.",
			EnvVars:  []string{"DCGM_CONTAINER_RUNTIME_SOCKET"},
		},

		&cli.StringSliceFlag{
			Name:     CLINvidiaResourceNames,
			Category: workloadLabelsCategory,
			Value:    cli.NewStringSlice(defaults.NvidiaResourceNames...),
			Usage:    "GPU resource `NAME` values such as nvidia.com/a100, nvidia.com/a10.",
			EnvVars:  []string{"NVIDIA_RESOURCE_NAMES"},
		},

		&cli.BoolFlag{
			Name:     CLIKubernetesVirtualGPUs,
			Category: workloadLabelsCategory,
			Value:    defaults.KubernetesVirtualGPUs,
			Usage: "Capture metrics associated with virtual GPUs exposed by Kubernetes device plugins " +
				"when using GPU sharing strategies, e.g. time-sharing or MPS.",
			EnvVars:            []string{"KUBERNETES_VIRTUAL_GPUS"},
			DisableDefaultText: true,
		},

		&cli.BoolFlag{
			Name:               CLIDumpEnabled,
			Category:           "Diagnostics",
			Value:              defaults.DumpConfig.Enabled,
			Usage:              "Enable file-based debugging dumps for troubleshooting",
			EnvVars:            []string{"DCGM_EXPORTER_DUMP_ENABLED"},
			DisableDefaultText: true,
		},

		&cli.StringFlag{
			Name:     CLIDumpDirectory,
			Category: "Diagnostics",
			Value:    defaults.DumpConfig.Directory,
			Usage:    "Store debug dump files in `DIRECTORY`",
			EnvVars:  []string{"DCGM_EXPORTER_DUMP_DIRECTORY"},
		},

		&cli.IntFlag{
			Name:     CLIDumpRetention,
			Category: "Diagnostics",
			Value:    int(defaults.DumpConfig.Retention / time.Hour),
			Usage:    "Retain debug dump files for `HOURS` (0 = no age cleanup; the size and count limits still apply)",
			EnvVars:  []string{"DCGM_EXPORTER_DUMP_RETENTION"},
		},

		&cli.BoolFlag{
			Name:     CLIDumpCompression,
			Category: "Diagnostics",
			Value:    defaults.DumpConfig.Compression,
			Usage:    "Use gzip compression for debug dump files",
			EnvVars:  []string{"DCGM_EXPORTER_DUMP_COMPRESSION"},
		},

		&cli.StringFlag{
			Name:     CLIDumpMinInterval,
			Category: "Diagnostics",
			Value:    defaults.DumpConfig.MinInterval.String(),
			Usage:    "Write at most one debug dump per `DURATION` for each dump kind and entity group",
			EnvVars:  []string{"DCGM_EXPORTER_DUMP_MIN_INTERVAL"},
		},

		&cli.Int64Flag{
			Name:     CLIDumpMaxFileBytes,
			Category: "Diagnostics",
			Value:    defaults.DumpConfig.MaxFileBytes,
			Usage:    "Limit each debug dump file to `BYTES` on disk",
			EnvVars:  []string{"DCGM_EXPORTER_DUMP_MAX_FILE_BYTES"},
		},

		&cli.Int64Flag{
			Name:     CLIDumpMaxTotalBytes,
			Category: "Diagnostics",
			Value:    defaults.DumpConfig.MaxTotalBytes,
			Usage:    "Limit all debug dump files to `BYTES` in total; the oldest files are removed first",
			EnvVars:  []string{"DCGM_EXPORTER_DUMP_MAX_TOTAL_BYTES"},
		},

		&cli.Int64Flag{
			Name:     CLIDumpMaxFiles,
			Category: "Diagnostics",
			Value:    defaults.DumpConfig.MaxFiles,
			Usage:    "Keep at most `COUNT` debug dump files; the oldest files are removed first",
			EnvVars:  []string{"DCGM_EXPORTER_DUMP_MAX_FILES"},
		},

		&cli.BoolFlag{
			Name:               CLIKubernetesEnableDRA,
			Category:           workloadLabelsCategory,
			Value:              defaults.KubernetesEnableDRA,
			Usage:              "Capture metrics associated with GPUs managed by Kubernetes Dynamic Resource Allocation (DRA) API.",
			EnvVars:            []string{"KUBERNETES_ENABLE_DRA"},
			DisableDefaultText: true,
		},

		&cli.BoolFlag{
			Name:               CLIDisableStartupValidate,
			Category:           "Diagnostics",
			Value:              defaults.DisableStartupValidate,
			Usage:              "Disable validation checks during startup. Can be useful for running in minimal environments or testing",
			EnvVars:            []string{"DISABLE_STARTUP_VALIDATE"},
			DisableDefaultText: true,
		},

		&cli.BoolFlag{
			Name:               CLIEnableGPUBindUnbindWatch,
			Category:           devicesHostengineCategory,
			Value:              defaults.EnableGPUBindUnbindWatch,
			Usage:              "Enable watching for GPU bind/unbind events to trigger automatic reloads (requires DCGM 4.5+)",
			EnvVars:            []string{"DCGM_EXPORTER_ENABLE_GPU_BIND_UNBIND_WATCH"},
			DisableDefaultText: true,
		},

		&cli.StringFlag{
			Name:     CLIGPUBindUnbindPollInterval,
			Category: devicesHostengineCategory,
			Usage:    "Poll GPU bind/unbind events every `DURATION` (DCGM recommends 1s).",
			EnvVars:  []string{"DCGM_EXPORTER_GPU_BIND_UNBIND_POLL_INTERVAL"},
			Value:    defaults.GPUBindUnbindPollInterval.String(),
		},

		&cli.DurationFlag{
			Name:     CLIDCGMCleanupTimeout,
			Category: devicesHostengineCategory,
			Usage:    "Wait up to `DURATION` for DCGM to release watches; if DCGM has not finished by then, the exporter exits.",
			EnvVars:  []string{envDCGMCleanupTimeout},
			Value:    defaults.DCGMCleanupTimeout,
		},

		&cli.BoolFlag{
			Name:               CLIEnablePprof,
			Category:           httpServerCategory,
			Value:              defaults.EnablePprof,
			Usage:              "Enable /debug/pprof/ HTTP endpoints on the metrics port; needs a web config file with TLS or basic auth",
			EnvVars:            []string{"DCGM_EXPORTER_ENABLE_PPROF"},
			DisableDefaultText: true,
		},
	}

	if runtime.GOOS == "linux" {
		c.Flags = append(c.Flags, &cli.BoolFlag{
			Name:               CLIWebSystemdSocket,
			Category:           httpServerCategory,
			Value:              defaults.WebSystemdSocket,
			Usage:              "Use systemd socket activation listeners instead of port listeners (Linux only).",
			EnvVars:            []string{"DCGM_EXPORTER_SYSTEMD_SOCKET"},
			DisableDefaultText: true,
		})
	} else {
		c.Action = func(*cli.Context) error {
			return errors.New("dcgm-exporter is only supported on Linux")
		}

		return c
	}

	c.Action = action
	configureInspectionCLI(c)

	return c
}

// newOSWatcher provides an owned signal channel and cleanup function for the
// application runner. StartDCGMExporterWithSignalSource uses it in production.
func newOSWatcher(sigs ...os.Signal) (chan os.Signal, func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, sigs...)
	cleanup := func() {
		signal.Stop(sigChan)
		close(sigChan)
	}
	return sigChan, cleanup
}

// action runs exporter startup behind stdout capture and turns a startup panic
// into a returned error. NewApp installs it as the Linux command action.
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

// configureLogger installs the configured text or JSON slog handler before
// startup. The command action calls it once so every later component shares it.
// The format and level come from the resolved config, so a YAML file can set them.
func configureLogger(config *appconfig.Config) error {
	logFormat := config.LogFormat
	logDebug := config.Debug
	var opts slog.HandlerOptions
	if logDebug {
		opts.Level = slog.LevelDebug
		defer slog.Debug("Debug output is enabled")
	}

	if err := appconfig.ValidateLogFormat(logFormat); err != nil {
		return err
	}

	if logFormat == "json" {
		// Use our custom JSON handler that properly handles complex structs
		logging.SetupGlobalLogger(os.Stderr, &opts)
		return nil
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &opts)))

	return nil
}

// resolveConfigAndLogger resolves startup config, installs the logger that the
// config selects, logs the config warnings with it, and then checks the
// enrichment sources, which can wait up to a minute for a required one. A config
// error is returned before any logger is installed. Nothing is served until
// this returns. The result keeps the origins, which runtime messages name.
func resolveConfigAndLogger(ctx context.Context, c *cli.Context) (*appconfig.Resolved, error) {
	resolved, err := resolveRuntimeConfig(c)
	if err != nil {
		return nil, err
	}

	if err := configureLogger(resolved.Config); err != nil {
		return nil, err
	}

	logConfigWarnings(resolved.Warnings)
	logged := len(resolved.Warnings)

	if err := resolveEnrichment(ctx, resolved); err != nil {
		return nil, err
	}

	logConfigWarnings(resolved.Warnings[logged:])

	return resolved, nil
}

// logConfigWarnings logs each warning collected while resolving the config.
func logConfigWarnings(warnings []appconfig.ResolutionWarning) {
	for _, warning := range warnings {
		slog.Warn(warning.Message, warning.Attrs...)
	}
}

// startupError turns a stop signal during the wait for a required enrichment
// source into a clean stop; any other startup error is returned as it is.
func startupError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		slog.Info("Stopped while waiting for enrichment sources")
		return nil
	}

	return err
}

// appVersion returns the CLI app's version, or "" when c or its app is nil.
func appVersion(c *cli.Context) string {
	if c == nil || c.App == nil {
		return ""
	}

	return c.App.Version
}

// runDCGMExporter starts the exporter until lifecycleCtx is canceled or the
// metrics server returns a terminal error. Reload requests use the same path
// as SIGHUP and preserve the current registry when a replacement build fails.
func runDCGMExporter(lifecycleCtx context.Context, c *cli.Context, reloadRequests <-chan struct{}) (returnErr error) {
	if lifecycleCtx == nil {
		return errors.New("lifecycle context is required")
	}

	resolved, err := resolveConfigAndLogger(lifecycleCtx, c)
	if err != nil {
		return startupError(lifecycleCtx, err)
	}

	config := resolved.Config

	slog.Info("Starting dcgm-exporter", slog.String("Version", appVersion(c)))

	config.ServiceInstanceID = uuid.NewString()
	coord := initReloadCoordinator(config)

	// Cleanup timeout messages name the input that set the timeout.
	coord.cleanupSource = resolved.Origin(appconfig.FieldDCGMCleanupTimeout)

	if config.EnableOTLP {
		coord.readConfig = func() (*appconfig.Config, error) { return contextToConfig(c) }
	}

	// Initialize DCGM Provider Instance (once)
	if err := initializeDCGMProviderFunc(config); err != nil {
		return fmt.Errorf("initialize DCGM provider: %w", err)
	}

	defer cleanupDCGMAfterRetirement(coord)
	slog.Info("DCGM successfully initialized!")

	ctx := lifecycleCtx
	watcherCtx, watcherCancel := context.WithCancel(lifecycleCtx)
	defer watcherCancel()
	var watcherWg sync.WaitGroup

	// Stop the GPU watch before DCGM cleanup. A stop timeout blocks that cleanup.
	defer func() {
		if err := coord.stopGPUWatcher(); err != nil && !errors.Is(returnErr, err) {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	// Register the lifecycle watch immediately after DCGM initialization. Trigger
	// safely queues any completion observed before later startup reads or before
	// the coordinator begins draining events.
	if err := startGPULifecycleWatcher(watcherCtx, config, coord); err != nil {
		return err
	}

	// Initialize NVML as the preferred MIG profile-name source in every deployment.
	if err := initializeNVMLForConfig(config); err != nil {
		return err
	}

	defer cleanupNVMLAfterRetirement(coord)

	// Query profiling support after starting the GPU lifecycle watcher, if enabled.
	// CSV reloads reuse this snapshot until the next GPU lifecycle reset.
	coord.queryDCPMetrics(0)

	// Refresh a stale discovery snapshot if a GPU detaches during construction.
	// Other setup failures remain fatal, including an unknown GPU status.
	initialRegistry, deviceWatchListManager, err := buildInitialRegistry(ctx, config, coord)
	if err != nil {
		return err
	}

	cleanupRegistry := shutdownRegistryCleanup(ctx, coord, config, func() *registry.Registry { return initialRegistry })

	defer func() { cleanupRegistryAfterShutdown(coord, cleanupRegistry, &returnErr) }()

	// Create metrics server (will run throughout entire lifecycle)
	metricsServer, serverCleanup, err := newMetricsServerFunc(
		config,
		deviceWatchListManager,
		initialRegistry,
		func() { coord.Trigger(evDRAResourceSliceChanged) },
	)
	if err != nil {
		return err
	}

	defer serverCleanup()
	coord.setServer(metricsServer)
	cleanupRegistry = shutdownRegistryCleanup(ctx, coord, config, metricsServer.ClearRegistry)

	// Start HTTP server (runs continuously until shutdown signal)
	stop := make(chan struct{})
	serverResults := make(chan error, 1)
	go func() {
		serverResults <- metricsServer.Run(ctx, stop)
	}()

	slog.Info("HTTP server started - ready to serve metrics")

	startRuntimeWatchers(watcherCtx, config, coord, &watcherWg)

	// Wait for shutdown. Reload requests trigger the same handler as a CSV
	// change, matching SIGHUP production behavior.
	serverStopped, terminalErr := waitForExporterShutdown(lifecycleCtx, reloadRequests, serverResults, coord)

	slog.Info("Shutting down gracefully...")

	// Stop watchers first
	watcherCancel()
	watcherWg.Wait()

	// Stop HTTP server unless it already stopped after reporting a terminal error.
	if !serverStopped {
		close(stop)
		terminalErr = metricsServerFailure(coord, <-serverResults)
	}

	if err := metricsServer.TerminalError(); err != nil && !errors.Is(terminalErr, err) {
		terminalErr = errors.Join(terminalErr, err)
	}

	slog.Info("Shutdown complete")
	return terminalErr
}

// cleanupRegistryAfterShutdown waits for the final published registry before the
// provider defers run, joining retirement errors into the lifecycle result.
func cleanupRegistryAfterShutdown(coord *reloadCoordinator, cleanup func() error, returnErr *error) {
	if coord.nativeCleanupBlocked.Load() {
		return
	}
	beginDCGMShutdownFunc()
	if err := cleanup(); err != nil {
		coord.blockNativeCleanup(err)
		*returnErr = errors.Join(*returnErr, err)
	}
}

// cleanupDCGMAfterRetirement keeps native handles alive when retirement was unsafe.
func cleanupDCGMAfterRetirement(coord *reloadCoordinator) {
	if coord.nativeCleanupBlocked.Load() {
		slog.Error("Skipping DCGM cleanup because registry retirement did not complete")
		return
	}
	if dcgmClient := dcgmprovider.Client(); dcgmClient != nil {
		dcgmClient.Cleanup()
	}
}

// shutdownRegistryCleanup returns the shutdown cleanup that takes the
// registry from current and waits, within the DCGM cleanup timeout, for it to
// retire. A timeout error names the shutdown step and the timeout's source.
func shutdownRegistryCleanup(
	ctx context.Context, coord *reloadCoordinator, config *appconfig.Config, current func() *registry.Registry,
) func() error {
	return func() error {
		return waitForRegistryRetirement(ctx, current(), coord.cleanupWait(config, cleanupShutdown))
	}
}

// startGPULifecycleWatcher registers lifecycle events before registry discovery.
// The DCGM cleanup timeout bounds watch setup and stop.
func startGPULifecycleWatcher(ctx context.Context, config *appconfig.Config, coord *reloadCoordinator) error {
	if !config.EnableGPUBindUnbindWatch {
		return nil
	}
	bindUnbindWatcher := newGPUBindUnbindWatcherFunc(
		watcher.WithPollInterval(config.GPUBindUnbindPollInterval),
	)
	onEvent := func(state dcgm.BindUnbindEventState) {
		event, ok := reloadEventForGPUState(state)
		if !ok {
			return
		}
		coord.Trigger(event)
	}
	wait := coord.cleanupWait(config, cleanupGPUWatchStart)
	coord.setGPUWatcher(watcher.NewRestartableGPUWatcher(bindUnbindWatcher.Start, onEvent, wait.timeout), wait)

	if err := coord.startGPUWatcher(ctx); err != nil {
		return fmt.Errorf("start GPU bind/unbind watcher: %w", err)
	}
	return nil
}

// cleanupNVMLAfterRetirement preserves the same native-retirement guard as DCGM.
func cleanupNVMLAfterRetirement(coord *reloadCoordinator) {
	if coord.nativeCleanupBlocked.Load() {
		slog.Error("Skipping NVML cleanup because registry retirement did not complete")
		return
	}
	cleanupNVMLProviderFunc()
}

func startRuntimeWatchers(ctx context.Context, config *appconfig.Config, coord *reloadCoordinator, watcherWg *sync.WaitGroup) {
	// Start the coordinator after the server is ready. Any lifecycle completion
	// observed during startup is already queued by Trigger.
	watcherWg.Add(1)
	go func() {
		defer watcherWg.Done()
		coord.Run(ctx)
	}()

	// File watcher (metric source changes) — trigger a config reload on change.
	if metricFile, ok := config.MetricFileWatcherPath(); ok {
		fileWatcher := newFileWatcherFunc(metricFile)
		runWatcher(ctx, fileWatcher, func() {
			slog.Info("Metric file changed - triggering hot reload")
			coord.Trigger(evConfigChanged)
		}, watcherWg)
	}

	if config.EnableOTLP && config.ConfigFile != "" {
		fileWatcher := newFileWatcherFunc(config.ConfigFile)
		runWatcher(ctx, fileWatcher, func() {
			slog.Info("OTLP YAML file changed - triggering hot reload")
			coord.Trigger(evConfigChanged)
		}, watcherWg)
	}

	// GPU bind/unbind watcher (optional) reports concrete DCGM lifecycle phases.
	coord.stopGPUWatcherOnDone(ctx, watcherWg)
}

// waitForExporterShutdown stops on lifecycle cancellation or a terminal server
// result; closed reload sources are disabled rather than polled repeatedly.
func waitForExporterShutdown(ctx context.Context, reloadRequests <-chan struct{}, serverResults <-chan error, coord *reloadCoordinator) (serverStopped bool, terminalErr error) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Shutdown requested", slog.String("reason", ctx.Err().Error()))
			return false, nil
		case err := <-serverResults:
			return true, metricsServerFailure(coord, err)
		case _, ok := <-reloadRequests:
			if !ok {
				reloadRequests = nil
				continue
			}
			slog.Info("Reload requested - triggering hot reload")
			coord.Trigger(evConfigChanged)
		}
	}
}

// metricsServerFailure blocks native cleanup before shutdown if scrapes did not drain.
func metricsServerFailure(coord *reloadCoordinator, err error) error {
	if err == nil {
		return nil
	}
	terminalErr := fmt.Errorf("metrics server stopped: %w", err)
	if errors.Is(err, server.ErrActiveScrapeDrainTimeout) {
		coord.blockNativeCleanup(err)
		slog.Warn("Skipping native cleanup after active scrape drain timeout")
	}
	return terminalErr
}

// initializeNVMLForConfig requires NVML when Kubernetes startup validation is
// enabled; otherwise, initialization failure falls back to DCGM profile names.
func initializeNVMLForConfig(config *appconfig.Config) error {
	err := initializeNVMLProviderFunc()
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
	return nil
}

// buildInitialRegistry retries only topology detach failures after safe rollback;
// provider cleanup remains blocked if rollback cannot finish.
func buildInitialRegistry(ctx context.Context, config *appconfig.Config, coord *reloadCoordinator) (*registry.Registry, devicewatchlistmanager.Manager, error) {
	var err error
	var initialRegistry *registry.Registry
	var deviceWatchListManager devicewatchlistmanager.Manager
	for retries := 0; ; retries++ {
		initialRegistry, deviceWatchListManager, err = buildRegistryFunc(ctx, config, coord.dcp)
		if err == nil && ctx.Err() == nil {
			break
		}
		if err == nil {
			err = errors.Join(ctx.Err(), coord.retireRegistry(ctx, initialRegistry, config, cleanupCanceledBuild))
		}

		if rollbackErr := waitForCollectorRollback(ctx, err, coord.cleanupWait(config, cleanupFailedBuild)); rollbackErr != nil {
			coord.blockNativeCleanup(rollbackErr)
			return nil, nil, errors.Join(err, rollbackErr)
		}

		if !errors.Is(err, errGPUDetachedDuringBuild) || retries >= maxGPURecoveryRetries {
			return nil, nil, err
		}
		if ctx.Err() != nil {
			return nil, nil, errors.Join(err, ctx.Err())
		}

		slog.Info("GPU detached during startup; rebuilding registry for the current topology")
		coord.queryDCPMetrics(0)
	}
	return initialRegistry, deviceWatchListManager, nil
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
