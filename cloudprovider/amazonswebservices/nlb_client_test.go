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
	"sync"
	"testing"

	ackv1alpha1 "github.com/aws-controllers-k8s/elbv2-controller/apis/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	elbv2api "sigs.k8s.io/aws-load-balancer-controller/apis/elbv2/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openkruise/kruise-game/pkg/util"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = ackv1alpha1.AddToScheme(scheme)
	_ = elbv2api.AddToScheme(scheme)
	return scheme
}

func TestSyncTargetGroupAndService_Create(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "game-pod-0",
			Namespace: "default",
			UID:       "uid-123",
		},
	}

	config := &nlbConfig{
		loadBalancerARNs: []string{"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/abc"},
		healthCheck:      &healthCheck{},
		vpcID:            "vpc-123",
		backends: []*backend{
			{targetPort: 8080, protocol: corev1.ProtocolTCP},
		},
		isFixed:     false,
		annotations: map[string]string{"custom-anno": "val"},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

	n := &NlbPlugin{
		maxPort:     int32(32100),
		minPort:     int32(32001),
		cache:       make(map[string]portAllocated),
		podAllocate: make(map[string]*nlbPorts),
		mutex:       sync.RWMutex{},
	}

	err := n.syncTargetGroupAndService(config, pod, fakeClient, ctx)
	if err != nil {
		t.Fatalf("syncTargetGroupAndService create failed: %v", err)
	}

	svc := &corev1.Service{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "game-pod-0", Namespace: "default"}, svc)
	if err != nil {
		t.Fatalf("service not created: %v", err)
	}

	if svc.Annotations[NlbConfigHashKey] != util.GetHash(config) {
		t.Errorf("NlbConfigHashKey = %q, want %q", svc.Annotations[NlbConfigHashKey], util.GetHash(config))
	}
	if svc.Annotations["custom-anno"] != "val" {
		t.Errorf("custom annotation not set")
	}
	if svc.Labels[ResourceTagKey] != ResourceTagValue {
		t.Errorf("managed-by label not set")
	}
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("service type = %v, want ClusterIP", svc.Spec.Type)
	}
	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(svc.Spec.Ports))
	}
	if svc.Spec.Ports[0].TargetPort.IntValue() != 8080 {
		t.Errorf("targetPort = %d, want 8080", svc.Spec.Ports[0].TargetPort.IntValue())
	}

	podKey := "default/game-pod-0"
	allocatedPorts := n.podAllocate[podKey]
	if allocatedPorts == nil {
		t.Fatalf("podAllocate missing for %s", podKey)
	}
	port := allocatedPorts.ports[0]

	tg := &ackv1alpha1.TargetGroup{}
	tgName := "game-pod-0-" + string(rune('0'+port/10000)) // just use the constructed name
	_ = tgName
	tgList := &ackv1alpha1.TargetGroupList{}
	err = fakeClient.List(ctx, tgList, client.InNamespace("default"))
	if err != nil {
		t.Fatalf("list target groups: %v", err)
	}
	if len(tgList.Items) != 1 {
		t.Fatalf("expected 1 target group, got %d", len(tgList.Items))
	}
	tg = &tgList.Items[0]
	if tg.Annotations[NlbARNAnnoKey] != config.loadBalancerARNs[0] {
		t.Errorf("TG NLB ARN = %q, want %q", tg.Annotations[NlbARNAnnoKey], config.loadBalancerARNs[0])
	}
	if *tg.Spec.VPCID != "vpc-123" {
		t.Errorf("TG VPCID = %q, want vpc-123", *tg.Spec.VPCID)
	}
}

func TestSyncTargetGroupAndService_UpdateConfigHash(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "game-pod-1",
			Namespace: "default",
			UID:       "uid-456",
		},
	}

	oldConfig := &nlbConfig{
		loadBalancerARNs: []string{"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/old"},
		healthCheck:      &healthCheck{},
		vpcID:            "vpc-old",
		backends:         []*backend{{targetPort: 8080, protocol: corev1.ProtocolTCP}},
		isFixed:          false,
		annotations:      map[string]string{},
	}

	existingSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "game-pod-1",
			Namespace: "default",
			Annotations: map[string]string{
				NlbARNAnnoKey:    "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/old",
				NlbConfigHashKey: util.GetHash(oldConfig),
			},
			Labels: map[string]string{
				ResourceTagKey: ResourceTagValue,
				SvcSelectorKey: "game-pod-1",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{Name: "8080", Port: 32001, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, existingSvc).Build()

	n := &NlbPlugin{
		maxPort: int32(32100),
		minPort: int32(32001),
		cache:   make(map[string]portAllocated),
		podAllocate: map[string]*nlbPorts{
			"default/game-pod-1": {
				arn:   "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/new",
				ports: []int32{32001},
			},
		},
		mutex: sync.RWMutex{},
	}

	newConfig := &nlbConfig{
		loadBalancerARNs: []string{"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/new"},
		healthCheck:      &healthCheck{healthCheckEnabled: ptr.To(true)},
		vpcID:            "vpc-new",
		backends:         []*backend{{targetPort: 9090, protocol: corev1.ProtocolTCP}},
		isFixed:          false,
		annotations:      map[string]string{"new-anno": "new-val"},
	}

	err := n.syncTargetGroupAndService(newConfig, pod, fakeClient, ctx)
	if err != nil {
		t.Fatalf("syncTargetGroupAndService update failed: %v", err)
	}

	svc := &corev1.Service{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "game-pod-1", Namespace: "default"}, svc)
	if err != nil {
		t.Fatalf("get service: %v", err)
	}

	expectedHash := util.GetHash(newConfig)
	if svc.Annotations[NlbConfigHashKey] != expectedHash {
		t.Errorf("NlbConfigHashKey after update = %q, want %q (regression: empty mutate func bug)",
			svc.Annotations[NlbConfigHashKey], expectedHash)
	}
	if svc.Annotations[NlbARNAnnoKey] != "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/new" {
		t.Errorf("NLB ARN not updated in service annotations")
	}
	if svc.Annotations["new-anno"] != "new-val" {
		t.Errorf("new custom annotation not applied after update")
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].TargetPort.IntValue() != 9090 {
		t.Errorf("service ports not updated: %v", svc.Spec.Ports)
	}
}

func TestSyncTargetGroupAndService_MultipleBackends(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "game-pod-2",
			Namespace: "game-ns",
			UID:       "uid-789",
		},
	}

	config := &nlbConfig{
		loadBalancerARNs: []string{"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/multi"},
		healthCheck:      &healthCheck{},
		vpcID:            "vpc-multi",
		backends: []*backend{
			{targetPort: 8080, protocol: corev1.ProtocolTCP},
			{targetPort: 8081, protocol: corev1.ProtocolUDP},
		},
		isFixed:     false,
		annotations: map[string]string{},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

	n := &NlbPlugin{
		maxPort:     int32(32100),
		minPort:     int32(32001),
		cache:       make(map[string]portAllocated),
		podAllocate: make(map[string]*nlbPorts),
		mutex:       sync.RWMutex{},
	}

	err := n.syncTargetGroupAndService(config, pod, fakeClient, ctx)
	if err != nil {
		t.Fatalf("syncTargetGroupAndService multiple backends failed: %v", err)
	}

	tgList := &ackv1alpha1.TargetGroupList{}
	err = fakeClient.List(ctx, tgList, client.InNamespace("game-ns"))
	if err != nil {
		t.Fatalf("list TGs: %v", err)
	}
	if len(tgList.Items) != 2 {
		t.Errorf("expected 2 target groups, got %d", len(tgList.Items))
	}

	svc := &corev1.Service{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "game-pod-2", Namespace: "game-ns"}, svc)
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if len(svc.Spec.Ports) != 2 {
		t.Errorf("expected 2 service ports, got %d", len(svc.Spec.Ports))
	}
}

func TestSyncListenerAndTargetGroupBinding_Create(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	tg := &ackv1alpha1.TargetGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "game-pod-0-32001",
			Namespace: "default",
			Labels: map[string]string{
				ResourceTagKey: ResourceTagValue,
				SvcSelectorKey: "game-pod-0",
			},
			Annotations: map[string]string{
				NlbARNAnnoKey:  "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/abc",
				NlbPortAnnoKey: "32001",
			},
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "v1", Kind: "Pod", Name: "game-pod-0", UID: "uid-123"},
			},
		},
		Spec: ackv1alpha1.TargetGroupSpec{
			Protocol: ptr.To("TCP"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tg).Build()
	targetGroupARN := "arn:aws:elasticloadbalancing:us-east-1:111:targetgroup/tg/xyz"

	err := syncListenerAndTargetGroupBinding(ctx, fakeClient, tg, &targetGroupARN)
	if err != nil {
		t.Fatalf("syncListenerAndTargetGroupBinding failed: %v", err)
	}

	listener := &ackv1alpha1.Listener{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "game-pod-0-32001", Namespace: "default"}, listener)
	if err != nil {
		t.Fatalf("listener not created: %v", err)
	}
	if *listener.Spec.Port != 32001 {
		t.Errorf("listener port = %d, want 32001", *listener.Spec.Port)
	}
	if *listener.Spec.LoadBalancerARN != "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/abc" {
		t.Errorf("listener LB ARN mismatch")
	}
	if len(listener.Spec.DefaultActions) != 1 || *listener.Spec.DefaultActions[0].TargetGroupARN != targetGroupARN {
		t.Errorf("listener action TG ARN mismatch")
	}
	if listener.Labels[SvcSelectorKey] != "game-pod-0" {
		t.Errorf("listener pod-name label missing")
	}

	tgb := &elbv2api.TargetGroupBinding{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "game-pod-0-32001", Namespace: "default"}, tgb)
	if err != nil {
		t.Fatalf("TGB not created: %v", err)
	}
	if tgb.Spec.TargetGroupARN != targetGroupARN {
		t.Errorf("TGB ARN = %q, want %q", tgb.Spec.TargetGroupARN, targetGroupARN)
	}
	if tgb.Spec.ServiceRef.Name != "game-pod-0" {
		t.Errorf("TGB ServiceRef.Name = %q, want game-pod-0", tgb.Spec.ServiceRef.Name)
	}
}

func TestSyncListenerAndTargetGroupBinding_InvalidPort(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	tg := &ackv1alpha1.TargetGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "game-pod-bad",
			Namespace: "default",
			Annotations: map[string]string{
				NlbARNAnnoKey:  "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/nlb/abc",
				NlbPortAnnoKey: "not-a-number",
			},
			Labels: map[string]string{SvcSelectorKey: "game-pod-bad"},
		},
		Spec: ackv1alpha1.TargetGroupSpec{Protocol: ptr.To("TCP")},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tg).Build()
	arn := "arn:tg"
	err := syncListenerAndTargetGroupBinding(ctx, fakeClient, tg, &arn)
	if err == nil {
		t.Error("expected error for invalid port annotation, got nil")
	}
}
