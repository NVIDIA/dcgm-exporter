/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cli

import (
	"context"
	"fmt"

	urfavecli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/bootstrap"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// clusterCommand builds the complete cluster subcommand tree.
func clusterCommand(opts Options) *urfavecli.Command {
	command := &urfavecli.Command{
		Name:      "cluster",
		Usage:     "Manage a local or external e2e Kubernetes target",
		UsageText: "e2e cluster <command> [options]",
		Action: func(c *urfavecli.Context) error {
			if c.NArg() == 0 {
				return urfavecli.ShowSubcommandHelp(c)
			}
			return fmt.Errorf("unknown e2e cluster command %q", c.Args().First())
		},
	}
	command.Subcommands = []*urfavecli.Command{
		newClusterCheckCommand(opts),
		newClusterLifecycleCommand("up", opts),
		newClusterLifecycleCommand("deploy", opts),
		newClusterLifecycleCommand("deploy-dcgm", opts),
		newClusterConfigCommand("status", opts),
		newClusterConfigCommand("logs", opts),
		newClusterConfigCommand("cleanup", opts),
	}
	return command
}

// newClusterCheckCommand verifies the tools required by local cluster workflows.
func newClusterCheckCommand(opts Options) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "check",
		Usage:     "Verify local cluster tools are available",
		UsageText: "e2e cluster check",
		Action: func(c *urfavecli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unexpected e2e cluster check arguments: %v", c.Args().Slice())
			}
			restorePath, err := bootstrap.ConfigureToolPath(opts.Root, config.TestOptions{})
			if err != nil {
				return err
			}
			defer restorePath()
			return cluster.CheckLocal(c.Context, opts.Runner, opts.Stdout)
		},
	}
}

// newClusterLifecycleCommand reuses the tests flag contract for cluster creation and deployment.
func newClusterLifecycleCommand(name string, opts Options) *urfavecli.Command {
	testOpts, envErr := clusterLifecycleOptionsFromEnv()
	command := newTestsCLICommand(&testOpts, false)
	command.Name = name
	command.Usage = clusterCommandUsage(name)
	command.UsageText = "e2e cluster " + name + " [options]"
	command.Description = ""
	command.Flags = clusterLifecycleFlags(command.Flags)
	parseAction := command.Action
	command.Before = func(_ *urfavecli.Context) error {
		return envErr
	}
	command.Action = func(c *urfavecli.Context) error {
		if c.NArg() != 0 {
			return fmt.Errorf("unexpected e2e cluster %s arguments: %v", name, c.Args().Slice())
		}
		if err := parseAction(c); err != nil {
			return err
		}
		ctx, cancel, err := timeoutContext(c.Context, testOpts.Timeout)
		if err != nil {
			return setupFailure(err)
		}
		defer cancel()
		restorePath, err := bootstrap.ConfigureToolPath(opts.Root, testOpts)
		if err != nil {
			return err
		}
		defer restorePath()
		runner := newCommandOutputRunner(opts.Stdout, opts.Runner, testOpts.Verbose)
		switch name {
		case "up":
			return runClusterUp(ctx, opts.Stdout, opts.Root, runner, testOpts)
		case "deploy":
			return runClusterDeploy(ctx, opts.Stdout, opts.Root, runner, testOpts)
		case "deploy-dcgm":
			return runClusterDeployDCGM(ctx, opts.Stdout, opts.Root, runner, testOpts)
		default:
			return fmt.Errorf("unsupported cluster lifecycle command %q", name)
		}
	}
	return command
}

// clusterLifecycleFlags removes bootstrap options and describes the lifecycle timeout.
func clusterLifecycleFlags(flags []urfavecli.Flag) []urfavecli.Flag {
	lifecycleFlags := flags[:0]
	for _, flag := range flags {
		if clusterBootstrapFlag(flag.Names()[0]) {
			continue
		}
		if timeoutFlag, ok := flag.(*urfavecli.StringFlag); ok && timeoutFlag.Name == "timeout" {
			timeoutFlag.Usage = "bound cluster preparation or deployment to a Go `duration` (zero means unlimited)"
		}
		lifecycleFlags = append(lifecycleFlags, flag)
	}
	return lifecycleFlags
}

// clusterBootstrapFlag reports whether a flag controls the tests bootstrap workflow.
func clusterBootstrapFlag(name string) bool {
	switch name {
	case "bootstrap", "bootstrap-component", "bootstrap-only":
		return true
	default:
		return false
	}
}

// newClusterCLICommand defines flags shared by cluster inspection and cleanup commands.
func newClusterCLICommand(name string, cfg *cluster.Config, includeFollow bool) *urfavecli.Command {
	command := &urfavecli.Command{
		Name:      name,
		Usage:     clusterCommandUsage(name),
		UsageText: "e2e cluster " + name + " [options]",
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{Name: "run-name", EnvVars: []string{"E2E_RUN_NAME"}, Value: cfg.RunName, Destination: &cfg.RunName, Usage: "name used to derive and own resources for this test run"},
			&urfavecli.StringFlag{Name: "k3d-cluster-name", EnvVars: []string{"E2E_K3D_CLUSTER_NAME"}, Value: cfg.ClusterName, Destination: &cfg.ClusterName, Usage: "owned k3d cluster `name`"},
			&urfavecli.StringFlag{Name: testsFlagHelmNamespace, EnvVars: []string{"E2E_HELM_NAMESPACE"}, Value: cfg.Namespace, Destination: &cfg.Namespace, Usage: "dcgm-exporter Kubernetes `namespace`"},
			&urfavecli.StringFlag{Name: "helm-release-name", Aliases: []string{"helm-release"}, EnvVars: []string{"E2E_HELM_RELEASE_NAME"}, Value: cfg.ReleaseName, Destination: &cfg.ReleaseName, Usage: "dcgm-exporter Helm release `name`"},
			&urfavecli.StringFlag{Name: testsFlagDCGMNamespace, EnvVars: []string{"E2E_DCGM_NAMESPACE"}, Value: cfg.DCGMNamespace, Destination: &cfg.DCGMNamespace, Usage: "standalone DCGM Kubernetes `namespace`"},
			&urfavecli.StringFlag{Name: "kubeconfig", EnvVars: []string{"E2E_KUBECONFIG"}, Value: cfg.Kubeconfig, Destination: &cfg.Kubeconfig, Usage: "use an existing Kubernetes cluster through `path`"},
		},
		Action: func(c *urfavecli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unexpected e2e cluster %s arguments: %v", name, c.Args().Slice())
			}
			return applyRunNameDefaults(c, cfg.RunName, runResourceNames{
				cluster:       &cfg.ClusterName,
				namespace:     &cfg.Namespace,
				release:       &cfg.ReleaseName,
				dcgmNamespace: &cfg.DCGMNamespace,
			})
		},
	}
	if includeFollow {
		command.Flags = append(command.Flags, &urfavecli.BoolFlag{Name: "follow", Destination: &cfg.Follow, Usage: "follow exporter and standalone DCGM logs"})
	}
	return command
}

// newClusterConfigCommand runs inspection and cleanup operations with shared cluster flags.
func newClusterConfigCommand(name string, opts Options) *urfavecli.Command {
	cfg := cluster.DefaultConfig()
	command := newClusterCLICommand(name, &cfg, name == "logs")
	parseAction := command.Action
	command.Action = func(c *urfavecli.Context) error {
		if err := parseAction(c); err != nil {
			return err
		}
		cfg.Root = opts.Root
		if cfg.Kubeconfig == "" {
			cfg.LocalKubeconfig = opts.Root + "/.local-e2e/kubeconfig-" + cfg.ClusterName + ".yaml"
		}
		restorePath, err := bootstrap.ConfigureToolPath(opts.Root, config.TestOptions{})
		if err != nil {
			return err
		}
		defer restorePath()
		switch name {
		case "status":
			ctx, cancel := context.WithTimeout(c.Context, clusterInfoTimeout)
			defer cancel()
			return setupFailure(cluster.Status(ctx, opts.Runner, opts.Stdout, cfg))
		case "logs":
			ctx := c.Context
			cancel := func() {}
			if !cfg.Follow {
				ctx, cancel = context.WithTimeout(c.Context, clusterInfoTimeout)
			}
			defer cancel()
			return setupFailure(cluster.Logs(ctx, opts.Runner, opts.Stdout, cfg))
		case "cleanup":
			ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context), clusterInfoTimeout)
			defer cancel()
			return setupFailure(cluster.Cleanup(ctx, opts.Runner, opts.Stdout, cfg))
		default:
			return fmt.Errorf("unsupported cluster configuration command %q", name)
		}
	}
	return command
}

// clusterCommandUsage returns the one-line description for a cluster lifecycle command.
func clusterCommandUsage(name string) string {
	switch name {
	case "up":
		return "Create or repair the local GPU k3d cluster"
	case "deploy":
		return "Deploy dcgm-exporter to the target cluster"
	case "deploy-dcgm":
		return "Deploy standalone DCGM to the target cluster"
	case "status":
		return "Print cluster, exporter, and DCGM status"
	case "logs":
		return "Print exporter and DCGM logs"
	case "cleanup":
		return "Remove e2e-managed cluster resources"
	default:
		return "Manage e2e cluster resources"
	}
}

// runResourceNames points at the resource names a run name can derive.
type runResourceNames struct {
	cluster       *string
	namespace     *string
	release       *string
	dcgmNamespace *string
}

// applyRunNameDefaults derives resource names from --run-name unless their own
// flag is set, then derives the standalone DCGM namespace from an explicit
// --helm-namespace when --dcgm-namespace is not set.
func applyRunNameDefaults(c *urfavecli.Context, runName string, names runResourceNames) error {
	if c.IsSet("run-name") {
		runConfig, err := cluster.ConfigForRunName(runName)
		if err != nil {
			return err
		}
		setUnlessFlagSet(c, names.cluster, runConfig.ClusterName, "k3d-cluster-name")
		setUnlessFlagSet(c, names.namespace, runConfig.Namespace, testsFlagHelmNamespace)
		setUnlessFlagSet(c, names.release, runConfig.ReleaseName, "helm-release-name", "helm-release")
		setUnlessFlagSet(c, names.dcgmNamespace, runConfig.DCGMNamespace, testsFlagDCGMNamespace)
	}
	if c.IsSet(testsFlagHelmNamespace) && !c.IsSet(testsFlagDCGMNamespace) {
		*names.dcgmNamespace = *names.namespace + "-dcgm"
	}
	return nil
}

// setUnlessFlagSet assigns value when none of the named flags was set explicitly.
func setUnlessFlagSet(c *urfavecli.Context, target *string, value string, flags ...string) {
	for _, flag := range flags {
		if c.IsSet(flag) {
			return
		}
	}
	*target = value
}
