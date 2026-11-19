/*
Copyright 2026 The Flux authors

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
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/events"

	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
)

// TestKustomizationReconciler_DuplicateReconcile verifies that reconciling
// the same object multiple times converges to the same status instead of
// accumulating duplicate conditions or rewriting the status on every pass.
func TestKustomizationReconciler_DuplicateReconcile(t *testing.T) {
	g := NewWithT(t)

	namespaceName := "dup-" + randStringRunes(5)
	namespace := &corev1.Namespace{
		Name: namespaceName,
	}
	g.Expect(k8sClient.Create(ctx, namespace)).ToNot(HaveOccurred())
	t.Cleanup(func() {
		g.Expect(k8sClient.Delete(ctx, namespace)).NotTo(HaveOccurred())
	})

	kustomization := &kustomizev1.Kustomization{}
	kustomization.Name = "test-dup"
	kustomization.Namespace = namespaceName
	kustomization.Spec = kustomizev1.KustomizationSpec{
		Interval: metav1.Duration{Duration: interval},
		SourceRef: kustomizev1.CrossNamespaceSourceReference{
			Kind: "Bucket",
			Name: "missing",
		},
	}
	// Set the finalizer up front so the reconcile proceeds to the source
	// resolution instead of short-circuiting on the finalizer addition.
	kustomization.SetFinalizers([]string{kustomizev1.KustomizationFinalizer})
	g.Expect(k8sClient.Create(ctx, kustomization)).NotTo(HaveOccurred())

	r := &KustomizationReconciler{
		Client:        k8sClient,
		EventRecorder: events.NewFakeRecorder(32, true),
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(kustomization)}

	readyConditions := func() []metav1.Condition {
		resultK := &kustomizev1.Kustomization{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(kustomization), resultK)).To(Succeed())
		var ready []metav1.Condition
		for _, c := range resultK.Status.Conditions {
			if c.Type == meta.ReadyCondition {
				ready = append(ready, c)
			}
		}
		return ready
	}

	// First reconcile: the missing source converges the object to not ready.
	result1, err := r.Reconcile(ctx, req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result1.RequeueAfter).To(Equal(kustomization.GetRetryInterval()))

	first := readyConditions()
	g.Expect(first).To(HaveLen(1))
	g.Expect(first[0].Status).To(Equal(metav1.ConditionFalse))
	g.Expect(first[0].Reason).To(Equal(meta.ArtifactFailedReason))

	// Duplicate reconcile of the same object must be idempotent: same result,
	// same single Ready condition, no status drift.
	result2, err := r.Reconcile(ctx, req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result2).To(Equal(result1))

	second := readyConditions()
	g.Expect(second).To(HaveLen(1))
	g.Expect(second[0].Status).To(Equal(first[0].Status))
	g.Expect(second[0].Reason).To(Equal(first[0].Reason))
	g.Expect(second[0].Message).To(Equal(first[0].Message))
	g.Expect(second[0].ObservedGeneration).To(Equal(first[0].ObservedGeneration))

	// The condition set must not pile up either.
	resultK := &kustomizev1.Kustomization{}
	g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(kustomization), resultK)).To(Succeed())
	g.Expect(resultK.Status.Conditions).To(HaveLen(1))
}
