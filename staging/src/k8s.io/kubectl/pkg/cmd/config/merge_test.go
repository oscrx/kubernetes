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
	"bytes"
	"os"
	"strings"
	"testing"

	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	utiltesting "k8s.io/client-go/util/testing"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

func TestMerge(t *testing.T) {
	destConfig := clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"minikube": {Server: "https://192.168.99.100:8443"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"minikube": {AuthInfo: "minikube", Cluster: "minikube"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"minikube": {Token: "old-token"},
		},
		CurrentContext: "minikube",
	}
	srcConfig := clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"minikube":   {Server: "https://192.168.99.101:8443"},
			"my-cluster": {Server: "https://192.168.0.1:3434"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"my-cluster": {AuthInfo: "my-cluster", Cluster: "my-cluster"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"my-cluster": {Token: "new-token"},
		},
		CurrentContext: "my-cluster",
	}

	destFile, err := os.CreateTemp(os.TempDir(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer utiltesting.CloseAndRemove(t, destFile)
	if err := clientcmd.WriteToFile(destConfig, destFile.Name()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srcFile, err := os.CreateTemp(os.TempDir(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer utiltesting.CloseAndRemove(t, srcFile)
	if err := clientcmd.WriteToFile(srcConfig, srcFile.Name()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pathOptions := clientcmd.NewDefaultPathOptions()
	pathOptions.GlobalFile = destFile.Name()
	pathOptions.EnvVar = ""

	streams, _, out, _ := genericiooptions.NewTestIOStreams()
	cmd := NewCmdConfigMerge(streams, pathOptions)
	cmd.SetArgs([]string{srcFile.Name()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error executing command: %v", err)
	}

	expectedOut := `Merged "` + srcFile.Name() + `" into the current kubeconfig.` + "\n"
	if out.String() != expectedOut {
		t.Errorf("expected output %q, got %q", expectedOut, out.String())
	}

	merged, err := clientcmd.LoadFromFile(destFile.Name())
	if err != nil {
		t.Fatalf("unexpected error loading merged kubeconfig: %v", err)
	}

	if len(merged.Clusters) != 2 {
		t.Errorf("expected 2 clusters, got %d", len(merged.Clusters))
	}
	if merged.Clusters["minikube"].Server != "https://192.168.99.101:8443" {
		t.Errorf("expected source cluster to overwrite existing entry, got %q", merged.Clusters["minikube"].Server)
	}
	if merged.Clusters["my-cluster"].Server != "https://192.168.0.1:3434" {
		t.Errorf("expected new cluster to be added, got %q", merged.Clusters["my-cluster"].Server)
	}
	if merged.AuthInfos["minikube"].Token != "old-token" {
		t.Errorf("expected untouched user to be preserved, got %q", merged.AuthInfos["minikube"].Token)
	}
	if merged.CurrentContext != "minikube" {
		t.Errorf("expected current-context to be left unchanged, got %q", merged.CurrentContext)
	}
}

func TestMergeDryRun(t *testing.T) {
	destConfig := clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"minikube": {Server: "https://192.168.99.100:8443"},
		},
		CurrentContext: "minikube",
	}
	srcConfig := clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"my-cluster": {Server: "https://192.168.0.1:3434"},
		},
	}

	destFile, err := os.CreateTemp(os.TempDir(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer utiltesting.CloseAndRemove(t, destFile)
	if err := clientcmd.WriteToFile(destConfig, destFile.Name()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srcFile, err := os.CreateTemp(os.TempDir(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer utiltesting.CloseAndRemove(t, srcFile)
	if err := clientcmd.WriteToFile(srcConfig, srcFile.Name()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pathOptions := clientcmd.NewDefaultPathOptions()
	pathOptions.GlobalFile = destFile.Name()
	pathOptions.EnvVar = ""

	streams, _, out, _ := genericiooptions.NewTestIOStreams()
	cmd := NewCmdConfigMerge(streams, pathOptions)
	cmd.SetArgs([]string{srcFile.Name(), "--dry-run=client", "-o", "yaml"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error executing command: %v", err)
	}

	if !bytes.Contains(out.Bytes(), []byte("my-cluster")) {
		t.Errorf("expected dry-run output to contain merged cluster, got %q", out.String())
	}

	unchanged, err := clientcmd.LoadFromFile(destFile.Name())
	if err != nil {
		t.Fatalf("unexpected error loading kubeconfig: %v", err)
	}
	if len(unchanged.Clusters) != 1 {
		t.Errorf("expected dry-run not to write to disk, found %d clusters", len(unchanged.Clusters))
	}
}

func TestMergeDryRunServerRejected(t *testing.T) {
	destFile, err := os.CreateTemp(os.TempDir(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer utiltesting.CloseAndRemove(t, destFile)
	if err := clientcmd.WriteToFile(clientcmdapi.Config{}, destFile.Name()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srcFile, err := os.CreateTemp(os.TempDir(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer utiltesting.CloseAndRemove(t, srcFile)
	if err := clientcmd.WriteToFile(clientcmdapi.Config{}, srcFile.Name()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pathOptions := clientcmd.NewDefaultPathOptions()
	pathOptions.GlobalFile = destFile.Name()
	pathOptions.EnvVar = ""

	streams, _, _, errOut := genericiooptions.NewTestIOStreams()
	cmd := NewCmdConfigMerge(streams, pathOptions)
	cmd.SetArgs([]string{srcFile.Name(), "--dry-run=server"})

	defer cmdutil.DefaultBehaviorOnFatal()
	sawFatal := false
	cmdutil.BehaviorOnFatal(func(msg string, code int) {
		sawFatal = true
		if code != 1 {
			t.Errorf("expected exit code 1, got %d", code)
		}
		if !strings.Contains(msg, "does not talk to the API server") {
			t.Errorf("expected error about --dry-run=server not being supported, got %q", msg)
		}
	})

	_ = cmd.Execute()

	if !sawFatal {
		t.Fatalf("expected --dry-run=server to be rejected, got stderr %q", errOut.String())
	}
}
