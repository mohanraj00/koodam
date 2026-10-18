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
	"os/exec"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
)

var (
	// managerImage is the manager image that the suite builds and loads into kind.
	managerImage = "example.com/koodam:v0.0.1"
	ctx          = context.Background()
	restCfg      *rest.Config
	k8s          client.Client
	clientset    *kubernetes.Clientset
	// masterKey is the gateway master key. The suite makes it and never prints it.
	masterKey   string
	gatewayURL  = "http://127.0.0.1:" + gatewayPort
	portForward *exec.Cmd
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

	restCfg, err = ctrl.GetConfig()
	Expect(err).NotTo(HaveOccurred())
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

	By("installing the chart with the bundled gateway")
	i := strings.LastIndex(managerImage, ":")
	_, err = utils.Run(exec.Command("helm", "upgrade", "--install", release, "charts/koodam",
		"-n", systemNS, "-f", "charts/koodam/values-bundled.yaml",
		"--set", "image.repository="+managerImage[:i], "--set", "image.tag="+managerImage[i+1:],
		"--wait", "--timeout", "10m"))
	Expect(err).NotTo(HaveOccurred(), "Failed to install the chart")

	By("forwarding a local port to the bundled gateway")
	portForward = exec.Command("kubectl", "-n", systemNS, "port-forward",
		"svc/"+release+"-litellm", gatewayPort+":4000")
	portForward.Stdout, portForward.Stderr = GinkgoWriter, GinkgoWriter
	Expect(portForward.Start()).To(Succeed())
	Eventually(func() int {
		resp, err := http.Get(gatewayURL + "/health/readiness")
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}, 2*time.Minute, time.Second).Should(Equal(http.StatusOK))
})

var _ = AfterSuite(func() {
	if portForward != nil && portForward.Process != nil {
		_ = portForward.Process.Kill()
	}
	By("uninstalling the chart")
	_, _ = utils.Run(exec.Command("helm", "uninstall", release, "-n", systemNS))
})
