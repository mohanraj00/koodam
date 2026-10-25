//go:build e2e
// +build e2e

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

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/test/utils"
)

const (
	// systemNS has the controller and the bundled gateway.
	systemNS = "koodam-system"
	// runNS has the test Agents.
	runNS = "koodam-e2e"
	// release is the Helm release name. The chart names the LiteLLM Service "<release>-litellm".
	release = "koodam"
	// gatewayPort is the local port that forwards to the bundled LiteLLM Service.
	gatewayPort = "14000"
	// callTimeout is the limit for each network call of the test.
	callTimeout = 10 * time.Second
	// fakeUpstream is the name of the fake upstream model. testdata/values-e2e.yaml has its URL.
	fakeUpstream = "fake-upstream"
	// pythonImage runs testdata/fake_responses.py.
	pythonImage = "docker.io/library/python:3.13-alpine"
)

var (
	// managerImage is the manager image that the suite builds and loads into kind.
	managerImage = "example.com/koodam:v0.0.1"
	// workspaceImage is the workspace image that the suite builds from images/workspace.
	workspaceImage = "example.com/koodam-workspace:v0.0.1"
	ctx            = context.Background()
	restCfg        *rest.Config
	k8s            client.Client
	clientset      *kubernetes.Clientset
	// masterKey is the gateway master key. The suite makes it and never prints it.
	masterKey   string
	gatewayURL  = "http://127.0.0.1:" + gatewayPort
	portForward *exec.Cmd
	httpClient  = &http.Client{Timeout: callTimeout}
)

// TestE2E installs the chart with the bundled gateway on kind and runs the residual access tests.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting koodam e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the manager image")
	_, err := utils.Run(exec.Command("make", "docker-build", "IMG="+managerImage))
	Expect(err).NotTo(HaveOccurred(), "Failed to build the manager image")

	By("loading the manager image on kind")
	Expect(utils.LoadImageToKindClusterWithName(managerImage)).To(Succeed())

	By("building the workspace image and loading it on kind")
	_, err = utils.Run(exec.Command("make", "docker-build-workspace", "WORKSPACE_IMG="+workspaceImage))
	Expect(err).NotTo(HaveOccurred(), "Failed to build the workspace image")
	Expect(utils.LoadImageToKindClusterWithName(workspaceImage)).To(Succeed())

	restCfg, err = ctrl.GetConfig()
	Expect(err).NotTo(HaveOccurred())
	restCfg.Timeout = callTimeout
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(corev1alpha1.AddToScheme(scheme)).To(Succeed())
	k8s, err = client.New(restCfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	clientset, err = kubernetes.NewForConfig(restCfg)
	Expect(err).NotTo(HaveOccurred())

	By("making the namespaces")
	for _, ns := range []string{systemNS, runNS} {
		err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
	}

	By("making the master key Secret with a random key")
	b := make([]byte, 24)
	_, err = rand.Read(b)
	Expect(err).NotTo(HaveOccurred())
	masterKey = "sk-" + hex.EncodeToString(b)
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "koodam-gateway", Namespace: systemNS}}
	Expect(client.IgnoreNotFound(k8s.Delete(ctx, s))).To(Succeed())
	s.StringData = map[string]string{"master-key": masterKey}
	Expect(k8s.Create(ctx, s)).To(Succeed(), "Failed to make the master key Secret")

	By("making the fake upstream model for the Codex calls")
	makeFakeUpstream()

	By("installing the chart with the bundled gateway")
	installChart()

	By("forwarding a local port to the bundled gateway")
	portForward = forward(release+"-litellm", gatewayPort+":4000")
	Eventually(func() int {
		resp, err := httpClient.Get(gatewayURL + "/health/readiness")
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}, 2*time.Minute, time.Second).Should(Equal(http.StatusOK))
})

// installChart installs or upgrades the chart with the values of the suite and the extra helm
// arguments. With no extra argument, it gives the chart of the suite again.
func installChart(extra ...string) {
	i := strings.LastIndex(managerImage, ":")
	args := append([]string{"upgrade", "--install", release, "charts/koodam",
		"-n", systemNS, "-f", "charts/koodam/values-bundled.yaml",
		"-f", "test/e2e/testdata/values-e2e.yaml",
		"--set", "image.repository=" + managerImage[:i], "--set", "image.tag=" + managerImage[i+1:],
		"--set", "metrics.enabled=true", "--set", "jobTTLSeconds=" + strconv.Itoa(jobTTL),
		"--wait", "--timeout", "10m"}, extra...)
	out, err := utils.Run(exec.Command("helm", args...))
	Expect(err).NotTo(HaveOccurred(), "Failed to install the chart: "+out)
}

// makeFakeUpstream runs testdata/fake_responses.py in systemNS behind the Service fakeUpstream.
func makeFakeUpstream() {
	dir, err := utils.GetProjectDir()
	Expect(err).NotTo(HaveOccurred())
	src, err := os.ReadFile(filepath.Join(dir, "test", "e2e", "testdata", "fake_responses.py"))
	Expect(err).NotTo(HaveOccurred())
	labels := map[string]string{"app": fakeUpstream}
	om := metav1.ObjectMeta{Name: fakeUpstream, Namespace: systemNS, Labels: labels}
	objs := []client.Object{
		&corev1.ConfigMap{ObjectMeta: om, Data: map[string]string{"fake.py": string(src)}},
		&appsv1.Deployment{ObjectMeta: om, Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "fake", Image: pythonImage, Command: []string{"python", "-u", "/app/fake.py"},
					Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
					ReadinessProbe: &corev1.Probe{PeriodSeconds: 1, ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(8080)}}},
					VolumeMounts: []corev1.VolumeMount{{Name: "app", MountPath: "/app"}},
				}},
				Volumes: []corev1.Volume{{Name: "app", VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{
						Name: fakeUpstream}}}}},
			}},
		}},
		&corev1.Service{ObjectMeta: om, Spec: corev1.ServiceSpec{Selector: labels,
			Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}}},
	}
	for _, o := range objs {
		Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, o))).To(Succeed())
	}
	_, err = utils.Run(exec.Command("kubectl", "-n", systemNS, "rollout", "status",
		"deployment/"+fakeUpstream, "--timeout", "5m"))
	Expect(err).NotTo(HaveOccurred(), "The fake upstream did not start.")
}

// forward starts "kubectl port-forward" to the Service svc in systemNS. The caller stops it.
func forward(svc, ports string) *exec.Cmd {
	cmd := exec.Command("kubectl", "-n", systemNS, "port-forward", "svc/"+svc, ports)
	cmd.Stdout, cmd.Stderr = GinkgoWriter, GinkgoWriter
	Expect(cmd.Start()).To(Succeed())
	return cmd
}

var _ = AfterSuite(func() {
	if portForward != nil && portForward.Process != nil {
		_ = portForward.Process.Kill()
	}
	By("uninstalling the chart")
	_, _ = utils.Run(exec.Command("helm", "uninstall", release, "-n", systemNS))
})
