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
	"io"
	"os"

	urfavecli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/installdeps"
)

// runCluster dispatches cluster lifecycle commands after installing the packaged tool path.
func runCluster(ctx context.Context, args []string, stdout, stderr io.Writer, root string, runner e2eexec.Runner) error {
	if len(args) == 0 || helpRequested(args) {
		writeClusterHelp(stdout)
		return nil
	}
	if len(args) > 1 && helpRequested(args[1:]) {
		writeClusterCommandHelp(stdout, args[0])
		return nil
	}
	restorePath, err := installdeps.ConfigureToolPath(root, config.Tests{})
	if err != nil {
		return err
	}
	defer restorePath()
	switch args[0] {
	case "check":
		return cluster.CheckLocal(ctx, runner, stdout)
	case "up":
		opts, err := parseTests(args[1:], stderr)
		if err != nil {
			return err
		}
		runner = newOutputRunner(stdout, runner, opts.Verbose)
		return runClusterUp(ctx, stdout, root, runner, opts)
	case "deploy":
		opts, err := parseTests(args[1:], stderr)
		if err != nil {
			return err
		}
		runner = newOutputRunner(stdout, runner, opts.Verbose)
		return runClusterDeploy(ctx, stdout, root, runner, opts)
	case "deploy-dcgm":
		opts, err := parseTests(args[1:], stderr)
		if err != nil {
			return err
		}
		runner = newOutputRunner(stdout, runner, opts.Verbose)
		return runClusterDeployDCGM(ctx, stdout, root, runner, opts)
	case "status":
		cfg, err := parseCluster(args[1:], stderr, false, root)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, clusterInfoTimeout)
		defer cancel()
		return setupFailure(cluster.Status(ctx, runner, stdout, cfg))
	case "logs":
		cfg, err := parseCluster(args[1:], stderr, true, root)
		if err != nil {
			return err
		}
		logCtx := ctx
		cancel := func() {}
		if !cfg.Follow {
			logCtx, cancel = context.WithTimeout(ctx, clusterInfoTimeout)
		}
		defer cancel()
		return setupFailure(cluster.Logs(logCtx, runner, stdout, cfg))
	case "cleanup":
		cfg, err := parseCluster(args[1:], stderr, false, root)
		if err != nil {
			return err
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
		defer cancel()
		return setupFailure(cluster.Cleanup(cleanupCtx, runner, stdout, cfg))
	default:
		return fmt.Errorf("unknown e2e cluster command %q", args[0])
	}
}

// writeClusterHelp prints the cluster command index; each command renders its own flag help.
func writeClusterHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: e2e cluster <command> [options]

Commands:
  check              Verify local cluster tools are available
  up                 Create or repair the local GPU k3d cluster
  deploy             Deploy dcgm-exporter to the target cluster
  deploy-dcgm        Deploy standalone DCGM to the target cluster
  status             Print cluster, exporter, and DCGM status
  logs               Print exporter and DCGM logs
  cleanup            Remove e2e-managed local cluster or external namespaces

Run "e2e cluster <command> --help" for command options.
`)
}

// writeClusterCommandHelp renders a cluster subcommand from its parser-owned flag definition.
func writeClusterCommandHelp(w io.Writer, name string) {
	var command *urfavecli.Command
	switch name {
	case "up", "deploy", "deploy-dcgm":
		opts := defaultTestsConfig()
		command = newTestsCLICommand(&opts)
		command.Name = name
		command.HelpName = "e2e cluster " + name
		command.UsageText = command.HelpName + " [options]"
		command.Description = ""
	case "status", "logs", "cleanup":
		cfg := cluster.DefaultConfig()
		command = newClusterCLICommand(name, &cfg, name == "logs", false)
	case "check":
		command = &urfavecli.Command{
			Name:      name,
			HelpName:  "e2e cluster check",
			Usage:     "Verify local cluster tools are available",
			UsageText: "e2e cluster check",
		}
	default:
		writeClusterHelp(w)
		return
	}
	helpCategory := ""
	if name == "up" || name == "deploy" || name == "deploy-dcgm" {
		helpCategory = testsCategoryExecution
	}
	command.Flags = append(command.Flags, &urfavecli.BoolFlag{Name: "help", Aliases: []string{"h"}, Usage: "show help", Category: helpCategory})
	urfavecli.HelpPrinter(w, urfavecli.CommandHelpTemplate, command)
}

// parseCluster merges cluster command flags with environment defaults.
func parseCluster(args []string, stderr io.Writer, includeFollow bool, root string) (cluster.Config, error) {
	cfg := cluster.DefaultConfig()
	if value := os.Getenv("E2E_K3D_CLUSTER_NAME"); value != "" {
		cfg.ClusterName = value
	}
	if value := os.Getenv("E2E_HELM_NAMESPACE"); value != "" {
		cfg.Namespace = value
		cfg.DCGMNamespace = value + "-dcgm"
	}
	if value := os.Getenv("E2E_HELM_RELEASE_NAME"); value != "" {
		cfg.ReleaseName = value
	}
	dcgmNamespaceSet := false
	if value := os.Getenv("E2E_DCGM_NAMESPACE"); value != "" {
		cfg.DCGMNamespace = value
		dcgmNamespaceSet = true
	}
	if value := os.Getenv("E2E_KUBECONFIG"); value != "" {
		cfg.Kubeconfig = value
	}
	command := newClusterCLICommand("cluster", &cfg, includeFollow, dcgmNamespaceSet)
	app := urfavecli.NewApp()
	app.Name = "e2e"
	app.Writer = io.Discard
	app.ErrWriter = stderr
	app.HideHelp = true
	app.Commands = []*urfavecli.Command{command}
	if err := app.Run(append([]string{"e2e", "cluster"}, args...)); err != nil {
		return cfg, err
	}
	if cfg.Kubeconfig == "" {
		cfg.LocalKubeconfig = root + "/.local-e2e/kubeconfig-" + cfg.ClusterName + ".yaml"
	}
	return cfg, nil
}

// newClusterCLICommand defines flags shared by cluster inspection and cleanup commands.
func newClusterCLICommand(name string, cfg *cluster.Config, includeFollow, dcgmNamespaceSet bool) *urfavecli.Command {
	command := &urfavecli.Command{
		Name:      name,
		HelpName:  "e2e cluster " + name,
		Usage:     clusterCommandUsage(name),
		UsageText: "e2e cluster " + name + " [options]",
		HideHelp:  true,
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{Name: "k3d-cluster-name", EnvVars: []string{"E2E_K3D_CLUSTER_NAME"}, Value: cfg.ClusterName, Destination: &cfg.ClusterName, Usage: "owned k3d cluster `name`"},
			&urfavecli.StringFlag{Name: "helm-namespace", EnvVars: []string{"E2E_HELM_NAMESPACE"}, Value: cfg.Namespace, Destination: &cfg.Namespace, Usage: "dcgm-exporter Kubernetes `namespace`"},
			&urfavecli.StringFlag{Name: "helm-release-name", Aliases: []string{"helm-release"}, EnvVars: []string{"E2E_HELM_RELEASE_NAME"}, Value: cfg.ReleaseName, Destination: &cfg.ReleaseName, Usage: "dcgm-exporter Helm release `name`"},
			&urfavecli.StringFlag{Name: "dcgm-namespace", EnvVars: []string{"E2E_DCGM_NAMESPACE"}, Value: cfg.DCGMNamespace, Destination: &cfg.DCGMNamespace, Usage: "standalone DCGM Kubernetes `namespace`"},
			&urfavecli.StringFlag{Name: "kubeconfig", EnvVars: []string{"E2E_KUBECONFIG"}, Value: cfg.Kubeconfig, Destination: &cfg.Kubeconfig, Usage: "use an existing Kubernetes cluster through `path`"},
		},
		Action: func(c *urfavecli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unexpected e2e cluster arguments: %v", c.Args().Slice())
			}
			if c.IsSet("helm-namespace") && !dcgmNamespaceSet && !c.IsSet("dcgm-namespace") {
				cfg.DCGMNamespace = cfg.Namespace + "-dcgm"
			}
			return nil
		},
	}
	if includeFollow {
		command.Flags = append(command.Flags, &urfavecli.BoolFlag{Name: "follow", Destination: &cfg.Follow, Usage: "follow exporter and standalone DCGM logs"})
	}
	return command
}

// clusterCommandUsage returns the one-line description for a cluster lifecycle command.
func clusterCommandUsage(name string) string {
	switch name {
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
