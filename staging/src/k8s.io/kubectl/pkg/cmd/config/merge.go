/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/clientcmd/api/latest"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/kubectl/pkg/util/i18n"
	"k8s.io/kubectl/pkg/util/templates"
)

type mergeOptions struct {
	PrintFlags  *genericclioptions.PrintFlags
	PrintObject printers.ResourcePrinterFunc

	ConfigAccess   clientcmd.ConfigAccess
	SourceFile     string
	Flatten        bool
	DryRunStrategy cmdutil.DryRunStrategy

	genericiooptions.IOStreams
}

var (
	mergeLong = templates.LongDesc(i18n.T(`
		Merge a kubeconfig file into the current kubeconfig.

		Clusters, contexts, and users defined in SOURCE_FILE are added to the current kubeconfig. Entries whose
		names collide with existing entries are overwritten by the ones from SOURCE_FILE. The current-context is
		left unchanged.`))

	mergeExample = templates.Examples(`
		# Merge config.yaml into the current kubeconfig
		kubectl config merge config.yaml

		# Merge config.yaml and flatten the result into a self-contained kubeconfig
		kubectl config merge config.yaml --flatten

		# Preview the result of merging config.yaml without writing it
		kubectl config merge config.yaml --dry-run=client -o yaml`)
)

// NewCmdConfigMerge returns a Command instance for 'config merge' sub command
func NewCmdConfigMerge(streams genericiooptions.IOStreams, configAccess clientcmd.ConfigAccess) *cobra.Command {
	o := &mergeOptions{
		PrintFlags:   genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme).WithDefaultOutput("yaml"),
		ConfigAccess: configAccess,
		IOStreams:    streams,
	}

	cmd := &cobra.Command{
		Use:     "merge SOURCE_FILE",
		Short:   i18n.T("Merge a kubeconfig file into the current kubeconfig"),
		Long:    mergeLong,
		Example: mergeExample,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.complete(cmd, args))
			cmdutil.CheckErr(o.validate())
			cmdutil.CheckErr(o.run())
		},
	}

	o.PrintFlags.AddFlags(cmd)
	cmd.Flags().BoolVar(&o.Flatten, "flatten", o.Flatten, "Flatten the resulting kubeconfig into self-contained output")
	cmdutil.AddDryRunFlag(cmd)

	return cmd
}

func (o *mergeOptions) complete(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return cmdutil.UsageErrorf(cmd, "SOURCE_FILE is required")
	}
	o.SourceFile = args[0]

	dryRunStrategy, err := cmdutil.GetDryRunStrategy(cmd)
	if err != nil {
		return err
	}
	if dryRunStrategy == cmdutil.DryRunServer {
		return errors.New("kubectl config merge does not talk to the API server; use --dry-run=client instead")
	}
	o.DryRunStrategy = dryRunStrategy

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return err
	}
	o.PrintObject = printer.PrintObj

	return nil
}

func (o mergeOptions) validate() error {
	if len(o.SourceFile) == 0 {
		return errors.New("a source kubeconfig file must be specified")
	}
	return nil
}

func (o mergeOptions) run() error {
	destConfig, err := o.ConfigAccess.GetStartingConfig()
	if err != nil {
		return err
	}

	srcConfig, err := clientcmd.LoadFromFile(o.SourceFile)
	if err != nil {
		return err
	}

	for name, cluster := range srcConfig.Clusters {
		merged := *cluster
		if existing, ok := destConfig.Clusters[name]; ok {
			merged.LocationOfOrigin = existing.LocationOfOrigin
		} else {
			merged.LocationOfOrigin = ""
		}
		destConfig.Clusters[name] = &merged
	}
	for name, authInfo := range srcConfig.AuthInfos {
		merged := *authInfo
		if existing, ok := destConfig.AuthInfos[name]; ok {
			merged.LocationOfOrigin = existing.LocationOfOrigin
		} else {
			merged.LocationOfOrigin = ""
		}
		destConfig.AuthInfos[name] = &merged
	}
	for name, context := range srcConfig.Contexts {
		merged := *context
		if existing, ok := destConfig.Contexts[name]; ok {
			merged.LocationOfOrigin = existing.LocationOfOrigin
		} else {
			merged.LocationOfOrigin = ""
		}
		destConfig.Contexts[name] = &merged
	}

	if o.Flatten {
		if err := clientcmdapi.FlattenConfig(destConfig); err != nil {
			return err
		}
	}

	if o.DryRunStrategy == cmdutil.DryRunClient {
		convertedObj, err := latest.Scheme.ConvertToVersion(destConfig, latest.ExternalVersion)
		if err != nil {
			return err
		}
		return o.PrintObject(convertedObj, o.Out)
	}

	if err := clientcmd.ModifyConfig(o.ConfigAccess, *destConfig, true); err != nil {
		return err
	}

	fmt.Fprintf(o.Out, "Merged %q into the current kubeconfig.\n", o.SourceFile)
	return nil
}
