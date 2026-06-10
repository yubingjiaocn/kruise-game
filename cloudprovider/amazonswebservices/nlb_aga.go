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
	"fmt"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	log "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gamekruiseiov1alpha1 "github.com/openkruise/kruise-game/apis/v1alpha1"
)

const (
	AGALabelSelectorConfigName        = "AGALabelSelector"
	AGAClientIPPreservationConfigName = "AGAClientIPPreservation"
	AGANamespaceConfigName            = "AGANamespace"

	agaGroup    = "aga.k8s.aws"
	agaVersion  = "v1beta1"
	agaResource = "globalaccelerators"
	agaKind     = "GlobalAccelerator"
)

var agaGVK = schema.GroupVersionKind{Group: agaGroup, Version: agaVersion, Kind: agaKind}

type agaAssignment struct {
	agaName      string
	agaNamespace string
	nlbARN       string
	port         int32
}

type AGAManager struct {
	mu          sync.RWMutex
	assignments map[string]*agaAssignment // "ns/podName:port" -> AGA assignment
}

var globalAGAManager = &AGAManager{
	assignments: make(map[string]*agaAssignment),
}

func (m *AGAManager) getAssignment(key string) *agaAssignment {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.assignments[key]
}

func (m *AGAManager) setAssignment(key string, a *agaAssignment) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignments[key] = a
}

func (m *AGAManager) removeAssignment(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.assignments, key)
}

func (m *AGAManager) getAssignmentsForPod(podKey string) []*agaAssignment {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*agaAssignment
	prefix := podKey + ":"
	for k, v := range m.assignments {
		if strings.HasPrefix(k, prefix) {
			result = append(result, v)
		}
	}
	return result
}

func appendAGAListener(ctx context.Context, c client.Client, agaCR *unstructured.Unstructured,
	nlbARN string, port int32, clientIPPreservation bool, podKey string) error {

	if listenerExists(agaCR, nlbARN, port) {
		log.Infof("[AGA] listener already exists for port %d on AGA %s/%s, skipping",
			port, agaCR.GetNamespace(), agaCR.GetName())
		return nil
	}

	listenerValue := map[string]interface{}{
		"protocol": "TCP",
		"portRanges": []interface{}{
			map[string]interface{}{"fromPort": int64(port), "toPort": int64(port)},
		},
		"clientAffinity": "SOURCE_IP",
		"endpointGroups": []interface{}{
			map[string]interface{}{
				"endpoints": []interface{}{
					map[string]interface{}{
						"type":                        "EndpointID",
						"endpointID":                  nlbARN,
						"weight":                      int64(128),
						"clientIPPreservationEnabled": clientIPPreservation,
					},
				},
			},
		},
	}

	patch := []map[string]interface{}{
		{
			"op":    "add",
			"path":  "/spec/listeners/-",
			"value": listenerValue,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal JSON Patch: %w", err)
	}

	log.Infof("[AGA] appending listener (port %d, NLB %s) to AGA %s/%s for pod %s",
		port, nlbARN, agaCR.GetNamespace(), agaCR.GetName(), podKey)

	return c.Patch(ctx, agaCR, client.RawPatch(types.JSONPatchType, patchBytes))
}

func removeAGAListener(ctx context.Context, c client.Client, agaNamespace, agaName string,
	nlbARN string, port int32) error {

	agaCR := &unstructured.Unstructured{}
	agaCR.SetGroupVersionKind(agaGVK)
	err := c.Get(ctx, types.NamespacedName{Namespace: agaNamespace, Name: agaName}, agaCR)
	if err != nil {
		return fmt.Errorf("get AGA CR %s/%s: %w", agaNamespace, agaName, err)
	}

	idx := findListenerIndex(agaCR, nlbARN, port)
	if idx < 0 {
		log.Infof("[AGA] listener for port %d not found on AGA %s/%s, nothing to remove",
			port, agaNamespace, agaName)
		return nil
	}

	patch := []map[string]interface{}{
		{
			"op":   "remove",
			"path": fmt.Sprintf("/spec/listeners/%d", idx),
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal JSON Patch: %w", err)
	}

	log.Infof("[AGA] removing listener at index %d (port %d) from AGA %s/%s",
		idx, port, agaNamespace, agaName)

	return c.Patch(ctx, agaCR, client.RawPatch(types.JSONPatchType, patchBytes))
}

func listenerExists(agaCR *unstructured.Unstructured, nlbARN string, port int32) bool {
	return findListenerIndex(agaCR, nlbARN, port) >= 0
}

func listenerCoversPort(agaCR *unstructured.Unstructured, nlbARN string, port int32) bool {
	listeners := getListeners(agaCR)
	for _, l := range listeners {
		lMap, ok := l.(map[string]interface{})
		if !ok {
			continue
		}
		portRanges, ok := lMap["portRanges"].([]interface{})
		if !ok || len(portRanges) == 0 {
			continue
		}
		pr, ok := portRanges[0].(map[string]interface{})
		if !ok {
			continue
		}
		fromPort := toInt64(pr["fromPort"])
		toPort := toInt64(pr["toPort"])
		if int64(port) < fromPort || int64(port) > toPort {
			continue
		}
		endpointGroups, ok := lMap["endpointGroups"].([]interface{})
		if !ok || len(endpointGroups) == 0 {
			continue
		}
		eg, ok := endpointGroups[0].(map[string]interface{})
		if !ok {
			continue
		}
		endpoints, ok := eg["endpoints"].([]interface{})
		if !ok || len(endpoints) == 0 {
			continue
		}
		for _, ep := range endpoints {
			epMap, ok := ep.(map[string]interface{})
			if !ok {
				continue
			}
			if epID, _ := epMap["endpointID"].(string); epID == nlbARN {
				return true
			}
		}
	}
	return false
}

func findListenerIndex(agaCR *unstructured.Unstructured, nlbARN string, port int32) int {
	listeners := getListeners(agaCR)
	for i, l := range listeners {
		if matchesListener(l, nlbARN, port) {
			return i
		}
	}
	return -1
}

func matchesListener(listener interface{}, nlbARN string, port int32) bool {
	lMap, ok := listener.(map[string]interface{})
	if !ok {
		return false
	}
	portRanges, ok := lMap["portRanges"].([]interface{})
	if !ok || len(portRanges) == 0 {
		return false
	}
	pr, ok := portRanges[0].(map[string]interface{})
	if !ok {
		return false
	}
	fromPort := toInt64(pr["fromPort"])
	toPort := toInt64(pr["toPort"])
	if fromPort != int64(port) || toPort != int64(port) {
		return false
	}

	endpointGroups, ok := lMap["endpointGroups"].([]interface{})
	if !ok || len(endpointGroups) == 0 {
		return false
	}
	eg, ok := endpointGroups[0].(map[string]interface{})
	if !ok {
		return false
	}
	endpoints, ok := eg["endpoints"].([]interface{})
	if !ok || len(endpoints) == 0 {
		return false
	}
	ep, ok := endpoints[0].(map[string]interface{})
	if !ok {
		return false
	}
	endpointID, _ := ep["endpointID"].(string)
	return endpointID == nlbARN
}

func getListeners(agaCR *unstructured.Unstructured) []interface{} {
	spec, ok := agaCR.Object["spec"].(map[string]interface{})
	if !ok {
		return nil
	}
	listeners, ok := spec["listeners"].([]interface{})
	if !ok {
		return nil
	}
	return listeners
}

func selectAGA(ctx context.Context, c client.Client, selectorStr string, namespace string) (*unstructured.Unstructured, error) {
	selector, err := labels.Parse(selectorStr)
	if err != nil {
		return nil, fmt.Errorf("parse AGA label selector %q: %w", selectorStr, err)
	}

	agaList := &unstructured.UnstructuredList{}
	agaList.SetGroupVersionKind(agaGVK)
	err = c.List(ctx, agaList, &client.ListOptions{
		Namespace:     namespace,
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("list AGA CRs: %w", err)
	}

	if len(agaList.Items) == 0 {
		return nil, fmt.Errorf("no GlobalAccelerator CR matching selector %q in namespace %s", selectorStr, namespace)
	}

	var best *unstructured.Unstructured
	bestCount := int(^uint(0) >> 1)
	for i := range agaList.Items {
		item := &agaList.Items[i]
		listeners := getListeners(item)
		if len(listeners) < bestCount {
			bestCount = len(listeners)
			best = item
		}
	}
	return best, nil
}

func getAGAStaticIPs(agaCR *unstructured.Unstructured) []string {
	status, ok := agaCR.Object["status"].(map[string]interface{})
	if !ok {
		return nil
	}
	ipSets, ok := status["ipSets"].([]interface{})
	if !ok {
		return nil
	}
	var ips []string
	for _, ipSet := range ipSets {
		ipSetMap, ok := ipSet.(map[string]interface{})
		if !ok {
			continue
		}
		addrs, ok := ipSetMap["ipAddresses"].([]interface{})
		if !ok {
			continue
		}
		for _, addr := range addrs {
			if s, ok := addr.(string); ok {
				ips = append(ips, s)
			}
		}
	}
	return ips
}

func getAGAStatus(agaCR *unstructured.Unstructured) string {
	status, ok := agaCR.Object["status"].(map[string]interface{})
	if !ok {
		return ""
	}
	s, _ := status["status"].(string)
	return s
}

// reconcileAGAForPod finds or creates AGA listeners for allocated NLB ports. Called from OnPodUpdated.
// If the AGA already has a wide-range listener covering the port (pre-provisioned model), it just
// records the assignment without appending. Otherwise it appends a per-port listener.
func reconcileAGAForPod(ctx context.Context, c client.Client, config *nlbConfig,
	nlbARN string, ports []int32, podKey string) error {

	if config.agaLabelSelector == "" {
		return nil
	}

	agaNamespace := config.agaNamespace
	if agaNamespace == "" {
		ns := strings.SplitN(podKey, "/", 2)
		if len(ns) == 2 {
			agaNamespace = ns[0]
		}
	}

	for _, port := range ports {
		assignKey := fmt.Sprintf("%s:%d", podKey, port)
		if globalAGAManager.getAssignment(assignKey) != nil {
			continue
		}

		agaCR, err := selectAGA(ctx, c, config.agaLabelSelector, agaNamespace)
		if err != nil {
			return fmt.Errorf("select AGA for pod %s: %w", podKey, err)
		}

		if listenerCoversPort(agaCR, nlbARN, port) {
			log.Infof("[AGA] AGA %s/%s already covers port %d for NLB %s, recording assignment",
				agaCR.GetNamespace(), agaCR.GetName(), port, nlbARN)
			globalAGAManager.setAssignment(assignKey, &agaAssignment{
				agaName:      agaCR.GetName(),
				agaNamespace: agaCR.GetNamespace(),
				nlbARN:       nlbARN,
				port:         port,
			})
			continue
		}

		err = appendAGAListener(ctx, c, agaCR, nlbARN, port, config.agaClientIPPreservation, podKey)
		if err != nil {
			return fmt.Errorf("append AGA listener for pod %s port %d: %w", podKey, port, err)
		}

		globalAGAManager.setAssignment(assignKey, &agaAssignment{
			agaName:      agaCR.GetName(),
			agaNamespace: agaCR.GetNamespace(),
			nlbARN:       nlbARN,
			port:         port,
		})
	}
	return nil
}

// cleanupAGAForPod removes per-port AGA listeners created by OKG for this pod.
// Wide-range (pre-provisioned) listeners are not removed — they are shared resources.
func cleanupAGAForPod(ctx context.Context, c client.Client, config *nlbConfig, podKey string) {
	if config.agaLabelSelector == "" {
		return
	}

	assignments := globalAGAManager.getAssignmentsForPod(podKey)
	for _, a := range assignments {
		agaCR := &unstructured.Unstructured{}
		agaCR.SetGroupVersionKind(agaGVK)
		err := c.Get(ctx, types.NamespacedName{Namespace: a.agaNamespace, Name: a.agaName}, agaCR)
		if err == nil {
			idx := findListenerIndex(agaCR, a.nlbARN, a.port)
			if idx >= 0 {
				err = removeAGAListener(ctx, c, a.agaNamespace, a.agaName, a.nlbARN, a.port)
				if err != nil {
					log.Warningf("[AGA] cleanup failed for pod %s, AGA %s/%s port %d: %v",
						podKey, a.agaNamespace, a.agaName, a.port, err)
				}
			}
		}
		assignKey := fmt.Sprintf("%s:%d", podKey, a.port)
		globalAGAManager.removeAssignment(assignKey)
	}
}

// getAGAIPsForPod returns AGA static IPs if AGA is DEPLOYED. Called during network-status construction.
func getAGAIPsForPod(ctx context.Context, c client.Client, config *nlbConfig, podKey string) []string {
	if config.agaLabelSelector == "" {
		return nil
	}

	assignments := globalAGAManager.getAssignmentsForPod(podKey)
	if len(assignments) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var allIPs []string
	for _, a := range assignments {
		agaCR := &unstructured.Unstructured{}
		agaCR.SetGroupVersionKind(agaGVK)
		err := c.Get(ctx, types.NamespacedName{Namespace: a.agaNamespace, Name: a.agaName}, agaCR)
		if err != nil {
			continue
		}
		if getAGAStatus(agaCR) != "DEPLOYED" {
			continue
		}
		for _, ip := range getAGAStaticIPs(agaCR) {
			if !seen[ip] {
				allIPs = append(allIPs, ip)
				seen[ip] = true
			}
		}
	}
	return allIPs
}

func toInt64(v interface{}) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case float64:
		return int64(val)
	case int:
		return int64(val)
	default:
		return 0
	}
}

// --- Task 1: AGA status watch (informer-driven reverse trigger) ---

func startWatchAGA(ctx context.Context) error {
	var err error
	go func() {
		err = watchAGA(ctx)
	}()
	return err
}

func watchAGA(ctx context.Context) error {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		log.Errorf("[AGA] failed to create manager for AGA watcher: %v", err)
		return err
	}

	agaObj := &unstructured.Unstructured{}
	agaObj.SetGroupVersionKind(agaGVK)
	informer, err := mgr.GetCache().GetInformer(ctx, agaObj)
	if err != nil {
		log.Errorf("[AGA] failed to get AGA informer: %v", err)
		return fmt.Errorf("failed to get AGA informer: %v", err)
	}

	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handleAGAAdd(ctx, mgr.GetClient(), obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			handleAGAStatusChange(ctx, mgr.GetClient(), oldObj, newObj)
		},
	}); err != nil {
		log.Errorf("[AGA] failed to add AGA event handler: %v", err)
		return fmt.Errorf("failed to add AGA event handler: %v", err)
	}

	log.Info("[AGA] Start to watch GlobalAccelerator CRs successfully")
	return mgr.Start(ctx)
}

func handleAGAStatusChange(ctx context.Context, c client.Client, oldObj, newObj interface{}) {
	oldCR, ok := toUnstructured(oldObj)
	if !ok {
		return
	}
	newCR, ok := toUnstructured(newObj)
	if !ok {
		return
	}

	newStatus := getAGAStatus(newCR)
	newIPs := getAGAStaticIPs(newCR)
	if newStatus != "DEPLOYED" || len(newIPs) == 0 {
		return
	}

	oldStatus := getAGAStatus(oldCR)
	agaName := newCR.GetName()
	agaNamespace := newCR.GetNamespace()

	if oldStatus != "DEPLOYED" {
		log.Infof("[AGA] AGA %s/%s transitioned to DEPLOYED with IPs %v, updating associated pods",
			agaNamespace, agaName, newIPs)
		updatePodsWithAGAIPs(ctx, c, agaName, agaNamespace, newIPs)
		return
	}

	oldIPs := getAGAStaticIPs(oldCR)
	if strings.Join(oldIPs, ",") != strings.Join(newIPs, ",") {
		log.Infof("[AGA] AGA %s/%s IPs changed from %v to %v, updating associated pods",
			agaNamespace, agaName, oldIPs, newIPs)
		updatePodsWithAGAIPs(ctx, c, agaName, agaNamespace, newIPs)
	}
}

func handleAGAAdd(ctx context.Context, c client.Client, obj interface{}) {
	cr, ok := toUnstructured(obj)
	if !ok {
		return
	}
	if getAGAStatus(cr) != "DEPLOYED" {
		return
	}
	ips := getAGAStaticIPs(cr)
	if len(ips) == 0 {
		return
	}

	agaName := cr.GetName()
	agaNamespace := cr.GetNamespace()

	globalAGAManager.mu.RLock()
	hasAssignment := false
	for _, a := range globalAGAManager.assignments {
		if a.agaName == agaName && a.agaNamespace == agaNamespace {
			hasAssignment = true
			break
		}
	}
	globalAGAManager.mu.RUnlock()

	if !hasAssignment {
		return
	}

	log.Infof("[AGA] initial sync: AGA %s/%s is DEPLOYED with IPs %v, checking pods need update",
		agaNamespace, agaName, ips)
	updatePodsWithAGAIPs(ctx, c, agaName, agaNamespace, ips)
}

func updatePodsWithAGAIPs(ctx context.Context, c client.Client, agaName, agaNamespace string, agaIPs []string) {
	globalAGAManager.mu.RLock()
	podKeys := make(map[string]bool)
	for key, a := range globalAGAManager.assignments {
		if a.agaName == agaName && a.agaNamespace == agaNamespace {
			podKey := key[:strings.LastIndex(key, ":")]
			podKeys[podKey] = true
		}
	}
	globalAGAManager.mu.RUnlock()

	ipStr := strings.Join(agaIPs, ",")

	for podKey := range podKeys {
		parts := strings.SplitN(podKey, "/", 2)
		if len(parts) != 2 {
			continue
		}
		ns, name := parts[0], parts[1]

		pod := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, pod); err != nil {
			log.Warningf("[AGA] failed to get pod %s for AGA IP update: %v", podKey, err)
			continue
		}

		statusAnno := pod.Annotations[gamekruiseiov1alpha1.GameServerNetworkStatus]
		if statusAnno == "" {
			continue
		}

		var networkStatus gamekruiseiov1alpha1.NetworkStatus
		if err := json.Unmarshal([]byte(statusAnno), &networkStatus); err != nil {
			log.Warningf("[AGA] failed to unmarshal network-status for pod %s: %v", podKey, err)
			continue
		}

		if networkStatus.CurrentNetworkState != gamekruiseiov1alpha1.NetworkReady {
			continue
		}

		changed := false
		for i := range networkStatus.ExternalAddresses {
			if networkStatus.ExternalAddresses[i].IP != ipStr {
				networkStatus.ExternalAddresses[i].IP = ipStr
				changed = true
			}
		}
		if !changed {
			continue
		}

		newStatusBytes, err := json.Marshal(networkStatus)
		if err != nil {
			continue
		}

		patch := client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(
			`{"metadata":{"annotations":{%q:%q}}}`,
			gamekruiseiov1alpha1.GameServerNetworkStatus, string(newStatusBytes))))
		if err := c.Patch(ctx, pod, patch); err != nil {
			log.Warningf("[AGA] failed to patch pod %s with AGA IPs: %v", podKey, err)
		} else {
			log.Infof("[AGA] updated pod %s network-status with AGA IPs %s", podKey, ipStr)
		}
	}
}

func toUnstructured(obj interface{}) (*unstructured.Unstructured, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	if ok {
		return u, true
	}
	raw, ok := obj.(cache.DeletedFinalStateUnknown)
	if ok {
		u, ok = raw.Obj.(*unstructured.Unstructured)
		return u, ok
	}
	return nil, false
}

// --- Task 2: Cache rebuild on controller restart ---

func initAGACache(ctx context.Context, c client.Client) {
	podList := &corev1.PodList{}
	err := c.List(ctx, podList, client.MatchingLabels{ResourceTagKey: ResourceTagValue})
	if err != nil {
		log.Warningf("[AGA] failed to list pods for AGA cache rebuild: %v", err)
		return
	}

	rebuilt := 0
	for i := range podList.Items {
		pod := &podList.Items[i]
		confAnno := pod.Annotations[gamekruiseiov1alpha1.GameServerNetworkConf]
		if confAnno == "" {
			continue
		}

		var conf []gamekruiseiov1alpha1.NetworkConfParams
		if err := json.Unmarshal([]byte(confAnno), &conf); err != nil {
			continue
		}

		lbConfig := parseLbConfig(conf)
		if lbConfig.agaLabelSelector == "" {
			continue
		}

		statusAnno := pod.Annotations[gamekruiseiov1alpha1.GameServerNetworkStatus]
		if statusAnno == "" {
			continue
		}

		var networkStatus gamekruiseiov1alpha1.NetworkStatus
		if err := json.Unmarshal([]byte(statusAnno), &networkStatus); err != nil {
			continue
		}

		if networkStatus.CurrentNetworkState != gamekruiseiov1alpha1.NetworkReady {
			continue
		}

		podKey := pod.GetNamespace() + "/" + pod.GetName()
		agaNamespace := lbConfig.agaNamespace
		if agaNamespace == "" {
			agaNamespace = pod.GetNamespace()
		}

		svc := &corev1.Service{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: pod.GetNamespace(), Name: pod.GetName()}, svc); err != nil {
			continue
		}
		nlbARN := svc.Annotations[NlbARNAnnoKey]
		if nlbARN == "" {
			continue
		}

		agaCR, err := selectAGA(ctx, c, lbConfig.agaLabelSelector, agaNamespace)
		if err != nil {
			continue
		}

		for _, port := range svc.Spec.Ports {
			assignKey := fmt.Sprintf("%s:%d", podKey, port.Port)
			if globalAGAManager.getAssignment(assignKey) != nil {
				continue
			}
			if listenerCoversPort(agaCR, nlbARN, port.Port) {
				globalAGAManager.setAssignment(assignKey, &agaAssignment{
					agaName:      agaCR.GetName(),
					agaNamespace: agaCR.GetNamespace(),
					nlbARN:       nlbARN,
					port:         port.Port,
				})
				rebuilt++
			}
		}
	}

	if rebuilt > 0 {
		log.Infof("[AGA] rebuilt %d AGA assignments from existing pod state", rebuilt)
	}
}

// getAssignmentsForAGA returns all pod keys that reference a specific AGA.
func (m *AGAManager) getAssignmentsForAGA(agaName, agaNamespace string) map[string][]*agaAssignment {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string][]*agaAssignment)
	for key, a := range m.assignments {
		if a.agaName == agaName && a.agaNamespace == agaNamespace {
			podKey := key[:strings.LastIndex(key, ":")]
			result[podKey] = append(result[podKey], a)
		}
	}
	return result
}

// rebuildAGACacheFromServices uses the service list to rebuild cache (alternative approach
// used when pods don't have the label). Operates on services that have the managed-by label.
func rebuildAGACacheFromServices(ctx context.Context, c client.Client, svcList []corev1.Service) {
	rebuilt := 0
	for i := range svcList {
		svc := &svcList[i]
		nlbARN := svc.Annotations[NlbARNAnnoKey]
		if nlbARN == "" {
			continue
		}

		podName := svc.Labels[SvcSelectorKey]
		if podName == "" {
			continue
		}
		podKey := svc.GetNamespace() + "/" + podName

		pod := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: svc.GetNamespace(), Name: podName}, pod); err != nil {
			continue
		}

		confAnno := pod.Annotations[gamekruiseiov1alpha1.GameServerNetworkConf]
		if confAnno == "" {
			continue
		}

		var conf []gamekruiseiov1alpha1.NetworkConfParams
		if err := json.Unmarshal([]byte(confAnno), &conf); err != nil {
			continue
		}

		lbConfig := parseLbConfig(conf)
		if lbConfig.agaLabelSelector == "" {
			continue
		}

		agaNamespace := lbConfig.agaNamespace
		if agaNamespace == "" {
			agaNamespace = svc.GetNamespace()
		}

		agaCR, err := selectAGA(ctx, c, lbConfig.agaLabelSelector, agaNamespace)
		if err != nil {
			continue
		}

		for _, port := range svc.Spec.Ports {
			assignKey := fmt.Sprintf("%s:%d", podKey, port.Port)
			if globalAGAManager.getAssignment(assignKey) != nil {
				continue
			}
			if listenerCoversPort(agaCR, nlbARN, port.Port) {
				globalAGAManager.setAssignment(assignKey, &agaAssignment{
					agaName:      agaCR.GetName(),
					agaNamespace: agaCR.GetNamespace(),
					nlbARN:       nlbARN,
					port:         port.Port,
				})
				rebuilt++
			}
		}
	}

	if rebuilt > 0 {
		log.Infof("[AGA] rebuilt %d AGA assignments from services", rebuilt)
		reconcileAGAIPsOnStartup(ctx, c)
	}
}

func reconcileAGAIPsOnStartup(ctx context.Context, c client.Client) {
	seen := make(map[string]bool)
	globalAGAManager.mu.RLock()
	for _, a := range globalAGAManager.assignments {
		key := a.agaNamespace + "/" + a.agaName
		seen[key] = true
	}
	globalAGAManager.mu.RUnlock()

	for key := range seen {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		agaNamespace, agaName := parts[0], parts[1]

		agaCR := &unstructured.Unstructured{}
		agaCR.SetGroupVersionKind(agaGVK)
		if err := c.Get(ctx, types.NamespacedName{Namespace: agaNamespace, Name: agaName}, agaCR); err != nil {
			continue
		}
		if getAGAStatus(agaCR) != "DEPLOYED" {
			continue
		}
		ips := getAGAStaticIPs(agaCR)
		if len(ips) == 0 {
			continue
		}
		updatePodsWithAGAIPs(ctx, c, agaName, agaNamespace, ips)
	}
}

// parseNetworkStatusPorts extracts external port numbers from a network-status annotation.
func parseNetworkStatusPorts(statusAnno string) []int32 {
	var ns gamekruiseiov1alpha1.NetworkStatus
	if err := json.Unmarshal([]byte(statusAnno), &ns); err != nil {
		return nil
	}
	var ports []int32
	for _, addr := range ns.ExternalAddresses {
		for _, p := range addr.Ports {
			if p.Port != nil {
				v, err := strconv.ParseInt(p.Port.String(), 10, 32)
				if err == nil {
					ports = append(ports, int32(v))
				}
			}
		}
	}
	return ports
}
