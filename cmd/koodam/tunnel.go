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

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/streaming/pkg/httpstream"
	"k8s.io/streaming/pkg/httpstream/spdy"
)

const (
	// sshPort is the port of sshd in the Workspace pod.
	sshPort = 22
	// portForwardProtocol is the SPDY sub-protocol of the port-forward API.
	portForwardProtocol = "portforward.k8s.io"
)

// errWait is the maximum time to wait for the error stream after the data stops.
const errWait = 2 * time.Second

// dialFunc opens a port-forward connection to a pod.
type dialFunc func(ctx context.Context, ns, pod string) (httpstream.Connection, error)

// tunnelCmd connects stdin and stdout to port 22 of the Workspace pod. OpenSSH uses it as a
// ProxyCommand. Stdout is the SSH data channel, so the command writes only the data to it.
func tunnelCmd(conn connect) *cobra.Command {
	return &cobra.Command{
		Use:   "tunnel NAMESPACE/WORKSPACE",
		Short: "Connect stdin and stdout to the SSH port of a Workspace (an OpenSSH ProxyCommand)",
		Long: "Connect stdin and stdout to port 22 of the pod WORKSPACE-0 through the Kubernetes API.\n" +
			"The command uses your kube context and your RBAC. It opens no local port.\n" +
			"You need create on pods/portforward and get on pods in the namespace.\n\n" +
			"Example ~/.ssh/config entry:\n  ProxyCommand koodam tunnel dev-mohan/main",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns, ws, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			e, err := conn()
			if err != nil {
				return err
			}
			return e.tunnel(cmd.Context(), ns, ws, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

// parseTarget splits NAMESPACE/WORKSPACE. The error does not show the argument, because it can
// hold control characters.
func parseTarget(arg string) (ns, ws string, err error) {
	parts := strings.Split(arg, "/")
	if len(parts) != 2 {
		return "", "", errors.New("the argument must be NAMESPACE/WORKSPACE, with 1 slash")
	}
	for i, what := range []string{"namespace", "workspace name"} {
		if msgs := validation.IsDNS1123Label(parts[i]); len(msgs) > 0 {
			return "", "", fmt.Errorf("the %s is not a valid DNS-1123 label: use 1 to 63 lower case "+
				"letters, digits and '-', with a letter or digit at the start and at the end", what)
		}
	}
	return parts[0], parts[1], nil
}

// tunnel finds the Workspace pod, opens the port-forward and copies the data.
func (e *env) tunnel(ctx context.Context, ns, ws string, in io.Reader, out io.Writer) error {
	pod := ws + "-0"
	p, err := e.clientset.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("pod %s/%s does not exist: check the Workspace %s in namespace %s", ns, pod, ws, ns)
	}
	if err != nil {
		return apiError("get the Workspace pod", err)
	}
	if p.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("pod %s/%s is %s, not Running: check the Workspace %s", ns, pod, p.Status.Phase, ws)
	}
	if e.dial == nil {
		return errors.New("no connection to the API server for the port-forward")
	}
	c, err := e.dial(ctx, ns, pod)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return forward(ctx, c, in, out, errWait)
}

// forward copies the data of port 22 in the 2 directions. It stops when the remote side ends,
// when the connection closes, or when ctx ends. At the end of stdin, it closes the write side
// of the data stream, so that the remote side can end. After the data stops, it waits for the
// error stream for the time wait at most.
func forward(ctx context.Context, c httpstream.Connection, in io.Reader, out io.Writer, wait time.Duration) error {
	h := http.Header{}
	h.Set(corev1.StreamType, corev1.StreamTypeError)
	h.Set(corev1.PortHeader, strconv.Itoa(sshPort))
	h.Set(corev1.PortForwardRequestIDHeader, "0")
	errStream, err := c.CreateStream(h)
	if err != nil {
		return fmt.Errorf("open the error stream: %w", err)
	}
	// The client does not write to the error stream.
	_ = errStream.Close()
	errMsg := make(chan []byte, 1)
	go func() {
		msg, _ := io.ReadAll(errStream)
		errMsg <- msg
	}()

	h.Set(corev1.StreamType, corev1.StreamTypeData)
	data, err := c.CreateStream(h)
	if err != nil {
		return fmt.Errorf("open the data stream: %w", err)
	}
	remoteDone := make(chan error, 1)
	localErr := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, data)
		remoteDone <- err
	}()
	go func() {
		_, err := io.Copy(data, in)
		// Tell the remote side that no more data comes.
		_ = data.Close()
		if err != nil {
			localErr <- err
		}
	}()

	var copyErr error
	remoteEnded := false
	select {
	case copyErr = <-remoteDone:
		remoteEnded = true
	case copyErr = <-localErr:
	case <-c.CloseChan():
	case <-ctx.Done():
		return ctx.Err()
	}
	// Reset the data stream before the read of the error stream. Unsent data can block it.
	_ = data.Reset()
	// Wait for the error stream, also after a close of the connection, because the server can
	// send the error and then close. Do not wait with no end: a stdin error or a stopped server
	// can keep the error stream open.
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var msg []byte
	select {
	case msg = <-errMsg:
	case <-wctx.Done():
	}
	// Wait for the copy to stdout to end, so that it does not write after the return.
	if !remoteEnded {
		select {
		case <-remoteDone:
		case <-wctx.Done():
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(msg) > 0 {
		return fmt.Errorf("the port-forward to port %d failed: %s", sshPort, printable(string(msg)))
	}
	if copyErr != nil {
		return fmt.Errorf("copy the data: %s", printable(copyErr.Error()))
	}
	return nil
}

// spdyDial returns a dialFunc that opens a SPDY port-forward with the user's kubeconfig.
func spdyDial(cfg *rest.Config, cs kubernetes.Interface) dialFunc {
	return func(ctx context.Context, ns, pod string) (httpstream.Connection, error) {
		tlsCfg, err := rest.TLSConfigFor(cfg)
		if err != nil {
			return nil, errors.New("read the TLS settings of the kube context")
		}
		proxy := http.ProxyFromEnvironment
		if cfg.Proxy != nil {
			proxy = cfg.Proxy
		}
		upgrader, err := spdy.NewRoundTripperWithConfig(spdy.RoundTripperConfig{
			TLS: tlsCfg, Proxier: proxy, PingPeriod: 5 * time.Second})
		if err != nil {
			return nil, errors.New("make the SPDY transport")
		}
		// The wrappers add the credentials of the kube context to the request.
		rt, err := rest.HTTPWrappersForConfig(cfg, upgrader)
		if err != nil {
			return nil, errors.New("add the credentials of the kube context to the transport")
		}
		u := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).
			SubResource("portforward").URL()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
		if err != nil {
			return nil, errors.New("make the port-forward request")
		}
		req.Header.Add(httpstream.HeaderProtocolVersion, portForwardProtocol)
		// Do not follow a redirect: the request carries the credentials of the kube context.
		client := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		resp, err := client.Do(req)
		if err != nil {
			// The URL holds the server address from the kubeconfig. Give only the cause.
			if urlErr, ok := errors.AsType[*url.Error](err); ok {
				err = urlErr.Err
			}
			return nil, fmt.Errorf("connect to the API server: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, fmt.Errorf("the API server sent a redirect (HTTP %d): koodam does not follow it", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("the API server denied the port-forward (HTTP 403): you need create on "+
				"pods/portforward and get on pods in namespace %s", ns)
		}
		c, err := upgrader.NewConnection(resp)
		if err != nil {
			return nil, fmt.Errorf("open the port-forward (HTTP %d): %s", resp.StatusCode, printable(err.Error()))
		}
		return c, nil
	}
}

// printable removes control characters, so that a message from the server cannot change
// the terminal.
func printable(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
}
