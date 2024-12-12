package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
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
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter"
	"github.com/NVIDIA/dcgm-exporter/pkg/stdout"
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
		&cli.StringFlag{
			Name:  CLIKubernetesGPUIDType,
			Value: string(dcgmexporter.GPUUID),
			Usage: fmt.Sprintf("Choose Type of GPU ID to use to map kubernetes resources to pods. Possible values: '%s', '%s'",
				dcgmexporter.GPUUID, dcgmexporter.DeviceName),
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
			Usage:   "TLS config file following webConfig spec.",
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
			Value:   dcgmexporter.DCGMDbgLvlNone,
			Usage:   "Specify the DCGM log verbosity level. This parameter is effective only when the '--enable-dcgm-log' option is set to 'true'. Possible values: NONE, FATAL, ERROR, WARN, INFO, DEBUG and VERB",
			EnvVars: []string{"DCGM_EXPORTER_DCGM_LOG_LEVEL"},
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
		logrus.Fatal(err)
		return nil
	}

	c.Action = func(c *cli.Context) error {
		return action(c)
	}

	return c
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
				logrus.WithField(dcgmexporter.LoggerStackTrace, string(debug.Stack())).Error("Encountered a failure.")
				err = fmt.Errorf("encountered a failure; err: %v", r)
			}
		}()
		return startDCGMExporter(c, cancel)
	})
}

func startDCGMExporter(c *cli.Context, cancel context.CancelFunc) error {
restart:

	logrus.Info("Starting dcgm-exporter")

	config, err := contextToConfig(c)
	if err != nil {
		return err
	}

	enableDebugLogging(config)

	cleanupDCGM := initDCGM(config)
	defer cleanupDCGM()

	logrus.Info("DCGM successfully initialized!")

	dcgm.FieldsInit()
	defer dcgm.FieldsTerm()

	fillConfigMetricGroups(config)

	cs := getCounters(config)

	fieldEntityGroupTypeSystemInfo := getFieldEntityGroupTypeSystemInfo(cs, config)

	hostname, err := dcgmexporter.GetHostname(config)
	if err != nil {
		return err
	}

	pipeline, cleanup, err := dcgmexporter.NewMetricsPipeline(config,
		cs.DCGMCounters,
		hostname,
		dcgmexporter.NewDCGMCollector,
		fieldEntityGroupTypeSystemInfo,
	)
	defer cleanup()
	if err != nil {
		logrus.Fatal(err)
	}

	cRegistry := dcgmexporter.NewRegistry()

	enableDCGMExpXIDErrorsCountCollector(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry)

	enableDCGMExpClockEventsCount(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry)

	defer func() {
		cRegistry.Cleanup()
	}()

	ch := make(chan string, 10)

	var wg sync.WaitGroup
	stop := make(chan interface{})

	wg.Add(1)
	go pipeline.Run(ch, stop, &wg)

	wg.Add(1)

	server, cleanup, err := dcgmexporter.NewMetricsServer(config, ch, cRegistry)
	defer cleanup()
	if err != nil {
		return err
	}

	go server.Run(stop, &wg)

	sigs := newOSWatcher(syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	sig := <-sigs
	close(stop)
	cancel()
	err = dcgmexporter.WaitWithTimeout(&wg, time.Second*2)
	if err != nil {
		logrus.Fatal(err)
	}

	if sig == syscall.SIGHUP {
		goto restart
	}

	return nil
}

func enableDCGMExpClockEventsCount(cs *dcgmexporter.CounterSet, fieldEntityGroupTypeSystemInfo *dcgmexporter.FieldEntityGroupTypeSystemInfo, hostname string, config *dcgmexporter.Config, cRegistry *dcgmexporter.Registry) {
	if dcgmexporter.IsDCGMExpClockEventsCountEnabled(cs.ExporterCounters) {
		item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
		if !exists {
			logrus.Fatalf("%s collector cannot be initialized", dcgmexporter.DCGMClockEventsCount.String())
		}
		clocksThrottleReasonsCollector, err := dcgmexporter.NewClockEventsCollector(
			cs.ExporterCounters, hostname, config, item)
		if err != nil {
			logrus.Fatal(err)
		}

		cRegistry.Register(clocksThrottleReasonsCollector)

		logrus.Infof("%s collector initialized", dcgmexporter.DCGMClockEventsCount.String())
	}
}

func enableDCGMExpXIDErrorsCountCollector(cs *dcgmexporter.CounterSet, fieldEntityGroupTypeSystemInfo *dcgmexporter.FieldEntityGroupTypeSystemInfo, hostname string, config *dcgmexporter.Config, cRegistry *dcgmexporter.Registry) {
	if dcgmexporter.IsDCGMExpXIDErrorsCountEnabled(cs.ExporterCounters) {
		item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
		if !exists {
			logrus.Fatalf("%s collector cannot be initialized", dcgmexporter.DCGMXIDErrorsCount.String())
		}

		xidCollector, err := dcgmexporter.NewXIDCollector(cs.ExporterCounters, hostname, config, item)
		if err != nil {
			logrus.Fatal(err)
		}

		cRegistry.Register(xidCollector)

		logrus.Infof("%s collector initialized", dcgmexporter.DCGMXIDErrorsCount.String())
	}
}

func getFieldEntityGroupTypeSystemInfo(cs *dcgmexporter.CounterSet, config *dcgmexporter.Config) *dcgmexporter.FieldEntityGroupTypeSystemInfo {
	var allCounters []dcgmexporter.Counter

	allCounters = append(allCounters, cs.DCGMCounters...)

	allCounters = appendDCGMXIDErrorsCountDependency(allCounters, cs)
	allCounters = appendDCGMClockEventsCountDependency(cs, allCounters)

	fieldEntityGroupTypeSystemInfo := dcgmexporter.NewEntityGroupTypeSystemInfo(allCounters, config)

	for _, egt := range dcgmexporter.FieldEntityGroupTypeToMonitor {
		err := fieldEntityGroupTypeSystemInfo.Load(egt)
		if err != nil {
			logrus.Infof("Not collecting %s metrics; %s", egt.String(), err)
		}
	}
	return fieldEntityGroupTypeSystemInfo
}

// appendDCGMXIDErrorsCountDependency appends DCGM counters required for the DCGM_EXP_CLOCK_EVENTS_COUNT metric
func appendDCGMClockEventsCountDependency(cs *dcgmexporter.CounterSet, allCounters []dcgmexporter.Counter) []dcgmexporter.Counter {
	if len(cs.ExporterCounters) > 0 {
		if containsField(cs.ExporterCounters, dcgmexporter.DCGMClockEventsCount) &&
			!containsField(allCounters, dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS) {
			allCounters = append(allCounters,
				dcgmexporter.Counter{
					FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
				})
		}
	}
	return allCounters
}

// appendDCGMXIDErrorsCountDependency appends DCGM counters required for the DCGM_EXP_XID_ERRORS_COUNT metric
func appendDCGMXIDErrorsCountDependency(allCounters []dcgmexporter.Counter, cs *dcgmexporter.CounterSet) []dcgmexporter.Counter {
	if len(cs.ExporterCounters) > 0 {
		if containsField(cs.ExporterCounters, dcgmexporter.DCGMXIDErrorsCount) &&
			!containsField(allCounters, dcgm.DCGM_FI_DEV_XID_ERRORS) {
			allCounters = append(allCounters,
				dcgmexporter.Counter{
					FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS,
				})
		}
	}
	return allCounters
}

func containsField(slice []dcgmexporter.Counter, fieldID dcgmexporter.ExporterCounter) bool {
	return slices.ContainsFunc(slice, func(counter dcgmexporter.Counter) bool {
		return counter.FieldID == dcgm.Short(fieldID)
	})
}

func getCounters(config *dcgmexporter.Config) *dcgmexporter.CounterSet {
	cs, err := dcgmexporter.GetCounterSet(config)
	if err != nil {
		logrus.Fatal(err)
	}

	// Copy labels from DCGM Counters to ExporterCounters
	for i := range cs.DCGMCounters {
		if cs.DCGMCounters[i].PromType == "label" {
			cs.ExporterCounters = append(cs.ExporterCounters, cs.DCGMCounters[i])
		}
	}
	return cs
}

func fillConfigMetricGroups(config *dcgmexporter.Config) {
	var groups []dcgm.MetricGroup
	groups, err := dcgm.GetSupportedMetricGroups(0)
	if err != nil {
		config.CollectDCP = false
		logrus.Info("Not collecting DCP metrics: ", err)
	} else {
		logrus.Info("Collecting DCP Metrics")
		config.MetricGroups = groups
	}
}

func enableDebugLogging(config *dcgmexporter.Config) {
	if config.Debug {
		// enable debug logging
		logrus.SetLevel(logrus.DebugLevel)
		logrus.Debug("Debug output is enabled")
	}

	logrus.Debugf("Command line: %s", strings.Join(os.Args, " "))

	logrus.WithField(dcgmexporter.LoggerDumpKey, fmt.Sprintf("%+v", config)).Debug("Loaded configuration")
}

func initDCGM(config *dcgmexporter.Config) func() {
	if config.UseRemoteHE {
		logrus.Info("Attemping to connect to remote hostengine at ", config.RemoteHEInfo)
		cleanup, err := dcgm.Init(dcgm.Standalone, config.RemoteHEInfo, "0")
		if err != nil {
			cleanup()
			logrus.Fatal(err)
		}
		return cleanup
	} else {

		if config.EnableDCGMLog {
			os.Setenv("__DCGM_DBG_FILE", "-")
			os.Setenv("__DCGM_DBG_LVL", config.DCGMLogLevel)
		}

		cleanup, err := dcgm.Init(dcgm.Embedded)
		if err != nil {
			cleanup()
			logrus.Fatal(err)
		}

		return cleanup
	}
}

func parseDeviceOptions(devices string) (dcgmexporter.DeviceOptions, error) {
	var dOpt dcgmexporter.DeviceOptions

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

func contextToConfig(c *cli.Context) (*dcgmexporter.Config, error) {
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
	if !slices.Contains(dcgmexporter.DCGMDbgLvlValues, dcgmLogLevel) {
		return nil, fmt.Errorf("invalid %s parameter value: %s", CLIDCGMLogLevel, dcgmLogLevel)
	}

	return &dcgmexporter.Config{
		CollectorsFile:             c.String(CLIFieldsFile),
		Address:                    c.String(CLIAddress),
		CollectInterval:            c.Int(CLICollectInterval),
		Kubernetes:                 c.Bool(CLIKubernetes),
		KubernetesGPUIdType:        dcgmexporter.KubernetesGPUIDType(c.String(CLIKubernetesGPUIDType)),
		CollectDCP:                 true,
		UseOldNamespace:            c.Bool(CLIUseOldNamespace),
		UseRemoteHE:                c.IsSet(CLIRemoteHEInfo),
		RemoteHEInfo:               c.String(CLIRemoteHEInfo),
		GPUDevices:                 gOpt,
		SwitchDevices:              sOpt,
		CPUDevices:                 cOpt,
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
