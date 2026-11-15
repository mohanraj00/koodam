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
	"bytes"
	"fmt"
	"io"
	"maps"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/usage"
)

// outputProgram runs a program with the stdin in and gives its stdout. It drops the stderr of
// the program, because a remote stderr can hold any text from the pod. It uses no shell. Tests
// replace it.
var outputProgram = func(cmd *cobra.Command, in string, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(cmd.Context(), name, args...)
	out := &bytes.Buffer{}
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader(in), out, io.Discard
	err := c.Run()
	return out.Bytes(), err
}

// usageGatewayNote tells that a Workspace with a gateway key has a real spend. The controller
// writes the gateway usage of a Workspace to its status.
const usageGatewayNote = "note: this Workspace has a gateway key. The gateway spend is the real spend: " +
	"kubectl get workspace %s -n %s -o jsonpath='{.status.usage}'. The session files also count the calls " +
	"through the gateway."

func usageCmd(conn connect) *cobra.Command {
	var by string
	cmd := &cobra.Command{
		Use:   "usage [WORKSPACE] [--by cost-group]",
		Short: "Show the projected Workspace cost or the Agent gateway spend by cost group",
		Long: "Show the tokens and the projected API cost of the Claude Code and Codex sessions in a Workspace.\n" +
			"koodam connects with SSH, as koodam ssh test does, and runs a fixed script in the pod. The\n" +
			"script reads ~/.claude/projects and ~/.codex/sessions and prints only the model names and the\n" +
			"token counts, not the text. koodam multiplies the counts by the embedded LiteLLM prices.\n\n" +
			"The cost is projected: it is the API price of the tokens, not the bill of a subscription plan.\n" +
			"Limits: the cache write price is the 5-minute price, and the price tiers above 200k tokens\n" +
			"are not used. If a Codex session file has more than 1 model, all its tokens go to the last model.\n\n" +
			"Run koodam ssh setup WORKSPACE first.\n\n" +
			"With --by cost-group, take no WORKSPACE argument. Read the Agent gateway usage in the\n" +
			"namespace from the Kubernetes API and group it by status.costGroup. This mode uses no SSH.",
		Args: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("by") {
				return cobra.ExactArgs(1)(cmd, args)
			}
			if by != "cost-group" {
				return fmt.Errorf("--by %q is not known: the only key is cost-group", by)
			}
			if len(args) != 0 {
				return fmt.Errorf("koodam usage --by takes no WORKSPACE argument")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if by == "cost-group" {
				e, err := conn()
				if err != nil {
					return err
				}
				agents := &corev1alpha1.AgentList{}
				if err := e.kube.List(cmd.Context(), agents, client.InNamespace(e.namespace)); err != nil {
					return apiError("list the Agents", err)
				}
				return printCostGroupUsage(cmd.OutOrStdout(), cmd.ErrOrStderr(), agents.Items, e.namespace)
			}
			e, ns, ws, err := sshTarget(conn, args[0])
			if err != nil {
				return err
			}
			w := &corev1alpha1.Workspace{}
			err = e.kube.Get(cmd.Context(), client.ObjectKey{Namespace: ns, Name: ws}, w)
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("workspace %s does not exist in namespace %s", ws, ns)
			}
			if err != nil {
				return apiError("get the Workspace", err)
			}
			if w.Status.Phase != corev1alpha1.WorkspaceRunning {
				return fmt.Errorf("workspace %s/%s is not Running (phase %q): start it, then run koodam usage again",
					ns, ws, w.Status.Phase)
			}
			if err := needEntry(ns, ws); err != nil {
				return err
			}
			prices, err := usage.Prices()
			if err != nil {
				return err
			}
			raw, err := outputProgram(cmd, usage.RemoteScript, sshProgram, "-T", hostAlias(ns, ws), "sh", "-s")
			if err != nil {
				return fmt.Errorf("the usage script failed in Workspace %s/%s: run koodam ssh test -n %s %s",
					ns, ws, ns, ws)
			}
			rows, err := usage.Parse(bytes.NewReader(raw))
			if err != nil {
				return err
			}
			if w.Spec.Gateway != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), usageGatewayNote+"\n", ws, ns)
			}
			return printUsage(cmd.OutOrStdout(), rows, prices)
		},
	}
	cmd.Flags().StringVar(&by, "by", "", "group the Agent gateway usage by cost-group")
	return cmd
}

// costGroupUsage has the sums of the gateway usage for a group.
type costGroupUsage struct {
	runs, calls, failed, input, output int64
	spend                              float64
}

func printCostGroupUsage(out, errOut io.Writer, agents []corev1alpha1.Agent, namespace string) error {
	groups := map[string]costGroupUsage{}
	var total costGroupUsage
	var missing, invalid int
	for _, a := range agents {
		u := a.Status.Usage
		if u == nil {
			missing++
			continue
		}
		spend, err := strconv.ParseFloat(u.Spend, 64)
		if err != nil || math.IsNaN(spend) || math.IsInf(spend, 0) {
			invalid++
			continue
		}
		g := groups[a.Status.CostGroup]
		g.runs++
		g.calls += u.Calls
		g.failed += u.FailedCalls
		g.input += u.InputTokens
		g.output += u.OutputTokens
		g.spend += spend
		groups[a.Status.CostGroup] = g
		total.runs++
		total.calls += u.Calls
		total.failed += u.FailedCalls
		total.input += u.InputTokens
		total.output += u.OutputTokens
		total.spend += spend
	}
	if missing > 0 {
		_, _ = fmt.Fprintf(errOut, "note: Agents with no gateway usage yet: %d. The cause is a run that did not end, "+
			"or a gateway that gave no usage. They are not in the table.\n", missing)
	}
	if invalid > 0 {
		_, _ = fmt.Fprintf(errOut, "note: Agents with a spend that is not a number: %d. They are not in the table.\n", invalid)
	}
	if len(groups) == 0 {
		_, err := fmt.Fprintf(out, "No gateway usage in the Agents of namespace %s.\n", namespace)
		return err
	}
	names := slices.Sorted(maps.Keys(groups))
	if _, ok := groups[""]; ok {
		names = append(slices.Delete(names, 0, 1), "")
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "COST-GROUP\tRUNS\tCALLS\tFAILED\tINPUT\tOUTPUT\tSPEND\tSOURCE")
	row := func(name string, g costGroupUsage) {
		_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%.4f USD\tsource: gateway\n",
			name, g.runs, g.calls, g.failed, g.input, g.output, g.spend)
	}
	for _, name := range names {
		label := name
		if label == "" {
			label = "(none)"
		}
		row(label, groups[name])
	}
	row("total", total)
	return w.Flush()
}

// printUsage writes 1 line for each harness and model, and 1 total line. Each line says that
// the cost is projected and that the source is the subscription session files.
func printUsage(out io.Writer, rows []usage.Row, prices *usage.Table) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "No token counts in the session files of the Workspace.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "HARNESS\tMODEL\tINPUT\tOUTPUT\tCACHE-READ\tCACHE-WRITE\tCOST\tSOURCE")
	var total usage.Row
	var cost float64
	unpriced := map[string]bool{}
	for _, r := range rows {
		c, ok := prices.Cost(r)
		text := "no price"
		if ok {
			text = fmt.Sprintf("projected %.4f USD", c)
			cost += c
		} else {
			unpriced[r.Model] = true
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%s\tsource: subscription\n",
			r.Harness, r.Model, r.Input, r.Output, r.CacheRead, r.CacheWrite, text)
		total.Input, total.Output = total.Input+r.Input, total.Output+r.Output
		total.CacheRead, total.CacheWrite = total.CacheRead+r.CacheRead, total.CacheWrite+r.CacheWrite
	}
	models := map[string]bool{}
	for _, r := range rows {
		models[r.Model] = true
	}
	text := "no price"
	if len(unpriced) < len(models) {
		text = fmt.Sprintf("projected %.4f USD", cost)
		if len(unpriced) > 0 {
			text += fmt.Sprintf(" (no price for %d of the models: %s)", len(unpriced),
				strings.Join(slices.Sorted(maps.Keys(unpriced)), ", "))
		}
	}
	_, _ = fmt.Fprintf(w, "total\t-\t%d\t%d\t%d\t%d\t%s\tsource: subscription\n",
		total.Input, total.Output, total.CacheRead, total.CacheWrite, text)
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "The prices are from LiteLLM commit %s. A subscription plan does not bill "+
		"this cost.\n", shortCommit(prices.Commit))
	return err
}

// shortCommit gives the first 12 characters of a commit.
func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
