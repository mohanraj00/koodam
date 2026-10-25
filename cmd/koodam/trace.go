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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/mohanraj00/koodam/internal/egress"
)

// proxySelector selects the egress proxy pods of the chart.
const proxySelector = "app.kubernetes.io/name=koodam,app.kubernetes.io/component=egress-proxy"

// maxSpanLine is the maximum length of a log line. A longer line stops the read with an error.
const maxSpanLine = 1 << 20

// traceTarget is the Agent or the Workspace of the trace argument. If kind is empty, a span of
// an Agent or a Workspace with the name matches.
type traceTarget struct {
	kind, name string
	// since and until are the time window: since <= time < until. A zero value has no limit.
	since, until time.Time
}

// parseTraceTarget reads NAME, agent/NAME or workspace/NAME. The error does not show the
// argument, because it can hold control characters.
func parseTraceTarget(arg string) (traceTarget, error) {
	kind, name, ok := strings.Cut(arg, "/")
	if !ok {
		kind, name = "", arg
	} else if kind != "agent" && kind != "workspace" {
		return traceTarget{}, errors.New("the argument must be NAME, agent/NAME or workspace/NAME")
	}
	if len(validation.IsDNS1123Subdomain(name)) > 0 {
		return traceTarget{}, errors.New("the name is not a valid DNS-1123 subdomain")
	}
	return traceTarget{kind: kind, name: name}, nil
}

// match returns true if the span is from the target in the namespace ns and in the time window.
func (t traceTarget) match(sp *egress.Span, ns string) bool {
	if sp.Namespace != ns {
		return false
	}
	agent := t.kind != "workspace" && sp.Agent == t.name
	workspace := t.kind != "agent" && sp.Workspace == t.name
	if !agent && !workspace {
		return false
	}
	return !sp.Time.Before(t.since) && (t.until.IsZero() || sp.Time.Before(t.until))
}

// tracedSpan is a span and its log line without change.
type tracedSpan struct {
	span egress.Span
	line []byte
}

// readSpans reads the spans of the target from one log stream and gives each one to emit.
// It skips a line that is not a span.
func readSpans(r io.Reader, t traceTarget, ns string, emit func(tracedSpan)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxSpanLine)
	for sc.Scan() {
		var sp egress.Span
		if json.Unmarshal(sc.Bytes(), &sp) != nil || sp.V == 0 || sp.Time.IsZero() {
			continue
		}
		if t.match(&sp, ns) {
			emit(tracedSpan{span: sp, line: append([]byte(nil), sc.Bytes()...)})
		}
	}
	return sc.Err()
}

func traceCmd(conn connect) *cobra.Command {
	var since, until, proxyNS string
	var asJSON, follow bool
	cmd := &cobra.Command{
		Use:   "trace NAME|agent/NAME|workspace/NAME",
		Short: "Show the egress spans of an Agent or a Workspace from the egress proxy log",
		Long: "Show the egress spans of an Agent or a Workspace. The command reads the log of the egress proxy\n" +
			"pods with your kubeconfig. You need list on pods and get on pods/log in the proxy namespace.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := parseTraceTarget(args[0])
			if err != nil {
				return err
			}
			if t.since, err = parseTime("--since", since); err != nil {
				return err
			}
			if t.until, err = parseTime("--until", until); err != nil {
				return err
			}
			e, err := conn()
			if err != nil {
				return err
			}
			return e.trace(cmd.Context(), cmd.OutOrStdout(), t, proxyNS, asJSON, follow)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "show only the spans at or after this time (RFC3339)")
	cmd.Flags().StringVar(&until, "until", "", "show only the spans before this time (RFC3339)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print each span as its JSON line from the proxy log")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream the new spans until Ctrl-C")
	cmd.Flags().StringVar(&proxyNS, "proxy-namespace", "koodam-system", "namespace of the egress proxy pods")
	return cmd
}

// parseTime reads an RFC3339 time. An empty value gives the zero time.
func parseTime(flag, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: the value is not an RFC3339 time: %w", flag, err)
	}
	return t, nil
}

// trace prints the spans of t from the log of each egress proxy pod. Without follow, it sorts the
// spans of all pods by time. With follow, it prints each span when it comes.
func (e *env) trace(ctx context.Context, out io.Writer, t traceTarget, proxyNS string, asJSON, follow bool) error {
	pods, err := e.clientset.CoreV1().Pods(proxyNS).List(ctx, metav1.ListOptions{LabelSelector: proxySelector})
	if err != nil {
		return apiError("list the egress proxy pods", err)
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no egress proxy pod in the namespace %s; set --proxy-namespace", proxyNS)
	}
	opts := &corev1.PodLogOptions{Follow: follow}
	if !t.since.IsZero() {
		// The log API uses seconds. The span filter applies the exact time.
		st := metav1.NewTime(t.since.Truncate(time.Second))
		opts.SinceTime = &st
	}
	p := newSpanPrinter(out, asJSON)
	if !follow {
		var spans []tracedSpan
		for _, pod := range pods.Items {
			if err := e.podSpans(ctx, proxyNS, pod.Name, opts, t, func(s tracedSpan) {
				spans = append(spans, s)
			}); err != nil {
				return err
			}
		}
		slices.SortStableFunc(spans, func(a, b tracedSpan) int { return a.span.Time.Compare(b.span.Time) })
		for _, s := range spans {
			p.print(s)
		}
		return p.flush()
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, len(pods.Items))
	for i, pod := range pods.Items {
		wg.Go(func() {
			errs[i] = e.podSpans(ctx, proxyNS, pod.Name, opts, t, func(s tracedSpan) {
				mu.Lock()
				defer mu.Unlock()
				p.print(s)
				_ = p.flush()
			})
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return errors.Join(errs...)
}

// podSpans reads the spans of t from the log of one proxy pod.
func (e *env) podSpans(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions, t traceTarget,
	emit func(tracedSpan)) error {
	rc, err := e.openStream(ctx, ns, pod, opts)
	if err != nil {
		return apiError("read the log of pod/"+pod, err)
	}
	defer func() { _ = rc.Close() }()
	if err := readSpans(rc, t, e.namespace, emit); err != nil && ctx.Err() == nil {
		return fmt.Errorf("read the log of pod/%s: %w", pod, err)
	}
	return nil
}

// spanPrinter prints a span as its JSON line or as a table row.
type spanPrinter struct {
	out    io.Writer
	w      *tabwriter.Writer
	asJSON bool
	header bool
}

func newSpanPrinter(out io.Writer, asJSON bool) *spanPrinter {
	return &spanPrinter{out: out, w: tabwriter.NewWriter(out, 0, 4, 2, ' ', 0), asJSON: asJSON}
}

func (p *spanPrinter) print(s tracedSpan) {
	if p.asJSON {
		_, _ = p.out.Write(append(s.line, '\n'))
		return
	}
	if !p.header {
		_, _ = fmt.Fprintln(p.w, "TIME\tSOURCE\tKIND\tMETHOD\tHOST\tPORT\tPATH\tSTATUS\tRESULT\tIN\tOUT\tMS")
		p.header = true
	}
	sp := s.span
	host := sp.Host
	if host == "" {
		host = sp.SNI
	}
	status := "-"
	if sp.Status != 0 {
		status = strconv.Itoa(sp.Status)
	}
	result := sp.Result
	if sp.Reason != "" {
		result += "/" + sp.Reason
	}
	_, _ = fmt.Fprintf(p.w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%.0f\n",
		sp.Time.UTC().Format(time.RFC3339Nano), cell(sp.Source), cell(sp.Kind), cell(sp.Method), cell(host),
		cell(sp.Port), cell(sp.Path), status, cell(result), sp.BytesIn, sp.BytesOut, sp.DurationMS)
}

func (p *spanPrinter) flush() error { return p.w.Flush() }

// cell makes a table cell from text of the pod. The host and the SNI come from the pod, so a
// control character, a tab or a space changes to "?". An empty value is "-".
func cell(s string) string {
	if s == "" {
		return "-"
	}
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return '?'
		}
		return r
	}, s)
}
