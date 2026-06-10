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
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	gamekruiseiov1alpha1 "github.com/openkruise/kruise-game/apis/v1alpha1"
)

func TestParseLbConfigWithAGA(t *testing.T) {
	tests := []struct {
		name                    string
		conf                    []gamekruiseiov1alpha1.NetworkConfParams
		wantAGALabelSelector    string
		wantAGAClientIPPreserve bool
		wantAGANamespace        string
		wantLBARNs              []string
	}{
		{
			name: "no AGA config - backward compatible",
			conf: []gamekruiseiov1alpha1.NetworkConfParams{
				{Name: NlbARNsConfigName, Value: "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/a/1"},
				{Name: PortProtocolsConfigName, Value: "8080/TCP"},
			},
			wantAGALabelSelector:    "",
			wantAGAClientIPPreserve: false,
			wantAGANamespace:        "",
			wantLBARNs:              []string{"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/a/1"},
		},
		{
			name: "with AGA label selector",
			conf: []gamekruiseiov1alpha1.NetworkConfParams{
				{Name: NlbARNsConfigName, Value: "arn:aws:elasticloadbalancing:us-west-2:600413481647:loadbalancer/net/okg-aga-poc-nlb/858689fb0d176b5f"},
				{Name: PortProtocolsConfigName, Value: "8080/TCP"},
				{Name: AGALabelSelectorConfigName, Value: "game.kruise.io/aga-pool=poc-test"},
			},
			wantAGALabelSelector:    "game.kruise.io/aga-pool=poc-test",
			wantAGAClientIPPreserve: false,
			wantAGANamespace:        "",
			wantLBARNs:              []string{"arn:aws:elasticloadbalancing:us-west-2:600413481647:loadbalancer/net/okg-aga-poc-nlb/858689fb0d176b5f"},
		},
		{
			name: "with AGA full config",
			conf: []gamekruiseiov1alpha1.NetworkConfParams{
				{Name: NlbARNsConfigName, Value: "arn:aws:elasticloadbalancing:us-west-2:600413481647:loadbalancer/net/nlb/abc"},
				{Name: PortProtocolsConfigName, Value: "8080/TCP"},
				{Name: AGALabelSelectorConfigName, Value: "app=game,tier=prod"},
				{Name: AGAClientIPPreservationConfigName, Value: "true"},
				{Name: AGANamespaceConfigName, Value: "aga-namespace"},
			},
			wantAGALabelSelector:    "app=game,tier=prod",
			wantAGAClientIPPreserve: true,
			wantAGANamespace:        "aga-namespace",
			wantLBARNs:              []string{"arn:aws:elasticloadbalancing:us-west-2:600413481647:loadbalancer/net/nlb/abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := parseLbConfig(tt.conf)
			if cfg.agaLabelSelector != tt.wantAGALabelSelector {
				t.Errorf("agaLabelSelector = %q, want %q", cfg.agaLabelSelector, tt.wantAGALabelSelector)
			}
			if cfg.agaClientIPPreservation != tt.wantAGAClientIPPreserve {
				t.Errorf("agaClientIPPreservation = %v, want %v", cfg.agaClientIPPreservation, tt.wantAGAClientIPPreserve)
			}
			if cfg.agaNamespace != tt.wantAGANamespace {
				t.Errorf("agaNamespace = %q, want %q", cfg.agaNamespace, tt.wantAGANamespace)
			}
			if !reflect.DeepEqual(cfg.loadBalancerARNs, tt.wantLBARNs) {
				t.Errorf("loadBalancerARNs = %v, want %v", cfg.loadBalancerARNs, tt.wantLBARNs)
			}
		})
	}
}

func TestMatchesListener(t *testing.T) {
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:600413481647:loadbalancer/net/okg-aga-poc-nlb/858689fb0d176b5f"

	tests := []struct {
		name     string
		listener interface{}
		nlbARN   string
		port     int32
		want     bool
	}{
		{
			name: "exact match",
			listener: map[string]interface{}{
				"protocol": "TCP",
				"portRanges": []interface{}{
					map[string]interface{}{"fromPort": int64(32001), "toPort": int64(32001)},
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
			},
			nlbARN: nlbARN,
			port:   32001,
			want:   true,
		},
		{
			name: "different port",
			listener: map[string]interface{}{
				"protocol": "TCP",
				"portRanges": []interface{}{
					map[string]interface{}{"fromPort": int64(32002), "toPort": int64(32002)},
				},
				"endpointGroups": []interface{}{
					map[string]interface{}{
						"endpoints": []interface{}{
							map[string]interface{}{"endpointID": nlbARN},
						},
					},
				},
			},
			nlbARN: nlbARN,
			port:   32001,
			want:   false,
		},
		{
			name: "different NLB ARN",
			listener: map[string]interface{}{
				"portRanges": []interface{}{
					map[string]interface{}{"fromPort": int64(32001), "toPort": int64(32001)},
				},
				"endpointGroups": []interface{}{
					map[string]interface{}{
						"endpoints": []interface{}{
							map[string]interface{}{"endpointID": "arn:other"},
						},
					},
				},
			},
			nlbARN: nlbARN,
			port:   32001,
			want:   false,
		},
		{
			name:     "nil listener",
			listener: nil,
			nlbARN:   nlbARN,
			port:     32001,
			want:     false,
		},
		{
			name: "port range (not single port)",
			listener: map[string]interface{}{
				"portRanges": []interface{}{
					map[string]interface{}{"fromPort": int64(32001), "toPort": int64(32050)},
				},
				"endpointGroups": []interface{}{
					map[string]interface{}{
						"endpoints": []interface{}{
							map[string]interface{}{"endpointID": nlbARN},
						},
					},
				},
			},
			nlbARN: nlbARN,
			port:   32001,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesListener(tt.listener, tt.nlbARN, tt.port)
			if got != tt.want {
				t.Errorf("matchesListener() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAGAStaticIPs(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want []string
	}{
		{
			name: "deployed AGA with 2 IPs",
			obj: map[string]interface{}{
				"status": map[string]interface{}{
					"status": "DEPLOYED",
					"ipSets": []interface{}{
						map[string]interface{}{
							"ipAddresses": []interface{}{"166.117.44.192", "166.117.129.28"},
						},
					},
				},
			},
			want: []string{"166.117.44.192", "166.117.129.28"},
		},
		{
			name: "no status",
			obj:  map[string]interface{}{},
			want: nil,
		},
		{
			name: "empty ipSets",
			obj: map[string]interface{}{
				"status": map[string]interface{}{
					"status": "IN_PROGRESS",
					"ipSets": []interface{}{},
				},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &unstructured.Unstructured{Object: tt.obj}
			got := getAGAStaticIPs(cr)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getAGAStaticIPs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAGAStatus(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want string
	}{
		{
			name: "deployed",
			obj: map[string]interface{}{
				"status": map[string]interface{}{"status": "DEPLOYED"},
			},
			want: "DEPLOYED",
		},
		{
			name: "in progress",
			obj: map[string]interface{}{
				"status": map[string]interface{}{"status": "IN_PROGRESS"},
			},
			want: "IN_PROGRESS",
		},
		{
			name: "no status",
			obj:  map[string]interface{}{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &unstructured.Unstructured{Object: tt.obj}
			got := getAGAStatus(cr)
			if got != tt.want {
				t.Errorf("getAGAStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindListenerIndex(t *testing.T) {
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:600413481647:loadbalancer/net/nlb/abc"

	agaCR := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"listeners": []interface{}{
					map[string]interface{}{
						"protocol": "TCP",
						"portRanges": []interface{}{
							map[string]interface{}{"fromPort": int64(32001), "toPort": int64(32050)},
						},
						"endpointGroups": []interface{}{
							map[string]interface{}{
								"endpoints": []interface{}{
									map[string]interface{}{"endpointID": nlbARN},
								},
							},
						},
					},
					map[string]interface{}{
						"protocol": "TCP",
						"portRanges": []interface{}{
							map[string]interface{}{"fromPort": int64(32001), "toPort": int64(32001)},
						},
						"endpointGroups": []interface{}{
							map[string]interface{}{
								"endpoints": []interface{}{
									map[string]interface{}{"endpointID": nlbARN},
								},
							},
						},
					},
					map[string]interface{}{
						"protocol": "TCP",
						"portRanges": []interface{}{
							map[string]interface{}{"fromPort": int64(32002), "toPort": int64(32002)},
						},
						"endpointGroups": []interface{}{
							map[string]interface{}{
								"endpoints": []interface{}{
									map[string]interface{}{"endpointID": nlbARN},
								},
							},
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name    string
		nlbARN  string
		port    int32
		wantIdx int
	}{
		{"find per-port listener at index 1", nlbARN, 32001, 1},
		{"find per-port listener at index 2", nlbARN, 32002, 2},
		{"not found - port not in CR", nlbARN, 32099, -1},
		{"not found - different ARN", "arn:other", 32001, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findListenerIndex(agaCR, tt.nlbARN, tt.port)
			if got != tt.wantIdx {
				t.Errorf("findListenerIndex() = %d, want %d", got, tt.wantIdx)
			}
		})
	}
}

func TestAGAManagerAssignments(t *testing.T) {
	mgr := &AGAManager{assignments: make(map[string]*agaAssignment)}

	podKey := "ns/pod-0"
	a1 := &agaAssignment{agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn:nlb", port: 32001}
	a2 := &agaAssignment{agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn:nlb", port: 32002}

	mgr.setAssignment("ns/pod-0:32001", a1)
	mgr.setAssignment("ns/pod-0:32002", a2)

	if mgr.getAssignment("ns/pod-0:32001") != a1 {
		t.Error("expected assignment a1")
	}

	assignments := mgr.getAssignmentsForPod(podKey)
	if len(assignments) != 2 {
		t.Errorf("expected 2 assignments, got %d", len(assignments))
	}

	mgr.removeAssignment("ns/pod-0:32001")
	if mgr.getAssignment("ns/pod-0:32001") != nil {
		t.Error("expected nil after remove")
	}
	assignments = mgr.getAssignmentsForPod(podKey)
	if len(assignments) != 1 {
		t.Errorf("expected 1 assignment after remove, got %d", len(assignments))
	}
}

func TestListenerCoversPort(t *testing.T) {
	nlbARN := "arn:aws:elasticloadbalancing:us-west-2:600413481647:loadbalancer/net/nlb/abc"

	agaCR := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"listeners": []interface{}{
					map[string]interface{}{
						"protocol": "TCP",
						"portRanges": []interface{}{
							map[string]interface{}{"fromPort": int64(32001), "toPort": int64(32050)},
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
					},
				},
			},
		},
	}

	tests := []struct {
		name   string
		nlbARN string
		port   int32
		want   bool
	}{
		{"port in range", nlbARN, 32001, true},
		{"port at end of range", nlbARN, 32050, true},
		{"port in middle", nlbARN, 32025, true},
		{"port below range", nlbARN, 32000, false},
		{"port above range", nlbARN, 32051, false},
		{"different NLB ARN", "arn:other", 32001, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listenerCoversPort(agaCR, tt.nlbARN, tt.port)
			if got != tt.want {
				t.Errorf("listenerCoversPort() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		input interface{}
		want  int64
	}{
		{int64(42), 42},
		{float64(32001), 32001},
		{int(100), 100},
		{"string", 0},
		{nil, 0},
	}
	for _, tt := range tests {
		got := toInt64(tt.input)
		if got != tt.want {
			t.Errorf("toInt64(%v) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestToUnstructured(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{"kind": "GlobalAccelerator"}}

	got, ok := toUnstructured(u)
	if !ok || got != u {
		t.Error("toUnstructured should return the object directly")
	}

	_, ok = toUnstructured("not an object")
	if ok {
		t.Error("toUnstructured should return false for non-unstructured objects")
	}

	_, ok = toUnstructured(nil)
	if ok {
		t.Error("toUnstructured should return false for nil")
	}
}

func TestGetAssignmentsForAGA(t *testing.T) {
	mgr := &AGAManager{assignments: make(map[string]*agaAssignment)}

	mgr.setAssignment("ns/pod-0:32001", &agaAssignment{agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn:nlb", port: 32001})
	mgr.setAssignment("ns/pod-0:32002", &agaAssignment{agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn:nlb", port: 32002})
	mgr.setAssignment("ns/pod-1:32003", &agaAssignment{agaName: "aga-2", agaNamespace: "ns", nlbARN: "arn:nlb", port: 32003})
	mgr.setAssignment("ns/pod-2:32004", &agaAssignment{agaName: "aga-1", agaNamespace: "ns", nlbARN: "arn:nlb", port: 32004})

	result := mgr.getAssignmentsForAGA("aga-1", "ns")
	if len(result) != 2 {
		t.Errorf("expected 2 pod keys for aga-1, got %d", len(result))
	}
	if _, ok := result["ns/pod-0"]; !ok {
		t.Error("expected ns/pod-0 in result")
	}
	if _, ok := result["ns/pod-2"]; !ok {
		t.Error("expected ns/pod-2 in result")
	}

	result2 := mgr.getAssignmentsForAGA("aga-2", "ns")
	if len(result2) != 1 {
		t.Errorf("expected 1 pod key for aga-2, got %d", len(result2))
	}

	result3 := mgr.getAssignmentsForAGA("aga-nonexist", "ns")
	if len(result3) != 0 {
		t.Errorf("expected 0 pod keys for nonexist, got %d", len(result3))
	}
}

func TestHandleAGAStatusChangeDetection(t *testing.T) {
	oldCR := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "aga-1", "namespace": "ns"},
		"status": map[string]interface{}{
			"status": "IN_PROGRESS",
			"ipSets": []interface{}{
				map[string]interface{}{
					"ipAddresses": []interface{}{"1.2.3.4", "5.6.7.8"},
				},
			},
		},
	}}

	newCRDeployed := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "aga-1", "namespace": "ns"},
		"status": map[string]interface{}{
			"status": "DEPLOYED",
			"ipSets": []interface{}{
				map[string]interface{}{
					"ipAddresses": []interface{}{"1.2.3.4", "5.6.7.8"},
				},
			},
		},
	}}

	oldStatus := getAGAStatus(oldCR)
	newStatus := getAGAStatus(newCRDeployed)
	newIPs := getAGAStaticIPs(newCRDeployed)

	shouldTrigger := oldStatus != "DEPLOYED" && newStatus == "DEPLOYED" && len(newIPs) > 0
	if !shouldTrigger {
		t.Error("transition IN_PROGRESS->DEPLOYED should trigger update")
	}

	oldCRAlreadyDeployed := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"status": "DEPLOYED",
			"ipSets": []interface{}{
				map[string]interface{}{
					"ipAddresses": []interface{}{"1.2.3.4", "5.6.7.8"},
				},
			},
		},
	}}
	oldStatus2 := getAGAStatus(oldCRAlreadyDeployed)
	shouldNotTrigger := oldStatus2 != "DEPLOYED" && newStatus == "DEPLOYED"
	if shouldNotTrigger {
		t.Error("already-deployed to deployed should NOT trigger initial transition")
	}

	noIPCR := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"status": "DEPLOYED"},
	}}
	noIPs := getAGAStaticIPs(noIPCR)
	shouldSkip := getAGAStatus(noIPCR) != "DEPLOYED" || len(noIPs) == 0
	if !shouldSkip {
		t.Error("DEPLOYED but no IPs should be skipped")
	}
}

func TestParseNetworkStatusPorts(t *testing.T) {
	statusJSON := `{"externalAddresses":[{"ip":"1.2.3.4","ports":[{"name":"8080","port":"32001","protocol":"TCP"}],"endPoint":"nlb.elb.us-west-2.amazonaws.com"}],"currentNetworkState":"Ready"}`

	ports := parseNetworkStatusPorts(statusJSON)
	if len(ports) != 1 || ports[0] != 32001 {
		t.Errorf("parseNetworkStatusPorts() = %v, want [32001]", ports)
	}

	empty := parseNetworkStatusPorts("")
	if len(empty) != 0 {
		t.Errorf("parseNetworkStatusPorts empty = %v, want nil", empty)
	}
}
