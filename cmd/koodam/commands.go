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
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// condRevoked is true when the controller blocked the key and wrote the usage.
	condRevoked = "Revoked"
)

func runCmd(conn connect) *cobra.Command {
	var file string
	var follow bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "run -f AGENT.yaml",
		Short: "Create an Agent. With --wait, wait for the end of the run and show the spend",
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
			// The flags give the namespace only if the file has none.
			e.namespace = a.Namespace
			if err := e.kube.Create(cmd.Context(), a); err != nil {
				return fmt.Errorf("create the Agent: %w", err)
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
	cmd.Flags().BoolVar(&follow, "wait", false, "wait for the revoke and show the spend")
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
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	if a.Kind != "Agent" || a.APIVersion != corev1alpha1.GroupVersion.String() {
		return nil, fmt.Errorf("%s is not an %s Agent", file, corev1alpha1.GroupVersion)
	}
	if a.Namespace == "" {
		a.Namespace = namespace
	}
	return a, nil
}

// follow waits for the revoke and shows the spend. It returns an error if the run failed.
func (e *env) follow(ctx context.Context, out io.Writer, name string) error {
	var a *corev1alpha1.Agent
	err := wait.PollUntilContextCancel(ctx, e.poll, true, func(ctx context.Context) (bool, error) {
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
	return a, err
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
				return err
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
				return err
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
