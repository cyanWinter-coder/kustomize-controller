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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
)

// TestKustomizationReconciler_SourceDeletion verifies that deleting the
// source object converges the Kustomization to a not-ready state without
// garbage collecting the managed resources: the status inventory and the
// last applied revision are preserved so that reconciliation can resume
// when the source comes back.
func TestKustomizationReconciler_SourceDeletion(t *testing.T) {
	g := NewWithT(t)
	id := "srcdel-" + randStringRunes(5)
	revision := "v1.0.0"

	err := createNamespace(id)
	g.Expect(err).NotTo(HaveOccurred(), "failed to create test namespace")

	err = createKubeConfigSecret(id)
	g.Expect(err).NotTo(HaveOccurred(), "failed to create kubeconfig secret")

	manifests := func(name string, data string) []testserver.File {
		return []testserver.File{
			{
				Name: "secret.yaml",
				Body: fmt.Sprintf(`---
apiVersion: v1
kind: Secret
metadata:
  name: %[1]s
stringData:
  key: "%[2]s"
`, name, data),
			},
		}
	}

	artifact, err := testServer.ArtifactFromFiles(manifests(id, id))
	g.Expect(err).NotTo(HaveOccurred(), "failed to create artifact from files")

	repositoryName := types.NamespacedName{
		Name:      fmt.Sprintf("srcdel-%s", randStringRunes(5)),
		Namespace: id,
	}

	err = applyGitRepository(repositoryName, artifact, revision)
	g.Expect(err).NotTo(HaveOccurred())

	kustomizationKey := types.NamespacedName{
		Name:      fmt.Sprintf("srcdel-%s", randStringRunes(5)),
		Namespace: id,
	}
	kustomization := &kustomizev1.Kustomization{
		Name:      kustomizationKey.Name,
		Namespace: kustomizationKey.Namespace,
		Spec: kustomizev1.KustomizationSpec{
			Interval: metav1.Duration{Duration: reconciliationInterval},
			Path:     "./",
			KubeConfig: &meta.KubeConfigReference{
				SecretRef: &meta.SecretKeyReference{
					Name: "kubeconfig",
				},
			},
			SourceRef: kustomizev1.CrossNamespaceSourceReference{
				Name:      repositoryName.Name,
				Namespace: repositoryName.Namespace,
				Kind:      sourcev1.GitRepositoryKind,
			},
			TargetNamespace: id,
			Prune:           true,
		},
	}

	g.Expect(k8sClient.Create(context.Background(), kustomization)).To(Succeed())

	resultK := &kustomizev1.Kustomization{}

	t.Run("reconciles to ready", func(t *testing.T) {
		g.Eventually(func() bool {
			_ = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(kustomization), resultK)
			return isReconcileSuccess(resultK)
		}, timeout, time.Second).Should(BeTrue())
		g.Expect(resultK.Status.Inventory).NotTo(BeNil())
		g.Expect(resultK.Status.Inventory.Entries).NotTo(BeEmpty())
	})

	t.Run("source deletion preserves inventory and managed resources", func(t *testing.T) {
		inventoryEntries := resultK.Status.Inventory.DeepCopy()

		repo := &sourcev1.GitRepository{}
		g.Expect(k8sClient.Get(context.Background(), repositoryName, repo)).To(Succeed())
		g.Expect(k8sClient.Delete(context.Background(), repo)).To(Succeed())

		// The Kustomization converges to not ready with an artifact failure.
		g.Eventually(func() bool {
			_ = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(kustomization), resultK)
			ready := apimeta.FindStatusCondition(resultK.Status.Conditions, meta.ReadyCondition)
			return ready != nil &&
				ready.Status == metav1.ConditionFalse &&
				ready.Reason == meta.ArtifactFailedReason
		}, timeout, time.Second).Should(BeTrue())

		// The last applied revision and the inventory are not reset, and the
		// managed Secret is not garbage collected while the source is gone.
		g.Expect(resultK.Status.LastAppliedRevision).To(Equal(revision))
		g.Expect(resultK.Status.Inventory.Entries).To(Equal(inventoryEntries.Entries))
		g.Consistently(func() error {
			return k8sClient.Get(context.Background(), types.NamespacedName{Name: id, Namespace: id}, &corev1.Secret{})
		}, 5*time.Second, time.Second).Should(Succeed())
	})

	t.Run("recovers when the source is recreated", func(t *testing.T) {
		err = applyGitRepository(repositoryName, artifact, revision)
		g.Expect(err).NotTo(HaveOccurred())

		g.Eventually(func() bool {
			_ = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(kustomization), resultK)
			return isReconcileSuccess(resultK)
		}, timeout, time.Second).Should(BeTrue())
	})
}
