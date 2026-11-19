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
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	crtlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	pkgcache "github.com/fluxcd/pkg/cache"
	"github.com/fluxcd/pkg/runtime/events"

	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
)

func TestKustomizationReconciler_DeletesDurationMetricOnNotFound(t *testing.T) {
	g := NewWithT(t)

	name := "metrics-" + randStringRunes(5)
	namespace := "default"
	ref := corev1.ObjectReference{
		Kind:      kustomizev1.KustomizationKind,
		Name:      name,
		Namespace: namespace,
	}

	// Simulate a series left behind by a previous reconcile of an object that
	// was force-deleted before the controller observed its finalizer removal.
	g.Expect(testMetricsH.MetricsRecorder).NotTo(BeNil())
	testMetricsH.MetricsRecorder.RecordDuration(ref, time.Now())
	g.Expect(countDurationSeries(g, name, namespace)).To(Equal(1))

	// The object does not exist, so Get returns NotFound and the deferred
	// recording is never registered.
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		Name: name, Namespace: namespace,
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(countDurationSeries(g, name, namespace)).To(Equal(0))
}

// countDurationSeries returns the number of gotk_reconcile_duration_seconds
// series matching the given object name and namespace.
func countDurationSeries(g *WithT, name, namespace string) int {
	return countSeries(g, "gotk_reconcile_duration_seconds", name, namespace)
}

// fakeToken is a cache.Token implementation for tests.
type fakeToken struct{}

func (fakeToken) GetDuration() time.Duration { return time.Hour }

func TestKustomizationReconciler_DeletesTokenCacheEventsOnNotFound(t *testing.T) {
	g := NewWithT(t)

	name := "cache-" + randStringRunes(5)
	namespace := "default"

	tokenCache, err := pkgcache.NewTokenCache(100,
		pkgcache.WithMetricsRegisterer(crtlmetrics.Registry),
		pkgcache.WithMetricsPrefix("gotk_token_"))
	g.Expect(err).NotTo(HaveOccurred())

	// Simulate cache event series left behind by a previous reconcile of an
	// object that was deleted before its finalizer ran.
	for _, op := range kustomizev1.AllMetrics {
		_, _, err := tokenCache.GetOrSet(ctx, "key-"+op+"-"+name,
			func(context.Context) (pkgcache.Token, error) {
				return fakeToken{}, nil
			},
			pkgcache.WithInvolvedObject(pkgcache.InvolvedObject{
				Kind:      kustomizev1.KustomizationKind,
				Name:      name,
				Namespace: namespace,
				Operation: op,
			}))
		g.Expect(err).NotTo(HaveOccurred())
	}
	// Each operation records a cache_miss series for the object.
	g.Expect(countSeries(g, "gotk_token_cache_events_total", name, namespace)).
		To(Equal(len(kustomizev1.AllMetrics)))

	// The object does not exist, so Get returns NotFound and the finalizer
	// cleanup never ran for it.
	r := &KustomizationReconciler{
		Client:        k8sClient,
		EventRecorder: events.NewFakeRecorder(32, true),
		TokenCache:    tokenCache,
	}
	_, err = r.Reconcile(ctx, ctrl.Request{
		Name: name, Namespace: namespace,
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(countSeries(g, "gotk_token_cache_events_total", name, namespace)).To(Equal(0))
}

// countSeries returns the number of series of the given metric family
// matching the given object name and namespace.
func countSeries(g *WithT, family, name, namespace string) int {
	families, err := crtlmetrics.Registry.Gather()
	g.Expect(err).NotTo(HaveOccurred())

	count := 0
	for _, f := range families {
		if f.GetName() != family {
			continue
		}
		for _, metric := range f.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["name"] == name && labels["namespace"] == namespace {
				count++
			}
		}
	}
	return count
}
