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

// Command conversion serves the CRD conversion webhook of #511. The API server calls it to convert
// an object of the group core.koodam.dev between v1alpha1 and v1beta1.
//
// The process can change each object that it converts. Thus it is small: it has no API client, no
// token, no gateway credential, no flag and no environment input. It writes only fixed messages,
// and no message has data from a request, an object, the certificate or an error.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/mohanraj00/koodam/internal/conversion"
)

// The approved production values. No flag, environment variable or API changes them.
const (
	certDir   = "/etc/koodam/conversion"
	servePort = 9443
)

// The messages are the only text that the binary writes.
const (
	msgStart  = "conversion: serving"
	msgStop   = "conversion: stopped"
	msgArgs   = "conversion: the binary accepts no argument"
	msgServe  = "conversion: the certificate did not load, or the server did not listen or serve"
	msgFailed = "conversion: refused a request: "
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, out io.Writer) int {
	quiet()
	if len(args) != 0 {
		say(out, msgArgs)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	say(out, msgStart)
	if err := newServer(certDir, servePort, out).Start(ctx); err != nil {
		// The error can have file data, so it is not in the message.
		say(out, msgServe)
		return 1
	}
	say(out, msgStop)
	return 0
}

// say writes one fixed line. A write failure is ignored, because no other output can report it.
func say(out io.Writer, message string) {
	_, _ = fmt.Fprintln(out, message)
}

// quiet discards the standard logger, the controller-runtime logger and klog. These loggers can
// write an error text, and an error text can have data from a request.
func quiet() {
	log.SetOutput(io.Discard)
	log.SetFlags(0)
	ctrllog.SetLogger(logr.Discard())
	klog.SetLogger(logr.Discard())
}

// newServer returns the conversion server. A refusal writes one fixed line with its fixed reason.
func newServer(dir string, port int, out io.Writer) webhook.Server {
	return conversion.NewServer("", port, dir, func(reason string) { say(out, msgFailed+reason) })
}
