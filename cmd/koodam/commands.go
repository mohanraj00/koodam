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
	"os"
	"regexp"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
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
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return e.follow(ctx, out, a.Name)
		},
	}
	cmd.Flags().StringVarP(&file, "filename", "f", "", "the Agent YAML file")
	cmd.Flags().BoolVar(&follow, "wait", false, "show the pod log, wait for the revoke and show the spend")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour, "the maximum time for --wait")
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
	if err == nil || !errors.As(err, &status) {
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
// It returns an error if the run failed.
func (e *env) follow(ctx context.Context, out io.Writer, name string) error {
	var pod *corev1.Pod
	err := wait.PollUntilContextCancel(ctx, e.poll, true, func(ctx context.Context) (bool, error) {
		a, err := e.agent(ctx, name)
		if err != nil {
			return false, err
		}
		if pod, err = e.pod(ctx, name); err != nil && !errors.Is(err, errNoPod) {
			return false, err
		}
		// The log is available after the pod starts. A run can also end before its pod starts.
		started := pod != nil && pod.Status.Phase != corev1.PodPending
		return started || ended(a), nil
	})
	if err != nil {
		return fmt.Errorf("wait for the pod: %w", err)
	}
	if pod != nil && pod.Status.Phase != corev1.PodPending {
		if err := e.streamLog(ctx, out, pod.Name, true); err != nil {
			return err
		}
	}
	var a *corev1alpha1.Agent
	err = wait.PollUntilContextCancel(ctx, e.poll, true, func(ctx context.Context) (bool, error) {
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
	if a.Status.Phase == corev1alpha1.AgentFailed {
		return errors.New("the run failed")
	}
	return nil
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

// pod finds the newest pod of the Agent's Job.
func (e *env) pod(ctx context.Context, name string) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := e.kube.List(ctx, pods, client.InNamespace(e.namespace),
		client.MatchingLabels{agentLabel: name}); err != nil {
		return nil, apiError("list the pods", err)
	}
	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("agent/%s: %w", name, errNoPod)
	}
	newest := slices.MaxFunc(pods.Items, func(x, y corev1.Pod) int {
		return x.CreationTimestamp.Compare(y.CreationTimestamp.Time)
	})
	return &newest, nil
}

func (e *env) streamLog(ctx context.Context, out io.Writer, pod string, follow bool) error {
	opts := &corev1.PodLogOptions{Follow: follow}
	rc, err := e.clientset.CoreV1().Pods(e.namespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return apiError("read the log of pod "+pod, err)
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(out, rc)
	return err
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
			return nil
		},
	}
	if spend {
		cmd.Use = "spend [NAME]"
		cmd.Short = "Show the budget and the gateway usage of the Agents"
	}
	return cmd
}

func printAgents(out io.Writer, agents []corev1alpha1.Agent, spend bool) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if spend {
		_, _ = fmt.Fprintln(w, "NAME\tPHASE\tBUDGET\tSPEND\tCALLS\tFAILED\tINPUT\tOUTPUT")
	} else {
		_, _ = fmt.Fprintln(w, "NAME\tPHASE\tMODEL\tBUDGET\tSPEND\tKEY ALIAS")
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
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Name, a.Status.Phase, a.Spec.Model,
				a.Spec.Budget.String(), u.Spend, a.Status.KeyAlias)
		}
	}
	_ = w.Flush()
}

func logsCmd(conn connect) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Show the pod log of the Agent's Job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := conn()
			if err != nil {
				return err
			}
			pod, err := e.pod(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return e.streamLog(cmd.Context(), cmd.OutOrStdout(), pod.Name, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream the log until the pod stops")
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
