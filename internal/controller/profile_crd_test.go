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

package controller

import (
	"context"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

var _ = Describe("Profile costGroups CRD", func() {
	DescribeTable("checks the allowed set", func(groups []string, valid bool) {
		p := &corev1beta1.Profile{ObjectMeta: metav1.ObjectMeta{GenerateName: "cost-crd-"},
			Spec: corev1beta1.ProfileSpec{CostGroups: groups}}
		err := k8sClient.Create(context.Background(), p)
		if err == nil {
			DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), p)).To(Succeed()) })
		}
		if valid {
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Spec.CostGroups).To(ConsistOf(groups))
		} else {
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
			Expect(err.Error()).To(ContainSubstring("spec.costGroups"))
		}
	},
		Entry("permits valid values", []string{testCostGroupDenied, testCostGroupAllowed, "0", strings.Repeat("a", 63)}, true),
		Entry("refuses a value outside the pattern", []string{"Project_A"}, false),
		Entry("refuses an empty value", []string{""}, false),
		Entry("refuses a long value", []string{strings.Repeat("a", 64)}, false),
		Entry("refuses more than 32 values", func() []string {
			groups := make([]string, 33)
			for i := range groups {
				groups[i] = "group-" + strconv.Itoa(i)
			}
			return groups
		}(), false),
	)
})
