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
	"net/url"
	"os"
	"regexp"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// agentLabel is on the pods of an Agent. It must match internal/controller.
	agentLabel = "koodam.dev/agent"
	// condRevoked is true when the controller blocked the key and wrote the usage.
	condRevoked = "Revoked"
)

// yamlLine finds the line number in a YAML decode error.
var yamlLine = regexp.MustCompile(`line (\d+)`)

func runCmd(conn connect) *cobra.Command {
	var file string
	var follow bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "run -f AGENT.yaml",
		Short: "Create an Agent. With --wait, show the pod log and the spend at the end",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := conn()
			if err != nil {
				return err
			}
			a, err := readAgent(file, e.namespace)
			if err != nil {
				return err
			}
			// As in kubectl, an explicit -n must agree with the namespace in the file.
			if ns, _ := cmd.Flags().GetString("namespace"); cmd.Flags().Changed("namespace") && ns != a.Namespace {
				return fmt.Errorf("the namespace in %s (%s) does not agree with -n %s", file, a.Namespace, ns)
			}
			e.namespace = a.Namespace
			err = e.kube.Create(cmd.Context(), a)
			if apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("agent/%s exists in namespace %s: delete it or use a new name", a.Name, a.Namespace)
			}
			if err != nil {
				return apiError("create the Agent", err)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "agent/%s created\n", a.Name)
			if !follow {
				return nil
			}
			return e.follow(cmd.Context(), timeout, out, a.Name)
		},
	}
	cmd.Flags().StringVarP(&file, "filename", "f", "", "the Agent YAML file")
	cmd.Flags().BoolVar(&follow, "wait", false, "show the pod log, wait for the revoke and show the spend")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour,
		"the maximum time of each wait, for the pod and for the revoke")
	_ = cmd.MarkFlagRequired("filename")
	return cmd
}

func readAgent(file, namespace string) (*corev1alpha1.Agent, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	a := &corev1alpha1.Agent{}
	if err := yaml.UnmarshalStrict(data, a); err != nil {
		// The decode error can hold a value from the file, and the file can hold a credential.
		// Thus give only the line number.
		msg := fmt.Sprintf("%s is not a valid Agent manifest", file)
		if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
			msg += " (line " + m[1] + ")"
		}
		return nil, errors.New(msg)
	}
	if a.Kind != "Agent" || a.APIVersion != corev1alpha1.GroupVersion.String() {
		return nil, fmt.Errorf("%s is not an %s Agent", file, corev1alpha1.GroupVersion)
	}
	if a.Namespace == "" {
		a.Namespace = namespace
	}
	return a, nil
}

// apiError cleans an error from the API server. It keeps only the reason, the HTTP code and the
// kind. The status message, the name and the causes can hold values from the manifest or from
// the stored object, so the error never shows them. If err is nil, apiError returns nil.
func apiError(op string, err error) error {
	var status apierrors.APIStatus
	var urlErr *url.Error
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &status) && errors.As(err, &urlErr):
		// The URL holds the object name. Give only the cause.
		return fmt.Errorf("%s: %w", op, urlErr.Err)
	case !errors.As(err, &status):
		return err
	}
	st := status.Status()
	clean := metav1.Status{Status: st.Status, Reason: apierrors.ReasonForError(err), Code: st.Code}
	if st.Details != nil {
		clean.Details = &metav1.StatusDetails{Group: st.Details.Group, Kind: st.Details.Kind}
	}
	return &cleanError{op: op, status: clean}
}

// cleanError is an API server error without values. It keeps the status, so that
// apierrors.IsNotFound and the other checks work.
type cleanError struct {
	op     string
	status metav1.Status
}

func (c *cleanError) Status() metav1.Status { return c.status }

func (c *cleanError) Error() string {
	msg := fmt.Sprintf("%s: %s (HTTP %d", c.op, c.status.Reason, c.status.Code)
	if d := c.status.Details; d != nil && d.Kind != "" {
		msg += ", " + d.Kind
	}
	return msg + ")"
}

// follow shows the pod log, then waits for the revoke and shows the spend.
// It returns an error if the run failed. A log error is only a warning, because the spend
// comes after the log. The timeout applies to each wait, not to the log stream, so a long
// run shows all of its log.
func (e *env) follow(ctx context.Context, timeout time.Duration, out io.Writer, name string) error {
	podCtx, cancelPod := context.WithTimeout(ctx, timeout)
	rc, err := e.waitLog(podCtx, ctx, name, true)
	cancelPod()
	if err == nil && rc != nil {
		_, err = io.Copy(out, rc)
		_ = rc.Close()
	}
	if err != nil {
		_, _ = fmt.Fprintf(out, "warning: %v. The wait for the spend continues.\n", err)
	}
	revokeCtx, cancelRevoke := context.WithTimeout(ctx, timeout)
	defer cancelRevoke()
	var a *corev1alpha1.Agent
	err = wait.PollUntilContextCancel(revokeCtx, e.poll, true, func(ctx context.Context) (bool, error) {
		var err error
		a, err = e.agent(ctx, name)
		return err == nil && ended(a), err
	})
	if err != nil {
		return fmt.Errorf("wait for the revoke: %w", err)
	}
	_, _ = fmt.Fprintf(out, "agent/%s %s\n", name, a.Status.Phase)
	if u := a.Status.Usage; u != nil {
		_, _ = fmt.Fprintf(out, "spend %s of %s, %d calls, %d failed calls, %d input and %d output tokens\n",
			u.Spend, a.Spec.Budget.String(), u.Calls, u.FailedCalls, u.InputTokens, u.OutputTokens)
	} else {
		_, _ = fmt.Fprintln(out, "spend unknown: the gateway gave no usage")
	}
	// The message tells if the spend did not settle, so that the usage can be low.
	if c := meta.FindStatusCondition(a.Status.Conditions, condRevoked); c != nil && c.Message != "" {
		_, _ = fmt.Fprintf(out, "revoke: %s\n", c.Message)
	}
	if noGatewaySpend(a) {
		_, _ = fmt.Fprintln(out, gatewayNote)
	}
	if a.Status.Phase == corev1alpha1.AgentFailed {
		return errors.New("the run failed")
	}
	return nil
}

// gatewayNote tells the developer about the gateway model.
const gatewayNote = "note: a run ended with no gateway spend. The gateway model gives each run a budget, " +
	"a revoke and the real spend: https://github.com/mohanraj00/koodam/blob/main/docs/gateway.md"

// noGatewaySpend is true when the run ended and the status has no gateway usage.
// The API cannot yet tell a subscription run from a gateway run, so this rule uses the usage only.
func noGatewaySpend(a *corev1alpha1.Agent) bool {
	return ended(a) && a.Status.Usage == nil
}

// ended is true when the controller revoked the run. Then the usage is final.
func ended(a *corev1alpha1.Agent) bool {
	return meta.IsStatusConditionTrue(a.Status.Conditions, condRevoked)
}

func (e *env) agent(ctx context.Context, name string) (*corev1alpha1.Agent, error) {
	a := &corev1alpha1.Agent{}
	err := e.kube.Get(ctx, types.NamespacedName{Namespace: e.namespace, Name: name}, a)
	return a, apiError("get the Agent", err)
}

// errNoPod is the error when the Agent's Job has no pod.
var errNoPod = errors.New("the Job has no pod")

// errNotStarted is the error when the pod did not start.
var errNotStarted = errors.New("the pod did not start")

// pod finds the newest pod of the Agent's current Job. The Job must be controlled by the Agent,
// and the pod must be controlled by the Job, so a pod with a copied label does not match.
func (e *env) pod(ctx context.Context, a *corev1alpha1.Agent) (*corev1.Pod, error) {
	if a.Status.Job == "" {
		return nil, errNoPod
	}
	job := &batchv1.Job{}
	err := e.kube.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: a.Status.Job}, job)
	if apierrors.IsNotFound(err) {
		return nil, errNoPod
	}
	if err != nil {
		return nil, apiError("get the Job", err)
	}
	if !metav1.IsControlledBy(job, a) {
		return nil, errNoPod
	}
	pods := &corev1.PodList{}
	if err := e.kube.List(ctx, pods, client.InNamespace(a.Namespace),
		client.MatchingLabels{agentLabel: a.Name}); err != nil {
		return nil, apiError("list the pods", err)
	}
	owned := slices.DeleteFunc(pods.Items, func(p corev1.Pod) bool { return !metav1.IsControlledBy(&p, job) })
	if len(owned) == 0 {
		return nil, errNoPod
	}
	newest := slices.MaxFunc(owned, func(x, y corev1.Pod) int {
		return x.CreationTimestamp.Compare(y.CreationTimestamp.Time)
	})
	return &newest, nil
}

// openLog opens the log of the Agent's pod. It reads the pod with ctx and streams the log with
// streamCtx. The error text has no pod name.
func (e *env) openLog(ctx, streamCtx context.Context, a *corev1alpha1.Agent, follow bool) (io.ReadCloser, error) {
	pod, err := e.pod(ctx, a)
	if err != nil {
		return nil, err
	}
	if pod.Status.Phase == corev1.PodPending {
		return nil, errNotStarted
	}
	opts := &corev1.PodLogOptions{Follow: follow}
	// The open runs in a goroutine, so the end of ctx stops a stalled open. The stream that the
	// open gives has a child of streamCtx, without the wait deadline.
	openCtx, cancelOpen := context.WithCancel(streamCtx)
	type opened struct {
		rc  io.ReadCloser
		err error
	}
	done := make(chan opened, 1)
	go func() {
		rc, err := e.openStream(openCtx, a.Namespace, pod.Name, opts)
		done <- opened{rc, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			cancelOpen()
			return nil, apiError("read the pod log", o.err)
		}
		return &cancelCloser{ReadCloser: o.rc, cancel: cancelOpen}, nil
	case <-ctx.Done():
		cancelOpen()
		go func() {
			// The open can succeed after the wait ends. Close that stream.
			if o := <-done; o.rc != nil {
				_ = o.rc.Close()
			}
		}()
		return nil, fmt.Errorf("read the pod log: %w", ctx.Err())
	}
}

// openStream opens the pod log stream with ctx.
func (e *env) openStream(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	if e.stream != nil {
		return e.stream(ctx, ns, pod, opts)
	}
	return e.clientset.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
}

// cancelCloser cancels the context of the stream when the stream closes.
type cancelCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// waitLog opens the pod log when the pod starts. It tries again until waitCtx ends or the run
// ends. If the run ends before the pod starts, it gives no log and no error. The log stream uses
// streamCtx, so the end of waitCtx does not stop it.
func (e *env) waitLog(waitCtx, streamCtx context.Context, name string, follow bool) (io.ReadCloser, error) {
	var rc io.ReadCloser
	var last error
	err := wait.PollUntilContextCancel(waitCtx, e.poll, true, func(ctx context.Context) (bool, error) {
		a, err := e.agent(ctx, name)
		if err != nil {
			return false, err
		}
		rc, last = e.openLog(ctx, streamCtx, a, follow)
		return last == nil || ended(a), nil
	})
	if errors.Is(last, errNoPod) || errors.Is(last, errNotStarted) {
		last = nil
	}
	if last != nil {
		return nil, last
	}
	return rc, err
}

// getCmd makes "get" or, if spend is true, "spend". Both show the phase, the budget and the usage.
func getCmd(conn connect, spend bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get [NAME]",
		Short: "Show the phase, the budget and the spend of the Agents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := conn()
			if err != nil {
				return err
			}
			list := &corev1alpha1.AgentList{}
			if len(args) == 1 {
				a, err := e.agent(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				list.Items = append(list.Items, *a)
			} else if err := e.kube.List(cmd.Context(), list, client.InNamespace(e.namespace)); err != nil {
				return apiError("list the Agents", err)
			}
			printAgents(cmd.OutOrStdout(), list.Items, spend)
			// The note goes to stderr, so that a script can still read the table from stdout.
			for i := range list.Items {
				if noGatewaySpend(&list.Items[i]) {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), gatewayNote)
					break
				}
			}
			return nil
		},
	}
	if spend {
		cmd.Use = "spend [NAME]"
		cmd.Short = "Show the budget and the gateway usage of the Agents"
	}
	return cmd
}

// timeText gives the time in UTC as RFC 3339, or "-" if there is no time.
func timeText(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func harnessText(h *corev1alpha1.HarnessSpec) string {
	if h == nil {
		return "-"
	}
	return h.Name
}

func printAgents(out io.Writer, agents []corev1alpha1.Agent, spend bool) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if spend {
		_, _ = fmt.Fprintln(w, "NAME\tPHASE\tBUDGET\tSPEND\tCALLS\tFAILED\tINPUT\tOUTPUT")
	} else {
		_, _ = fmt.Fprintln(w, "NAME\tPHASE\tMODEL\tHARNESS\tBUDGET\tSPEND\tSTARTED\tCOMPLETED\tKEY ALIAS")
	}
	for _, a := range agents {
		u := a.Status.Usage
		if u == nil {
			u = &corev1alpha1.AgentUsage{Spend: "-"}
		}
		if spend {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\n", a.Name, a.Status.Phase,
				a.Spec.Budget.String(), u.Spend, u.Calls, u.FailedCalls, u.InputTokens, u.OutputTokens)
		} else {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.Name, a.Status.Phase,
				a.Spec.Model, harnessText(a.Spec.Harness), a.Spec.Budget.String(), u.Spend,
				timeText(a.Status.StartedAt), timeText(a.Status.CompletedAt),
				a.Status.KeyAlias)
		}
	}
	_ = w.Flush()
}

func logsCmd(conn connect) *cobra.Command {
	var follow bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Show the pod log of the Agent's Job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := conn()
			if err != nil {
				return err
			}
			var rc io.ReadCloser
			if follow {
				// The timeout is for the wait for the pod only. The stream ends on Ctrl-C or the pod end.
				podCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
				defer cancel()
				rc, err = e.waitLog(podCtx, cmd.Context(), args[0], true)
			} else {
				var a *corev1alpha1.Agent
				if a, err = e.agent(cmd.Context(), args[0]); err == nil {
					rc, err = e.openLog(cmd.Context(), cmd.Context(), a, false)
				}
			}
			if err != nil {
				return err
			}
			if rc == nil {
				return fmt.Errorf("agent/%s: %w", args[0], errNoPod)
			}
			defer func() { _ = rc.Close() }()
			_, err = io.Copy(cmd.OutOrStdout(), rc)
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "wait for the pod, then stream the log until the pod stops")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "the maximum time to wait for the pod with -f")
	return cmd
}

func deleteCmd(conn connect) *cobra.Command {
	var follow bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "delete NAME",
		Short: "Delete an Agent. The controller revokes the key before the Agent goes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := conn()
			if err != nil {
				return err
			}
			a := &corev1alpha1.Agent{}
			a.Name, a.Namespace = args[0], e.namespace
			if err := e.kube.Delete(cmd.Context(), a); err != nil {
				return apiError("delete the Agent", err)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "agent/%s deleted\n", a.Name)
			if !follow {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			err = wait.PollUntilContextCancel(ctx, e.poll, true, func(ctx context.Context) (bool, error) {
				_, err := e.agent(ctx, a.Name)
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				return false, err
			})
			if err != nil {
				return fmt.Errorf("wait for the revoke: %w", err)
			}
			_, _ = fmt.Fprintf(out, "agent/%s revoked and gone\n", a.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&follow, "wait", false, "wait until the controller revokes the key and the Agent is gone")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "the maximum time for --wait")
	return cmd
}
