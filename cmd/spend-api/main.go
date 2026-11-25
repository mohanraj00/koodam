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

// Command spend-api serves read-only spend data for Agents.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/appclaim"
	"github.com/mohanraj00/koodam/internal/spendapi"
)

func main() {
	var c spendapi.Config
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	listen := flag.String("listen", "127.0.0.1:8080", "The HTTP listen address.")
	flag.StringVar(&rules.ExplicitPath, "kubeconfig", "", "The path to the kubeconfig file.")
	namespaces := flag.String("namespace", "", "The Agent namespaces, separated by commas. Defaults to the Kubernetes client namespace.")
	flag.StringVar(&c.GatewayURL, "gateway-url", "", "The LiteLLM gateway URL.")
	flag.DurationVar(&c.Timeout, "request-timeout", 5*time.Second, "The time limit of a request.")
	masterFile := flag.String("master-key-file", "", "The file with the LiteLLM master key.")
	flag.StringVar(&c.Claims.Issuer, "issuer", "", "The necessary value of the claim iss.")
	flag.StringVar(&c.Claims.Audience, "audience", "", "A value that the claim aud must have.")
	flag.StringVar(&c.Claims.CallerClaim, "caller-claim", "sub", "The claim with the caller label.")
	flag.StringVar(&c.Claims.TenantClaim, "tenant-claim", "", "The claim with the tenant label.")
	jwksFile := flag.String("jwks-file", "", "The file with the JWKS of the issuer.")
	staticFile := flag.String("static-token-file", "", "The file with local bearer tokens, one per line.")
	flag.Parse()

	if err := loadAuth(&c, *jwksFile, *staticFile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	master, err := os.ReadFile(*masterFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "The master key file is not readable.")
		os.Exit(2)
	}
	c.MasterKey = strings.TrimSpace(string(master))

	if *namespaces != "" {
		c.Namespaces = strings.Split(*namespaces, ",")
	}
	if err := setupKube(&c, rules); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	handler, err := spendapi.New(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := serve(*listen, c.Timeout, handler); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadAuth(c *spendapi.Config, jwksFile, staticFile string) error {
	if (jwksFile == "") == (staticFile == "") {
		return errors.New("give one of -jwks-file or -static-token-file")
	}
	if staticFile != "" {
		if c.Claims.Issuer != "" || c.Claims.Audience != "" || c.Claims.TenantClaim != "" {
			return errors.New("the local token file cannot be used with issuer settings")
		}
		data, err := os.ReadFile(staticFile)
		if err != nil {
			return errors.New("the local token file is not readable")
		}
		c.StaticTokens = strings.Fields(string(data))
		return nil
	}
	data, err := os.ReadFile(jwksFile)
	if err != nil {
		return errors.New("the JWKS file is not readable")
	}
	c.Keys, err = appclaim.ParseKeySet(data)
	return err
}

func setupKube(c *spendapi.Config, rules *clientcmd.ClientConfigLoadingRules) error {
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	cfg, err := cc.ClientConfig()
	if err != nil {
		return errors.New("the Kubernetes client configuration is not valid")
	}
	if len(c.Namespaces) == 0 {
		namespace, _, err := cc.Namespace()
		if err != nil {
			return errors.New("the Kubernetes client namespace is not available")
		}
		c.Namespaces = []string{namespace}
	}
	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		return errors.New("the Agent scheme could not be registered")
	}
	gv := corev1alpha1.GroupVersion
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gv})
	mapper.AddSpecific(gv.WithKind("Agent"), gv.WithResource("agents"), gv.WithResource("agent"), meta.RESTScopeNamespace)
	c.Kube, err = client.New(cfg, client.Options{Scheme: scheme, Mapper: mapper})
	if err != nil {
		return errors.New("the Kubernetes client could not start")
	}
	return nil
}

func serve(address string, timeout time.Duration, handler http.Handler) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	srv := &http.Server{
		Handler: handler, DisableGeneralOptionsHandler: true,
		ReadTimeout: 5 * time.Second, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: timeout + 5*time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10,
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return errors.New("the spend API listener did not start")
	}
	fail := make(chan error, 1)
	go func() { fail <- srv.Serve(listener) }()
	select {
	case <-fail:
		return errors.New("the spend API listener stopped")
	case <-ctx.Done():
		end, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(end); err != nil {
			return errors.New("the spend API did not stop before its shutdown time limit")
		}
		return nil
	}
}
