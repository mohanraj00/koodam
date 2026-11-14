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
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/test/utils"
)

// The run output spec checks #296: the run identity is the same after a controller restart, and
// the status has the digest of a known output file.
var _ = Describe("Run output", Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	AfterEach(dumpOnFailure)

	It("keeps the run identity after a controller restart and writes the digest of the output", func() {
		// The file has the 6 bytes "hello\n". This is its digest from sha256sum.
		const digest = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
		a := sampleAgent("output", `printf 'hello\n' > /out/report.txt; test -n "$KOODAM_RUN_ID"; sleep 45`)
		a.Spec.Output = &corev1alpha1.AgentOutput{Path: "/out/report.txt"}
		a.Spec.PodTemplate = &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: "out", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
			Containers: []corev1.Container{{Name: "agent",
				VolumeMounts: []corev1.VolumeMount{{Name: "out", MountPath: "/out"}}}}}}
		start(a)
		runID := a.Status.RunID
		Expect(runID).To(Equal(string(a.UID)))

		By("starting the controller again during the run")
		for _, args := range [][]string{
			{"-n", systemNS, "rollout", "restart", "deployment/" + release + "-controller-manager"},
			{"-n", systemNS, "rollout", "status", "deployment/" + release + "-controller-manager", "--timeout", "5m"},
		} {
			out, err := utils.Run(exec.Command("kubectl", args...))
			Expect(err).NotTo(HaveOccurred(), out)
		}

		expectSucceeded(a)
		Expect(k8s.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
		Expect(a.Status.RunID).To(Equal(runID), "The run identity changed after the controller restart.")
		Expect(a.Status.Output).To(Equal(&corev1alpha1.AgentOutputStatus{Path: "/out/report.txt", SHA256: digest,
			Size: new(int64(6))}))
	})
})
