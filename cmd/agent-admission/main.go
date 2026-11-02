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

// Command agent-admission serves the Agent ingress admission of #147. With no argument, it is a
// supervisor. The supervisor reads no TLS or request data. It starts the same binary in the private
// serving mode, sends the child stdout and stderr to the null device, forwards SIGTERM and SIGINT,
// waits for the child and writes only fixed messages. The serving child discards all logs, loads
// the TLS certificate one time and serves only the strict Agent validator. A restart rotates the
// certificate. No install path starts this binary yet.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	agentwebhook "github.com/mohanraj00/koodam/internal/webhook/v1alpha1"
)

// serveArg selects the private serving mode. Only the supervisor gives it.
const serveArg = "--internal-serve-agent-admission"

// The approved production values. No flag, environment variable or API changes them.
const (
	certFile    = "/etc/koodam/agent-admission/tls.crt"
	keyFile     = "/etc/koodam/agent-admission/tls.key"
	webhookPath = "/validate-core-koodam-dev-v1alpha1-agent-ingress"
	servePort   = 9443
	alpnHTTP1   = "http/1.1"
)

// The exit codes. The child uses codeCert and codeServe. The supervisor reads only the exit code
// of the child, never its output.
const (
	codeOK    = 0
	codeArgs  = 2
	codeStart = 3
	codeChild = 4
	codeCert  = 5
	codeServe = 6
)

// messages has the only text that the binary writes. No message contains a value from the input,
// the certificate, the child or an error.
var messages = map[int]string{
	codeOK:    "agent-admission: stopped",
	codeArgs:  "agent-admission: the arguments are not valid",
	codeStart: "agent-admission: the serving process did not start",
	codeChild: "agent-admission: the serving process stopped with a failure",
	codeCert:  "agent-admission: the TLS certificate did not load",
	codeServe: "agent-admission: the server did not listen or serve",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run selects the mode. It does not use the flag package, because a flag error writes the input.
func run(args []string, out io.Writer) int {
	quiet()
	switch {
	case len(args) == 0:
		// Register the signals before the child starts, so that no signal is lost.
		sigs := make(chan os.Signal, 2)
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
		exe, err := os.Executable()
		if err != nil {
			return report(out, codeStart)
		}
		return supervise(exe, []string{}, sigs, out)
	case len(args) == 1 && args[0] == serveArg:
		return serve()
	default:
		return report(out, codeArgs)
	}
}

// report writes the fixed message of code and returns code.
func report(out io.Writer, code int) int {
	fmt.Fprintln(out, messages[code])
	return code
}

// quiet discards the standard logger, the controller-runtime logger and klog. It runs before the
// certificate load, Register and Start.
func quiet() {
	log.SetOutput(io.Discard)
	log.SetFlags(0)
	ctrllog.SetLogger(logr.Discard())
	klog.SetLogger(logr.Discard())
}

// supervise starts exe in the serving mode with the environment env, and waits until the child
// stops. It forwards each signal from sigs. The child stdout and stderr go to the null device
// before the start, so the supervisor never gets child output. It applies no time limit: a time
// limit is not a revoke fence. The parameters are a test seam only. run always gives its own
// executable and an empty environment.
func supervise(exe string, env []string, sigs <-chan os.Signal, out io.Writer) int {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return report(out, codeStart)
	}
	defer func() { _ = null.Close() }()
	cmd := &exec.Cmd{Path: exe, Args: []string{exe, serveArg}, Env: env, Stdout: null, Stderr: null}
	if err := cmd.Start(); err != nil {
		return report(out, codeStart)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	stopping := false
	for {
		select {
		case sig := <-sigs:
			stopping = true
			// After the child stops, Signal returns an error. Wait still reaps the child.
			_ = cmd.Process.Signal(sig)
		case <-done:
			return report(out, childCode(cmd.ProcessState.ExitCode(), stopping))
		}
	}
}

// childCode maps the child exit code to a supervisor code. A child that stops after a forwarded
// signal, with code 0 or by the signal (-1), is a normal stop. Any other stop is a failure.
func childCode(exit int, stopping bool) int {
	switch {
	case stopping && (exit == codeOK || exit == -1):
		return codeOK
	case exit == codeCert || exit == codeServe:
		return exit
	default:
		return codeChild
	}
}

// serve is the serving child. Its output goes to the null device and quiet has discarded its logs.
func serve() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cert, ok := loadCertificate(certFile, keyFile)
	if !ok {
		return codeCert
	}
	if newServer(cert).Start(ctx) != nil {
		return codeServe
	}
	return codeOK
}

// loadCertificate reads the key pair one time. It drops the error, because an error can contain
// file data.
func loadCertificate(certPath, keyPath string) (*tls.Certificate, bool) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, false
	}
	return &cert, true
}

// newServer returns the controller-runtime webhook server with only the Agent validator. It has
// no manager, no API client and no client CA.
func newServer(cert *tls.Certificate) webhook.Server {
	srv := webhook.NewServer(webhook.Options{
		Port:    servePort,
		TLSOpts: []func(*tls.Config){tlsOptions(cert)},
	})
	srv.Register(webhookPath, agentwebhook.NewHandler())
	return srv
}

// tlsOptions sets TLS 1.2 or later and HTTP/1.1 only. It replaces the h2 default of the server.
// GetCertificate returns the certificate in memory. Because GetCertificate is set, the server
// starts no certificate watcher and reads no file.
func tlsOptions(cert *tls.Certificate) func(*tls.Config) {
	return func(c *tls.Config) {
		c.MinVersion = tls.VersionTLS12
		c.NextProtos = []string{alpnHTTP1}
		c.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil }
	}
}
