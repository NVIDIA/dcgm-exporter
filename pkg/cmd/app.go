package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/collector"
	dcgmClient "github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/dcgm_client"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/metrics"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/pipeline"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/registry"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/server"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/utils"
	"github.com/NVIDIA/dcgm-exporter/pkg/stdout"
)

const (
	undefinedConfigMapData = "none"
	deviceUsageTemplate    = `Specify which devices dcgm_client-exporter monitors.
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
	_ = t.Execute(&deviceUsageBuffer,
		map[string]string{"FlexKey": common.FlexKey, "MajorKey": common.MajorKey, "MinorKey": common.MinorKey})
	DeviceUsageStr := deviceUsageBuffer.String()

	c.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    common.CLIFieldsFile,
			Aliases: []string{"f"},
			Usage:   "Path to the file, that contains the DCGM fields to collect",
			Value:   "/etc/dcgm_client-exporter/default-counters.csv",
			EnvVars: []string{"DCGM_EXPORTER_COLLECTORS"},
		},
		&cli.StringFlag{
			Name:    common.CLIAddress,
			Aliases: []string{"a"},
			Value:   ":9400",
			Usage:   "Address",
			EnvVars: []string{"DCGM_EXPORTER_LISTEN"},
		},
		&cli.IntFlag{
			Name:    common.CLICollectInterval,
			Aliases: []string{"c"},
			Value:   30000,
			Usage:   "Interval of time at which point metrics are collected. Unit is milliseconds (ms).",
			EnvVars: []string{"DCGM_EXPORTER_INTERVAL"},
		},
		&cli.BoolFlag{
			Name:    common.CLIKubernetes,
			Aliases: []string{"k"},
			Value:   false,
			Usage:   "Enable kubernetes mapping metrics to kubernetes pods",
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES"},
		},
		&cli.BoolFlag{
			Name:    common.CLIUseOldNamespace,
			Aliases: []string{"o"},
			Value:   false,
			Usage:   "Use old 1.x namespace",
			EnvVars: []string{"DCGM_EXPORTER_USE_OLD_NAMESPACE"},
		},
		&cli.StringFlag{
			Name:    common.CLICPUDevices,
			Aliases: []string{"p"},
			Value:   common.FlexKey,
			Usage:   DeviceUsageStr,
			EnvVars: []string{"DCGM_EXPORTER_CPU_DEVICES_STR"},
		},
		&cli.StringFlag{
			Name:    common.CLIConfigMapData,
			Aliases: []string{"m"},
			Value:   undefinedConfigMapData,
			Usage:   "ConfigMap <NAMESPACE>:<NAME> for metric data",
			EnvVars: []string{"DCGM_EXPORTER_CONFIGMAP_DATA"},
		},
		&cli.StringFlag{
			Name:    common.CLIRemoteHEInfo,
			Aliases: []string{"r"},
			Value:   "localhost:5555",
			Usage:   "Connect to remote hostengine at <HOST>:<PORT>",
			EnvVars: []string{"DCGM_REMOTE_HOSTENGINE_INFO"},
		},
		&cli.StringFlag{
			Name:  common.CLIKubernetesGPUIDType,
			Value: string(common.GPUUID),
			Usage: fmt.Sprintf("Choose Type of GPU ID to use to map kubernetes resources to pods. Possible values: '%s', '%s'",
				common.GPUUID, common.DeviceName),
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE"},
		},
		&cli.StringFlag{
			Name:    common.CLIGPUDevices,
			Aliases: []string{"d"},
			Value:   common.FlexKey,
			Usage:   DeviceUsageStr,
			EnvVars: []string{"DCGM_EXPORTER_DEVICES_STR"},
		},
		&cli.BoolFlag{
			Name:    common.CLINoHostname,
			Aliases: []string{"n"},
			Value:   false,
			Usage:   "Omit the hostname information from the output, matching older versions.",
			EnvVars: []string{"DCGM_EXPORTER_NO_HOSTNAME"},
		},
		&cli.StringFlag{
			Name:    common.CLISwitchDevices,
			Aliases: []string{"s"},
			Value:   common.FlexKey,
			Usage:   DeviceUsageStr,
			EnvVars: []string{"DCGM_EXPORTER_OTHER_DEVICES_STR"},
		},
		&cli.BoolFlag{
			Name:    common.CLIUseFakeGPUs,
			Value:   false,
			Usage:   "Accept GPUs that are fake, for testing purposes only",
			EnvVars: []string{"DCGM_EXPORTER_USE_FAKE_GPUS"},
		},
		&cli.StringFlag{
			Name:    common.CLIWebConfigFile,
			Value:   "",
			Usage:   "TLS common file following webConfig spec.",
			EnvVars: []string{"DCGM_EXPORTER_WEB_CONFIG_FILE"},
		},
		&cli.IntFlag{
			Name:    common.CLIXIDCountWindowSize,
			Aliases: []string{"x"},
			Value:   int((5 * time.Minute).Milliseconds()),
			Usage:   "Set time window size in milliseconds (ms) for counting active XID errors in DCGM Exporter.",
			EnvVars: []string{"DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE"},
		},
		&cli.BoolFlag{
			Name:    common.CLIReplaceBlanksInModelName,
			Aliases: []string{"rbmn"},
			Value:   false,
			Usage:   "Replace every blank space in the GPU model name with a dash, ensuring a continuous, space-free identifier.",
			EnvVars: []string{"DCGM_EXPORTER_REPLACE_BLANKS_IN_MODEL_NAME"},
		},
		&cli.BoolFlag{
			Name:    common.CLIDebugMode,
			Value:   false,
			Usage:   "Enable debug output",
			EnvVars: []string{"DCGM_EXPORTER_DEBUG"},
		},
		&cli.IntFlag{
			Name:    common.CLIClockEventsCountWindowSize,
			Value:   int((5 * time.Minute).Milliseconds()),
			Usage:   "Set time window size in milliseconds (ms) for counting clock events in DCGM Exporter.",
			EnvVars: []string{"DCGM_EXPORTER_CLOCK_EVENTS_COUNT_WINDOW_SIZE"},
		},
		&cli.BoolFlag{
			Name:    common.CLIEnableDCGMLog,
			Value:   false,
			Usage:   "Enable writing DCGM logs to standard output (stdout).",
			EnvVars: []string{"DCGM_EXPORTER_ENABLE_DCGM_LOG"},
		},
		&cli.StringFlag{
			Name:    common.CLIDCGMLogLevel,
			Value:   common.DCGMDbgLvlNone,
			Usage:   "Specify the DCGM log verbosity level. This parameter is effective only when the '--enable-dcgm_client-log' option is set to 'true'. Possible values: NONE, FATAL, ERROR, WARN, INFO, DEBUG and VERB",
			EnvVars: []string{"DCGM_EXPORTER_DCGM_LOG_LEVEL"},
		},
	}

	if runtime.GOOS == "linux" {
		c.Flags = append(c.Flags, &cli.BoolFlag{
			Name:    common.CLIWebSystemdSocket,
			Value:   false,
			Usage:   "Use systemd socket activation listeners instead of port listeners (Linux only).",
			EnvVars: []string{"DCGM_EXPORTER_SYSTEMD_SOCKET"},
		})
	} else {
		err := "dcgm_client-exporter is only supported on Linux."
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
				logrus.WithField(common.LoggerStackTrace, string(debug.Stack())).Error("Encountered a failure.")
				err = fmt.Errorf("encountered a failure; err: %v", r)
			}
		}()
		return startDCGMExporter(c, cancel)
	})
}

func startDCGMExporter(c *cli.Context, cancel context.CancelFunc) error {
restart:

	logrus.Info("Starting dcgm_client-exporter")

	config := &common.Config{}
	err := config.Load(c)
	if err != nil {
		return err
	}

	enableDebugLogging(config)

	cleanupDCGM := dcgmClient.InitDCGM(config)
	defer cleanupDCGM()

	logrus.Info("DCGM successfully initialized!")

	// TODO (handle properly)
	dcgm.FieldsInit()
	defer dcgm.FieldsTerm()

	fillConfigMetricGroups(config)

	cs := getCounters(config)

	fieldEntityGroupTypeSystemInfo := getFieldEntityGroupTypeSystemInfo(cs, config)

	hostname, err := common.GetHostname(config)
	if err != nil {
		return err
	}

	newPipeline, cleanup, err := pipeline.NewMetricsPipeline(config,
		cs.DCGMCounters,
		hostname,
		collector.NewDCGMCollector,
		fieldEntityGroupTypeSystemInfo,
	)
	defer cleanup()
	if err != nil {
		logrus.Fatal(err)
	}

	cRegistry := registry.NewRegistry(config)

	enableDCGMExpXIDErrorsCountCollector(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry)

	enableDCGMExpClockEventsCount(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry)

	defer func() {
		cRegistry.Cleanup()
	}()

	ch := make(chan string, 10)

	var wg sync.WaitGroup
	stop := make(chan interface{})

	wg.Add(1)
	go newPipeline.Run(ch, stop, &wg)

	wg.Add(1)

	server, cleanup, err := server.NewMetricsServer(config, ch, cRegistry)
	defer cleanup()
	if err != nil {
		return err
	}

	go server.Run(stop, &wg)

	sigs := newOSWatcher(syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	sig := <-sigs
	close(stop)
	cancel()
	err = common.WaitWithTimeout(&wg, time.Second*2)
	if err != nil {
		logrus.Fatal(err)
	}

	if sig == syscall.SIGHUP {
		goto restart
	}

	return nil
}

func enableDCGMExpClockEventsCount(
	cs *common.CounterSet, fieldEntityGroupTypeSystemInfo *dcgmClient.FieldEntityGroupTypeSystemInfo,
	hostname string, config *common.Config, cRegistry *registry.Registry,
) {
	if collector.IsDCGMExpClockEventsCountEnabled(cs.ExporterCounters) {
		item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
		if !exists {
			logrus.Fatalf("%s collector cannot be initialized", metrics.DCGMClockEventsCount.String())
		}
		clocksThrottleReasonsCollector, err := collector.NewClockEventsCollector(
			cs.ExporterCounters, hostname, config, item)
		if err != nil {
			logrus.Fatal(err)
		}

		cRegistry.Register(clocksThrottleReasonsCollector)

		logrus.Infof("%s collector initialized", metrics.DCGMClockEventsCount.String())
	}
}

func enableDCGMExpXIDErrorsCountCollector(
	cs *common.CounterSet, fieldEntityGroupTypeSystemInfo *dcgmClient.FieldEntityGroupTypeSystemInfo,
	hostname string, config *common.Config, cRegistry *registry.Registry,
) {
	if collector.IsDCGMExpXIDErrorsCountEnabled(cs.ExporterCounters) {
		item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
		if !exists {
			logrus.Fatalf("%s collector cannot be initialized", metrics.DCGMXIDErrorsCount.String())
		}

		xidCollector, err := collector.NewXIDCollector(cs.ExporterCounters, hostname, config, item)
		if err != nil {
			logrus.Fatal(err)
		}

		cRegistry.Register(xidCollector)

		logrus.Infof("%s collector initialized", metrics.DCGMXIDErrorsCount.String())
	}
}

func getFieldEntityGroupTypeSystemInfo(
	cs *common.CounterSet, config *common.Config,
) *dcgmClient.FieldEntityGroupTypeSystemInfo {
	allCounters := []common.Counter{}

	allCounters = append(allCounters, cs.DCGMCounters...)
	allCounters = append(allCounters,
		common.Counter{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
		common.Counter{
			FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS,
		},
	)

	fieldEntityGroupTypeSystemInfo := dcgmClient.NewEntityGroupTypeSystemInfo(allCounters, config)

	for _, egt := range dcgmClient.FieldEntityGroupTypeToMonitor {
		err := fieldEntityGroupTypeSystemInfo.Load(egt)
		if err != nil {
			logrus.Infof("Not collecting %s metrics; %s", egt.String(), err)
		}
	}
	return fieldEntityGroupTypeSystemInfo
}

func getCounters(config *common.Config) *common.CounterSet {
	cs, err := utils.GetCounterSet(config)
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

func fillConfigMetricGroups(config *common.Config) {
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

func enableDebugLogging(config *common.Config) {
	if config.Debug {
		// enable debug logging
		logrus.SetLevel(logrus.DebugLevel)
		logrus.Debug("Debug output is enabled")
	}

	logrus.Debugf("Command line: %s", strings.Join(os.Args, " "))

	logrus.WithField(common.LoggerDumpKey, fmt.Sprintf("%+v", config)).Debug("Loaded configuration")
}
