package cmd

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/fsnotify/fsnotify"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/hostname"
	. "github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/prerequisites"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/server"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/stdout"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
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
	CLIFieldsFile                 = "collectors"
	CLIAddress                    = "address"
	CLICollectInterval            = "collect-interval"
	CLIKubernetes                 = "kubernetes"
	CLIKubernetesEnablePodLabels  = "kubernetes-enable-pod-labels"
	CLIKubernetesGPUIDType        = "kubernetes-gpu-id-type"
	CLIUseOldNamespace            = "use-old-namespace"
	CLIRemoteHEInfo               = "remote-hostengine-info"
	CLIGPUDevices                 = "devices"
	CLISwitchDevices              = "switch-devices"
	CLICPUDevices                 = "cpu-devices"
	CLINoHostname                 = "no-hostname"
	CLIUseFakeGPUs                = "fake-gpus"
	CLIConfigMapData              = "configmap-data"
	CLIWebSystemdSocket           = "web-systemd-socket"
	CLIWebConfigFile              = "web-config-file"
	CLIXIDCountWindowSize         = "xid-count-window-size"
	CLIReplaceBlanksInModelName   = "replace-blanks-in-model-name"
	CLIDebugMode                  = "debug"
	CLIClockEventsCountWindowSize = "clock-events-count-window-size"
	CLIEnableDCGMLog              = "enable-dcgm-log"
	CLIDCGMLogLevel               = "dcgm-log-level"
	CLILogFormat                  = "log-format"
	CLIPodResourcesKubeletSocket  = "pod-resources-kubelet-socket"
	CLIHPCJobMappingDir           = "hpc-job-mapping-dir"
	CLINvidiaResourceNames        = "nvidia-resource-names"
	CLIKubernetesVirtualGPUs      = "kubernetes-virtual-gpus"
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
		&cli.StringFlag{
			Name:  CLIKubernetesGPUIDType,
			Value: string(appconfig.GPUUID),
			Usage: fmt.Sprintf("Choose Type of GPU ID to use to map kubernetes resources to pods. Possible values: '%s', '%s'",
				appconfig.GPUUID, appconfig.DeviceName),
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE"},
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

func newOSWatcher(sigs ...os.Signal) chan os.Signal {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, sigs...)

	return sigChan
}

func action(c *cli.Context) (err error) {
	ctx, cancel := context.WithCancel(context.Background())
	return stdout.Capture(ctx, func() error {
		// The purpose of this function is to capture any panic that may occur
		// during initialization and return an error.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Encountered a failure.", slog.String(StackTrace, string(debug.Stack())))
				err = fmt.Errorf("encountered a failure; err: %v", r)
			}
		}()
		return startDCGMExporter(c, cancel)
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
		logger := slog.New(slog.NewJSONHandler(os.Stderr, &opts))
		slog.SetDefault(logger)
	default:
		return fmt.Errorf("invalid %s parameter values: %s", CLILogFormat, logFormat)
	}
	return nil
}

func startDCGMExporter(c *cli.Context, cancel context.CancelFunc) error {
	if err := configureLogger(c); err != nil {
		return err
	}

	for {
		// Create a new context for this run of the exporter
		// Runs are ended by various events (signals from OS or DCGM)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var version string
		if c != nil && c.App != nil {
			version = c.App.Version
		}

		slog.Info("Starting dcgm-exporter", slog.String("Version", version))

		config, err := contextToConfig(c)
		if err != nil {
			return err
		}

		err = prerequisites.Validate()
		if err != nil {
			return err
		}

		// Initialize DCGM Provider Instance
		dcgmprovider.Initialize(config)
		dcgmCleanup := dcgmprovider.Client().Cleanup

		// Initialize NVML Provider Instance
		nvmlprovider.Initialize()
		nvmlCleanup := nvmlprovider.Client().Cleanup

		slog.Info("DCGM successfully initialized!")
		slog.Info("NVML provider successfully initialized!")

		fillConfigMetricGroups(config)

		cs := getCounters(config)

		deviceWatchListManager := startDeviceWatchListManager(cs, config)

		hostname, err := hostname.GetHostname(config)
		if err != nil {
			nvmlCleanup()
			dcgmCleanup()
			return err
		}

		cf := collector.InitCollectorFactory(cs, deviceWatchListManager, hostname, config)

		cRegistry := registry.NewRegistry()
		for _, entityCollector := range cf.NewCollectors() {
			cRegistry.Register(entityCollector)
		}

		ch := make(chan string, 10)

		var wg sync.WaitGroup
		stop := make(chan interface{})

		wg.Add(1)

		server, cleanup, err := server.NewMetricsServer(config, ch, deviceWatchListManager, cRegistry)
		if err != nil {
			cRegistry.Cleanup()
			nvmlCleanup()
			dcgmCleanup()
			return err
		}

		go server.Run(ctx, stop, &wg)

		sigs := newOSWatcher(syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)

		go watchCollectorsFile(config.CollectorsFile, reloadMetricsServer(sigs))

		sig := <-sigs
		slog.Info("Received signal", slog.String("signal", sig.String()))
		close(stop)
		cancel() // Cancel the context for this iteration
		err = utils.WaitWithTimeout(&wg, time.Second*2)
		if err != nil {
			slog.Error(err.Error())
			cRegistry.Cleanup()
			nvmlCleanup()
			dcgmCleanup()
			cleanup()
			fatal()
		}

		// Call cleanup functions before continuing the loop
		cRegistry.Cleanup()
		nvmlCleanup()
		dcgmCleanup()
		cleanup()

		if sig != syscall.SIGHUP {
			return nil
		}

		// For SIGHUP, we'll continue the loop after cleanup
		slog.Info("Restarting dcgm-exporter after signal")
	}

	return nil
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

func getCounters(config *appconfig.Config) *counters.CounterSet {
	cs, err := counters.GetCounterSet(config)
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

func fillConfigMetricGroups(config *appconfig.Config) {
	var groups []dcgm.MetricGroup
	groups, err := dcgmprovider.Client().GetSupportedMetricGroups(0)
	if err != nil {
		config.CollectDCP = false
		slog.Info("Not collecting DCP metrics: " + err.Error())
	} else {
		slog.Info("Collecting DCP Metrics")
		config.MetricGroups = groups
	}
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
		CollectorsFile:             c.String(CLIFieldsFile),
		Address:                    c.String(CLIAddress),
		CollectInterval:            c.Int(CLICollectInterval),
		Kubernetes:                 c.Bool(CLIKubernetes),
		KubernetesEnablePodLabels:  c.Bool(CLIKubernetesEnablePodLabels),
		KubernetesGPUIdType:        appconfig.KubernetesGPUIDType(c.String(CLIKubernetesGPUIDType)),
		CollectDCP:                 true,
		UseOldNamespace:            c.Bool(CLIUseOldNamespace),
		UseRemoteHE:                c.IsSet(CLIRemoteHEInfo),
		RemoteHEInfo:               c.String(CLIRemoteHEInfo),
		GPUDeviceOptions:           gOpt,
		SwitchDeviceOptions:        sOpt,
		CPUDeviceOptions:           cOpt,
		NoHostname:                 c.Bool(CLINoHostname),
		UseFakeGPUs:                c.Bool(CLIUseFakeGPUs),
		ConfigMapData:              c.String(CLIConfigMapData),
		WebSystemdSocket:           c.Bool(CLIWebSystemdSocket),
		WebConfigFile:              c.String(CLIWebConfigFile),
		XIDCountWindowSize:         c.Int(CLIXIDCountWindowSize),
		ReplaceBlanksInModelName:   c.Bool(CLIReplaceBlanksInModelName),
		Debug:                      c.Bool(CLIDebugMode),
		ClockEventsCountWindowSize: c.Int(CLIClockEventsCountWindowSize),
		EnableDCGMLog:              c.Bool(CLIEnableDCGMLog),
		DCGMLogLevel:               dcgmLogLevel,
		PodResourcesKubeletSocket:  c.String(CLIPodResourcesKubeletSocket),
		HPCJobMappingDir:           c.String(CLIHPCJobMappingDir),
		NvidiaResourceNames:        c.StringSlice(CLINvidiaResourceNames),
		KubernetesVirtualGPUs:      c.Bool(CLIKubernetesVirtualGPUs),
	}, nil
}

func watchCollectorsFile(filePath string, onChange func()) {
	slog.Info("Watching for changes in file", slog.String("file", filePath))
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Error creating watcher", slog.String("error", err.Error()))
	}
	defer watcher.Close()

	dir := filepath.Dir(filePath)
	file := filepath.Base(filePath)

	err = watcher.Add(dir)
	if err != nil {
		slog.Error("Error adding dir to watcher", slog.String("error", err.Error()))
	}

	go func() {
		var lastModTime time.Time
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					slog.Error("Error reading events from watcher")
					return
				}

				if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
					if filepath.Base(event.Name) == file {
						time.Sleep(200 * time.Millisecond)
						info, err := os.Stat(filepath.Join(dir, file))
						if err == nil {
							modTime := info.ModTime()
							if modTime != lastModTime {
								lastModTime = modTime
								onChange()
							}
						}
					}
				}

			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	select {}
}

func reloadMetricsServer(s chan os.Signal) func() {
	// all we have to do is send a sighup
	return func() {
		slog.Info("Reloading metrics server")
		s <- syscall.SIGHUP
	}
}
