/*
Copyright 2026 Mohan Raj.

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

// Command koodam runs and inspects Agents. It reads only the Agent, its pods and the pod log.
// It never reads the key Secret.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// version is the version of the CLI. GoReleaser sets it with -X main.version.
var version = "dev"

// env is the connection to the cluster that each command uses.
type env struct {
	kube      client.Client
	clientset kubernetes.Interface
	namespace string
	// poll is the interval between two reads of the cluster.
	poll time.Duration
	// stream, if set, opens the pod log instead of the clientset. Tests use it.
	stream func(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)
	// dial opens a port-forward to a pod. Tests set a fake.
	dial dialFunc
}

// connect makes an env from the kubeconfig flags.
type connect func() (*env, error)

func main() {
	if err := newRoot(nil).Execute(); err != nil {
		os.Exit(1)
	}
}

// newRoot makes the root command. If conn is nil, the commands connect with the kubeconfig flags.
func newRoot(conn connect) *cobra.Command {
	root := &cobra.Command{
		Use:          "koodam",
		Short:        "Run an Agent and read its phase, log and spend",
		SilenceUsage: true,
		Version:      version,
	}
	root.SetVersionTemplate("koodam {{.Version}}\n")
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	root.PersistentFlags().StringVar(&rules.ExplicitPath, "kubeconfig", "", "path to the kubeconfig file")
	root.PersistentFlags().StringVar(&overrides.CurrentContext, "context", "", "kubeconfig context")
	root.PersistentFlags().StringVarP(&overrides.Context.Namespace, "namespace", "n", "", "namespace of the Agent")
	if conn == nil {
		conn = func() (*env, error) { return kubeEnv(rules, overrides) }
	}
	root.AddCommand(runCmd(conn), getCmd(conn, false), getCmd(conn, true), logsCmd(conn), deleteCmd(conn), tunnelCmd(conn), versionCmd())
	return root
}

// versionCmd prints the version of the CLI. It does not connect to the cluster.
func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the version of the CLI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "koodam %s\n", version)
			return err
		},
	}
}

func kubeEnv(rules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) (*env, error) {
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("read the kubeconfig: %w", err)
	}
	ns, _, err := cc.Namespace()
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &env{kube: kube, clientset: cs, namespace: ns, poll: 2 * time.Second, dial: spdyDial(cfg, cs)}, nil
}
