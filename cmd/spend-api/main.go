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
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/appclaim"
	"github.com/mohanraj00/koodam/internal/opspage"
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
	viewers := flag.Bool("viewer-review", false,
		"Check Kubernetes tokens with TokenReview and show only the namespaces where the caller can list Agents.")
	viewerAudience := flag.String("viewer-audience", "", "A token audience that the API server must accept, for -viewer-review.")
	flag.Parse()

	if err := loadAuth(&c, *jwksFile, *staticFile, *viewers, *viewerAudience); err != nil {
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
	if err := serve(*listen, c.Timeout, opspage.Routes(handler)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadAuth(c *spendapi.Config, jwksFile, staticFile string, viewers bool, audience string) error {
	if !oneOf(jwksFile != "", staticFile != "", viewers) {
		return errors.New("give one of -jwks-file, -static-token-file or -viewer-review")
	}
	if audience != "" && !viewers {
		return errors.New("-viewer-audience needs -viewer-review")
	}
	if viewers {
		if c.Claims.Issuer != "" || c.Claims.Audience != "" || c.Claims.TenantClaim != "" {
			return errors.New("-viewer-review cannot be used with issuer settings")
		}
		c.Viewers = &spendapi.Viewers{}
		if audience != "" {
			c.Viewers.Audiences = []string{audience}
		}
		return nil
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

func oneOf(set ...bool) bool {
	n := 0
	for _, s := range set {
		if s {
			n++
		}
	}
	return n == 1
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
	clients, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return errors.New("the Kubernetes client could not start")
	}
	pods := clients.CoreV1()
	c.Logs = func(ctx context.Context, namespace, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
		return pods.Pods(namespace).GetLogs(pod, o).Stream(ctx)
	}
	if c.Viewers != nil {
		c.Viewers.Tokens = clients.AuthenticationV1().TokenReviews()
		c.Viewers.Access = clients.AuthorizationV1().SubjectAccessReviews()
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1alpha1.AddToScheme, batchv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			return errors.New("the Agent, Job and pod schemes could not be registered")
		}
	}
	kube, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme, Mapper: kubeMapper()})
	if err != nil {
		return errors.New("the Kubernetes client could not start")
	}
	c.Kube, c.Watch = kube, kube
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

// kubeMapper maps the kinds that the server reads. It is static, so the server needs no discovery
// access. Each kind that the server reads must be here, or the read fails before it goes to the API
// server.
func kubeMapper() meta.RESTMapper {
	gv := corev1alpha1.GroupVersion
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gv, batchv1.SchemeGroupVersion, corev1.SchemeGroupVersion})
	mapper.AddSpecific(batchv1.SchemeGroupVersion.WithKind("Job"), batchv1.SchemeGroupVersion.WithResource("jobs"),
		batchv1.SchemeGroupVersion.WithResource("job"), meta.RESTScopeNamespace)
	mapper.AddSpecific(corev1.SchemeGroupVersion.WithKind("Pod"), corev1.SchemeGroupVersion.WithResource("pods"),
		corev1.SchemeGroupVersion.WithResource("pod"), meta.RESTScopeNamespace)
	mapper.AddSpecific(gv.WithKind("Agent"), gv.WithResource("agents"), gv.WithResource("agent"), meta.RESTScopeNamespace)
	mapper.AddSpecific(gv.WithKind("SpendBudget"), gv.WithResource("spendbudgets"), gv.WithResource("spendbudget"),
		meta.RESTScopeNamespace)
	return mapper
}
