/*
Copyright 2024 The Kruise Authors.
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

package amazonswebservices

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gamekruiseiov1alpha1 "github.com/openkruise/kruise-game/apis/v1alpha1"
)

func newAGATestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return scheme
}

func makeAGACR(name, namespace string, listeners []interface{}, status map[string]interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": agaGroup + "/" + agaVersion,
			"kind":       agaKind,
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"listeners": listeners,
			},
		},
	}
	if status != nil {
		obj.Object["status"] = status
	}
	return obj
}

func makePerPortListener(nlbARN string, port int64) map[string]interface{} {
	return map[string]interface{}{
		"protocol": "TCP",
		"portRanges": []interface{}{
			map[string]interface{}{"fromPort": port, "toPort": port},
		},
		"clientAffinity": "SOURCE_IP",
		"endpointGroups": []interface{}{
			map[string]interface{}{
				"endpoints": []interface{}{
					map[string]interface{}{
						"type":                        "EndpointID",
						"endpointID":                  nlbARN,
						"weight":                      int64(128),
						"clientIPPreservationEnabled": true,
					},
				},
			},
		},
	}
}

func makeWideRangeListener(nlbARN string, fromPort, toPort int64) map[string]interface{} {
	return map[string]interface{}{
		"protocol": "TCP",
		"portRanges": []interface{}{
			map[string]interface{}{"fromPort": fromPort, "toPort": toPort},
		},
		"clientAffinity": "SOURCE_IP",
		"endpointGroups": []interface{}{
			map[string]interface{}{
				"endpoints": []interface{}{
					map[string]interface{}{
						"type":       "EndpointID",
						"endpointID": nlbARN,
						"weight":     int64(128),
					},
				},
			},
		},
	}
}

func TestSelectAGA_SingleMatch(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()

	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{}, nil)
	agaCR.SetLabels(map[string]string{"game.kruise.io/aga-pool": "test"})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	result, err := selectAGA(ctx, fakeClient, "game.kruise.io/aga-pool=test", "game-ns")
	if err != nil {
		t.Fatalf("selectAGA: %v", err)
	}
	if result.GetName() != "aga-1" {
		t.Errorf("got name %q, want aga-1", result.GetName())
	}
}

func TestSelectAGA_MultiplePicksLeastListeners(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()

	aga1 := makeAGACR("aga-busy", "game-ns",
		[]interface{}{makePerPortListener("arn:nlb", 32001), makePerPortListener("arn:nlb", 32002)}, nil)
	aga1.SetLabels(map[string]string{"pool": "a"})

	aga2 := makeAGACR("aga-empty", "game-ns", []interface{}{}, nil)
	aga2.SetLabels(map[string]string{"pool": "a"})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(aga1, aga2).
		Build()

	result, err := selectAGA(ctx, fakeClient, "pool=a", "game-ns")
	if err != nil {
		t.Fatalf("selectAGA: %v", err)
	}
	if result.GetName() != "aga-empty" {
		t.Errorf("expected aga-empty (least listeners), got %q", result.GetName())
	}
}

func TestSelectAGA_NoMatch(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := selectAGA(ctx, fakeClient, "pool=nonexist", "game-ns")
	if err == nil {
		t.Error("expected error when no AGA matches, got nil")
	}
}

func TestSelectAGA_InvalidSelector(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := selectAGA(ctx, fakeClient, "!!!invalid!!!", "game-ns")
	if err == nil {
		t.Error("expected error for invalid label selector, got nil")
	}
}

func TestAppendAGAListener(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{}, nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	err := appendAGAListener(ctx, fakeClient, agaCR, nlbARN, 32001, true, "game-ns/pod-0")
	if err != nil {
		t.Fatalf("appendAGAListener: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(agaGVK)
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "aga-1", Namespace: "game-ns"}, updated)
	if err != nil {
		t.Fatalf("get updated AGA: %v", err)
	}

	listeners := getListeners(updated)
	if len(listeners) != 1 {
		t.Fatalf("expected 1 listener after append, got %d", len(listeners))
	}

	if !matchesListener(listeners[0], nlbARN, 32001) {
		t.Error("appended listener doesn't match expected port/ARN")
	}
}

func TestAppendAGAListener_AlreadyExists(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	existingListener := makePerPortListener(nlbARN, 32001)
	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{existingListener}, nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	err := appendAGAListener(ctx, fakeClient, agaCR, nlbARN, 32001, true, "game-ns/pod-0")
	if err != nil {
		t.Fatalf("appendAGAListener (idempotent): %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(agaGVK)
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "aga-1", Namespace: "game-ns"}, updated)
	listeners := getListeners(updated)
	if len(listeners) != 1 {
		t.Errorf("expected listener count to remain 1 (idempotent), got %d", len(listeners))
	}
}

func TestRemoveAGAListener(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	listener1 := makePerPortListener(nlbARN, 32001)
	listener2 := makePerPortListener(nlbARN, 32002)
	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{listener1, listener2}, nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	err := removeAGAListener(ctx, fakeClient, "game-ns", "aga-1", nlbARN, 32001)
	if err != nil {
		t.Fatalf("removeAGAListener: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(agaGVK)
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "aga-1", Namespace: "game-ns"}, updated)
	listeners := getListeners(updated)
	if len(listeners) != 1 {
		t.Fatalf("expected 1 listener after remove, got %d", len(listeners))
	}
	if !matchesListener(listeners[0], nlbARN, 32002) {
		t.Error("remaining listener should be port 32002")
	}
}

func TestRemoveAGAListener_NotFound(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{makePerPortListener(nlbARN, 32099)}, nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	err := removeAGAListener(ctx, fakeClient, "game-ns", "aga-1", nlbARN, 32001)
	if err != nil {
		t.Fatalf("removeAGAListener (not found) should succeed silently: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(agaGVK)
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "aga-1", Namespace: "game-ns"}, updated)
	if len(getListeners(updated)) != 1 {
		t.Error("listener count should remain unchanged when target not found")
	}
}

func TestRemoveAGAListener_CRNotFound(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	err := removeAGAListener(ctx, fakeClient, "game-ns", "nonexist", "arn:nlb", 32001)
	if err == nil {
		t.Error("expected error when AGA CR not found")
	}
}

func TestReconcileAGAForPod_NoSelector(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	config := &nlbConfig{agaLabelSelector: ""}
	err := reconcileAGAForPod(ctx, fakeClient, config, "arn:nlb", []int32{32001}, "ns/pod-0")
	if err != nil {
		t.Errorf("expected nil when no AGA selector: %v", err)
	}
}

func TestReconcileAGAForPod_PreProvisioned(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	wideListener := makeWideRangeListener(nlbARN, 32001, 32050)
	agaCR := makeAGACR("aga-wide", "game-ns", []interface{}{wideListener}, nil)
	agaCR.SetLabels(map[string]string{"pool": "pre"})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = make(map[string]*agaAssignment)
	defer func() { globalAGAManager.assignments = oldAssignments }()

	config := &nlbConfig{
		agaLabelSelector: "pool=pre",
		agaNamespace:     "game-ns",
	}

	err := reconcileAGAForPod(ctx, fakeClient, config, nlbARN, []int32{32010}, "game-ns/pod-0")
	if err != nil {
		t.Fatalf("reconcileAGAForPod (pre-provisioned): %v", err)
	}

	assignKey := "game-ns/pod-0:32010"
	a := globalAGAManager.getAssignment(assignKey)
	if a == nil {
		t.Fatal("expected assignment to be recorded")
	}
	if a.agaName != "aga-wide" {
		t.Errorf("assignment AGA name = %q, want aga-wide", a.agaName)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(agaGVK)
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "aga-wide", Namespace: "game-ns"}, updated)
	if len(getListeners(updated)) != 1 {
		t.Error("pre-provisioned mode should NOT append a new listener")
	}
}

func TestReconcileAGAForPod_PerPort(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	agaCR := makeAGACR("aga-perport", "game-ns", []interface{}{}, nil)
	agaCR.SetLabels(map[string]string{"pool": "pp"})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = make(map[string]*agaAssignment)
	defer func() { globalAGAManager.assignments = oldAssignments }()

	config := &nlbConfig{
		agaLabelSelector:        "pool=pp",
		agaNamespace:            "game-ns",
		agaClientIPPreservation: true,
	}

	err := reconcileAGAForPod(ctx, fakeClient, config, nlbARN, []int32{32001, 32002}, "game-ns/pod-0")
	if err != nil {
		t.Fatalf("reconcileAGAForPod (per-port): %v", err)
	}

	if globalAGAManager.getAssignment("game-ns/pod-0:32001") == nil {
		t.Error("missing assignment for port 32001")
	}
	if globalAGAManager.getAssignment("game-ns/pod-0:32002") == nil {
		t.Error("missing assignment for port 32002")
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(agaGVK)
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "aga-perport", Namespace: "game-ns"}, updated)
	listeners := getListeners(updated)
	if len(listeners) != 2 {
		t.Errorf("expected 2 per-port listeners appended, got %d", len(listeners))
	}
}

func TestReconcileAGAForPod_SkipsExistingAssignment(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	nlbARN := "arn:nlb"

	agaCR := makeAGACR("aga-1", "ns", []interface{}{}, nil)
	agaCR.SetLabels(map[string]string{"pool": "x"})

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agaCR).Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "ns", nlbARN: nlbARN, port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	config := &nlbConfig{agaLabelSelector: "pool=x", agaNamespace: "ns"}
	err := reconcileAGAForPod(ctx, fakeClient, config, nlbARN, []int32{32001}, "ns/pod-0")
	if err != nil {
		t.Fatalf("reconcileAGAForPod (skip existing): %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(agaGVK)
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "aga-1", Namespace: "ns"}, updated)
	if len(getListeners(updated)) != 0 {
		t.Error("should not append listener when assignment already exists")
	}
}

func TestCleanupAGAForPod(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	listener := makePerPortListener(nlbARN, 32001)
	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{listener}, nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"game-ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "game-ns", nlbARN: nlbARN, port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	config := &nlbConfig{agaLabelSelector: "pool=x"}
	cleanupAGAForPod(ctx, fakeClient, config, "game-ns/pod-0")

	if globalAGAManager.getAssignment("game-ns/pod-0:32001") != nil {
		t.Error("assignment should be removed after cleanup")
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(agaGVK)
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "aga-1", Namespace: "game-ns"}, updated)
	if len(getListeners(updated)) != 0 {
		t.Error("per-port listener should be removed during cleanup")
	}
}

func TestCleanupAGAForPod_NoSelector(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	config := &nlbConfig{agaLabelSelector: ""}
	cleanupAGAForPod(ctx, fakeClient, config, "ns/pod-0")

	if globalAGAManager.getAssignment("ns/pod-0:32001") == nil {
		t.Error("cleanup should be no-op when agaLabelSelector is empty")
	}
}

func TestGetAGAIPsForPod_Deployed(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()

	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{}, map[string]interface{}{
		"status": "DEPLOYED",
		"ipSets": []interface{}{
			map[string]interface{}{
				"ipAddresses": []interface{}{"1.2.3.4", "5.6.7.8"},
			},
		},
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"game-ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "game-ns", nlbARN: "arn:nlb", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	config := &nlbConfig{agaLabelSelector: "pool=x"}
	ips := getAGAIPsForPod(ctx, fakeClient, config, "game-ns/pod-0")
	if len(ips) != 2 || ips[0] != "1.2.3.4" || ips[1] != "5.6.7.8" {
		t.Errorf("getAGAIPsForPod = %v, want [1.2.3.4 5.6.7.8]", ips)
	}
}

func TestGetAGAIPsForPod_NotDeployed(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()

	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{}, map[string]interface{}{
		"status": "IN_PROGRESS",
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"game-ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "game-ns", nlbARN: "arn:nlb", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	config := &nlbConfig{agaLabelSelector: "pool=x"}
	ips := getAGAIPsForPod(ctx, fakeClient, config, "game-ns/pod-0")
	if len(ips) != 0 {
		t.Errorf("expected no IPs when not DEPLOYED, got %v", ips)
	}
}

func TestGetAGAIPsForPod_NoAssignment(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = make(map[string]*agaAssignment)
	defer func() { globalAGAManager.assignments = oldAssignments }()

	config := &nlbConfig{agaLabelSelector: "pool=x"}
	ips := getAGAIPsForPod(ctx, fakeClient, config, "game-ns/pod-0")
	if ips != nil {
		t.Errorf("expected nil IPs when no assignment, got %v", ips)
	}
}

func TestGetAGAIPsForPod_NoSelector(t *testing.T) {
	ctx := context.Background()
	fakeClient := fake.NewClientBuilder().WithScheme(newAGATestScheme()).Build()

	config := &nlbConfig{agaLabelSelector: ""}
	ips := getAGAIPsForPod(ctx, fakeClient, config, "ns/pod")
	if ips != nil {
		t.Errorf("expected nil when no AGA selector, got %v", ips)
	}
}

func TestUpdatePodsWithAGAIPs(t *testing.T) {
	scheme := newAGATestScheme()
	_ = gamekruiseiov1alpha1.AddToScheme(scheme)
	ctx := context.Background()

	networkStatus := gamekruiseiov1alpha1.NetworkStatus{
		CurrentNetworkState: gamekruiseiov1alpha1.NetworkReady,
		ExternalAddresses: []gamekruiseiov1alpha1.NetworkAddress{
			{IP: "old-ip"},
		},
	}
	statusBytes, _ := json.Marshal(networkStatus)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-0",
			Namespace: "game-ns",
			Annotations: map[string]string{
				gamekruiseiov1alpha1.GameServerNetworkStatus: string(statusBytes),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"game-ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "game-ns", nlbARN: "arn:nlb", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	updatePodsWithAGAIPs(ctx, fakeClient, "aga-1", "game-ns", []string{"10.0.0.1", "10.0.0.2"})

	updated := &corev1.Pod{}
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "pod-0", Namespace: "game-ns"}, updated)
	statusAnno := updated.Annotations[gamekruiseiov1alpha1.GameServerNetworkStatus]
	var ns gamekruiseiov1alpha1.NetworkStatus
	_ = json.Unmarshal([]byte(statusAnno), &ns)

	if len(ns.ExternalAddresses) == 0 || ns.ExternalAddresses[0].IP != "10.0.0.1,10.0.0.2" {
		t.Errorf("pod IP not updated, got %v", ns.ExternalAddresses)
	}
}

func TestUpdatePodsWithAGAIPs_NotReady(t *testing.T) {
	scheme := newAGATestScheme()
	_ = gamekruiseiov1alpha1.AddToScheme(scheme)
	ctx := context.Background()

	networkStatus := gamekruiseiov1alpha1.NetworkStatus{
		CurrentNetworkState: gamekruiseiov1alpha1.NetworkNotReady,
		ExternalAddresses: []gamekruiseiov1alpha1.NetworkAddress{
			{IP: "old-ip"},
		},
	}
	statusBytes, _ := json.Marshal(networkStatus)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-0",
			Namespace: "ns",
			Annotations: map[string]string{
				gamekruiseiov1alpha1.GameServerNetworkStatus: string(statusBytes),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	updatePodsWithAGAIPs(ctx, fakeClient, "aga-1", "ns", []string{"10.0.0.1"})

	updated := &corev1.Pod{}
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "pod-0", Namespace: "ns"}, updated)
	statusAnno := updated.Annotations[gamekruiseiov1alpha1.GameServerNetworkStatus]
	var ns gamekruiseiov1alpha1.NetworkStatus
	_ = json.Unmarshal([]byte(statusAnno), &ns)

	if ns.ExternalAddresses[0].IP != "old-ip" {
		t.Error("should not update IP when network is NotReady")
	}
}

func TestRebuildAGACacheFromServices(t *testing.T) {
	scheme := newAGATestScheme()
	_ = gamekruiseiov1alpha1.AddToScheme(scheme)
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	confParams := []gamekruiseiov1alpha1.NetworkConfParams{
		{Name: NlbARNsConfigName, Value: nlbARN},
		{Name: PortProtocolsConfigName, Value: "8080/TCP"},
		{Name: AGALabelSelectorConfigName, Value: "pool=rebuild"},
		{Name: AGANamespaceConfigName, Value: "game-ns"},
	}
	confBytes, _ := json.Marshal(confParams)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-0",
			Namespace: "game-ns",
			Annotations: map[string]string{
				gamekruiseiov1alpha1.GameServerNetworkConf: string(confBytes),
			},
		},
	}

	wideListener := makeWideRangeListener(nlbARN, 32001, 32050)
	agaCR := makeAGACR("aga-rebuild", "game-ns", []interface{}{wideListener},
		map[string]interface{}{"status": "DEPLOYED", "ipSets": []interface{}{
			map[string]interface{}{"ipAddresses": []interface{}{"9.9.9.9"}},
		}})
	agaCR.SetLabels(map[string]string{"pool": "rebuild"})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod, agaCR).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = make(map[string]*agaAssignment)
	defer func() { globalAGAManager.assignments = oldAssignments }()

	svcList := []corev1.Service{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-0",
				Namespace: "game-ns",
				Annotations: map[string]string{
					NlbARNAnnoKey: nlbARN,
				},
				Labels: map[string]string{
					ResourceTagKey: ResourceTagValue,
					SvcSelectorKey: "pod-0",
				},
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{Port: 32010},
				},
			},
		},
	}

	rebuildAGACacheFromServices(ctx, fakeClient, svcList)

	a := globalAGAManager.getAssignment("game-ns/pod-0:32010")
	if a == nil {
		t.Fatal("expected assignment to be rebuilt")
	}
	if a.agaName != "aga-rebuild" || a.agaNamespace != "game-ns" {
		t.Errorf("assignment = %+v, want aga-rebuild/game-ns", a)
	}
}

func TestRebuildAGACacheFromServices_NoNlbARN(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = make(map[string]*agaAssignment)
	defer func() { globalAGAManager.assignments = oldAssignments }()

	svcList := []corev1.Service{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "svc-no-arn", Namespace: "ns",
				Labels: map[string]string{SvcSelectorKey: "pod-0"},
			},
		},
	}

	rebuildAGACacheFromServices(ctx, fakeClient, svcList)

	if len(globalAGAManager.assignments) != 0 {
		t.Error("should not rebuild assignments when NLB ARN is missing")
	}
}

func TestReconcileAGAIPsOnStartup(t *testing.T) {
	scheme := newAGATestScheme()
	_ = gamekruiseiov1alpha1.AddToScheme(scheme)
	ctx := context.Background()

	agaCR := makeAGACR("aga-1", "game-ns", []interface{}{}, map[string]interface{}{
		"status": "DEPLOYED",
		"ipSets": []interface{}{
			map[string]interface{}{"ipAddresses": []interface{}{"10.0.0.1"}},
		},
	})

	networkStatus := gamekruiseiov1alpha1.NetworkStatus{
		CurrentNetworkState: gamekruiseiov1alpha1.NetworkReady,
		ExternalAddresses: []gamekruiseiov1alpha1.NetworkAddress{
			{IP: "old-ip"},
		},
	}
	statusBytes, _ := json.Marshal(networkStatus)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-0", Namespace: "game-ns",
			Annotations: map[string]string{
				gamekruiseiov1alpha1.GameServerNetworkStatus: string(statusBytes),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agaCR, pod).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"game-ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "game-ns", nlbARN: "arn:nlb", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	reconcileAGAIPsOnStartup(ctx, fakeClient)

	updated := &corev1.Pod{}
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "pod-0", Namespace: "game-ns"}, updated)
	statusAnno := updated.Annotations[gamekruiseiov1alpha1.GameServerNetworkStatus]
	var ns gamekruiseiov1alpha1.NetworkStatus
	_ = json.Unmarshal([]byte(statusAnno), &ns)

	if len(ns.ExternalAddresses) == 0 || ns.ExternalAddresses[0].IP != "10.0.0.1" {
		t.Errorf("reconcileAGAIPsOnStartup did not update pod IP: %v", ns.ExternalAddresses)
	}
}

func TestInitAGACache(t *testing.T) {
	scheme := newAGATestScheme()
	_ = gamekruiseiov1alpha1.AddToScheme(scheme)
	ctx := context.Background()
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:111:loadbalancer/net/nlb/abc"

	confParams := []gamekruiseiov1alpha1.NetworkConfParams{
		{Name: NlbARNsConfigName, Value: nlbARN},
		{Name: PortProtocolsConfigName, Value: "8080/TCP"},
		{Name: AGALabelSelectorConfigName, Value: "pool=init"},
		{Name: AGANamespaceConfigName, Value: "game-ns"},
	}
	confBytes, _ := json.Marshal(confParams)

	networkStatus := gamekruiseiov1alpha1.NetworkStatus{
		CurrentNetworkState: gamekruiseiov1alpha1.NetworkReady,
		ExternalAddresses: []gamekruiseiov1alpha1.NetworkAddress{
			{IP: "1.2.3.4"},
		},
	}
	statusBytes, _ := json.Marshal(networkStatus)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-0", Namespace: "game-ns",
			Labels: map[string]string{ResourceTagKey: ResourceTagValue},
			Annotations: map[string]string{
				gamekruiseiov1alpha1.GameServerNetworkConf:   string(confBytes),
				gamekruiseiov1alpha1.GameServerNetworkStatus: string(statusBytes),
			},
		},
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-0", Namespace: "game-ns",
			Annotations: map[string]string{NlbARNAnnoKey: nlbARN},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 32010}},
		},
	}

	wideListener := makeWideRangeListener(nlbARN, 32001, 32050)
	agaCR := makeAGACR("aga-init", "game-ns", []interface{}{wideListener}, nil)
	agaCR.SetLabels(map[string]string{"pool": "init"})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod, svc, agaCR).
		Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = make(map[string]*agaAssignment)
	defer func() { globalAGAManager.assignments = oldAssignments }()

	initAGACache(ctx, fakeClient)

	a := globalAGAManager.getAssignment("game-ns/pod-0:32010")
	if a == nil {
		t.Fatal("expected assignment from initAGACache")
	}
	if a.agaName != "aga-init" {
		t.Errorf("assignment AGA = %q, want aga-init", a.agaName)
	}
}

func TestHandleAGAAdd_DeployedWithAssignment(t *testing.T) {
	scheme := newAGATestScheme()
	_ = gamekruiseiov1alpha1.AddToScheme(scheme)
	ctx := context.Background()

	networkStatus := gamekruiseiov1alpha1.NetworkStatus{
		CurrentNetworkState: gamekruiseiov1alpha1.NetworkReady,
		ExternalAddresses:   []gamekruiseiov1alpha1.NetworkAddress{{IP: "old"}},
	}
	statusBytes, _ := json.Marshal(networkStatus)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-0", Namespace: "ns",
			Annotations: map[string]string{
				gamekruiseiov1alpha1.GameServerNetworkStatus: string(statusBytes),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"ns/pod-0:32001": {agaName: "aga-add", agaNamespace: "ns", nlbARN: "arn", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	agaObj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "aga-add", "namespace": "ns"},
		"status": map[string]interface{}{
			"status": "DEPLOYED",
			"ipSets": []interface{}{
				map[string]interface{}{"ipAddresses": []interface{}{"99.99.99.99"}},
			},
		},
	}}

	handleAGAAdd(ctx, fakeClient, agaObj)

	updated := &corev1.Pod{}
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "pod-0", Namespace: "ns"}, updated)
	statusAnno := updated.Annotations[gamekruiseiov1alpha1.GameServerNetworkStatus]
	var ns gamekruiseiov1alpha1.NetworkStatus
	_ = json.Unmarshal([]byte(statusAnno), &ns)

	if len(ns.ExternalAddresses) == 0 || ns.ExternalAddresses[0].IP != "99.99.99.99" {
		t.Errorf("handleAGAAdd should update pod IP, got %v", ns.ExternalAddresses)
	}
}

func TestHandleAGAAdd_NotDeployed(t *testing.T) {
	scheme := newAGATestScheme()
	ctx := context.Background()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	agaObj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "aga-1", "namespace": "ns"},
		"status":   map[string]interface{}{"status": "IN_PROGRESS"},
	}}

	handleAGAAdd(ctx, fakeClient, agaObj)
	// no panic, no-op — that's the pass condition
}

func TestHandleAGAStatusChange_TransitionTrigger(t *testing.T) {
	scheme := newAGATestScheme()
	_ = gamekruiseiov1alpha1.AddToScheme(scheme)
	ctx := context.Background()

	networkStatus := gamekruiseiov1alpha1.NetworkStatus{
		CurrentNetworkState: gamekruiseiov1alpha1.NetworkReady,
		ExternalAddresses:   []gamekruiseiov1alpha1.NetworkAddress{{IP: "old"}},
	}
	statusBytes, _ := json.Marshal(networkStatus)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-0", Namespace: "ns",
			Annotations: map[string]string{
				gamekruiseiov1alpha1.GameServerNetworkStatus: string(statusBytes),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

	oldAssignments := globalAGAManager.assignments
	globalAGAManager.assignments = map[string]*agaAssignment{
		"ns/pod-0:32001": {agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn", port: 32001},
	}
	defer func() { globalAGAManager.assignments = oldAssignments }()

	oldCR := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "aga-1", "namespace": "ns"},
		"status":   map[string]interface{}{"status": "IN_PROGRESS"},
	}}
	newCR := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "aga-1", "namespace": "ns"},
		"status": map[string]interface{}{
			"status": "DEPLOYED",
			"ipSets": []interface{}{
				map[string]interface{}{"ipAddresses": []interface{}{"77.77.77.77"}},
			},
		},
	}}

	handleAGAStatusChange(ctx, fakeClient, oldCR, newCR)

	updated := &corev1.Pod{}
	_ = fakeClient.Get(ctx, types.NamespacedName{Name: "pod-0", Namespace: "ns"}, updated)
	statusAnno := updated.Annotations[gamekruiseiov1alpha1.GameServerNetworkStatus]
	var ns gamekruiseiov1alpha1.NetworkStatus
	_ = json.Unmarshal([]byte(statusAnno), &ns)

	if len(ns.ExternalAddresses) == 0 || ns.ExternalAddresses[0].IP != "77.77.77.77" {
		t.Errorf("status change should update pod IP, got %v", ns.ExternalAddresses)
	}
}

// Verify that the fake client supports list+patch with unstructured GVK correctly.
func TestFakeClientUnstructuredListAndPatch(t *testing.T) {
	scheme := runtime.NewScheme()
	ctx := context.Background()

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": agaGroup + "/" + agaVersion,
			"kind":       agaKind,
			"metadata": map[string]interface{}{
				"name":      "test-aga",
				"namespace": "ns",
				"labels":    map[string]interface{}{"env": "test"},
			},
			"spec": map[string]interface{}{
				"listeners": []interface{}{},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: agaGroup, Version: agaVersion, Kind: agaKind})
	err := fc.List(ctx, list, &client.ListOptions{Namespace: "ns"})
	if err != nil {
		t.Fatalf("list unstructured: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(list.Items))
	}

	patch := []map[string]interface{}{
		{"op": "add", "path": "/spec/listeners/-", "value": map[string]interface{}{"protocol": "TCP"}},
	}
	patchBytes, _ := json.Marshal(patch)
	err = fc.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, patchBytes))
	if err != nil {
		t.Fatalf("JSON patch on unstructured: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(agaGVK)
	_ = fc.Get(ctx, types.NamespacedName{Name: "test-aga", Namespace: "ns"}, got)
	listeners := getListeners(got)
	if len(listeners) != 1 {
		t.Errorf("expected 1 listener after patch, got %d", len(listeners))
	}
}
