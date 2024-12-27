/*
Copyright © 2020 Red Hat Inc.

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
package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	compliancev1alpha1 "github.com/ComplianceAsCode/compliance-operator/pkg/apis/compliance/v1alpha1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"k8s.io/apimachinery/pkg/types"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	cmpv1alpha1 "github.com/ComplianceAsCode/compliance-operator/pkg/apis/compliance/v1alpha1"
)

const (
	/*#nodeRolePrefix         = "node-role.kubernetes.io/"*/
	nodeRolePrefix         = ""
	generatedKubelet       = "generated-kubelet"
	generatedKubeletSuffix = "kubelet"
	mcPayloadPrefix        = `data:text/plain,`
	mcBase64PayloadPrefix  = `data:text/plain;charset=utf-8;base64,`
)

var nodeSizingEnvList = [2]string{"autoSizingReserved", "systemReserved"}

func GetFirstNodeRoleLabel(nodeSelector map[string]string) string {
	if nodeSelector == nil {
		return ""
	}

	// FIXME: should we protect against multiple labels and return
	// an empty string if there are multiple?
	for k := range nodeSelector {
		if strings.HasPrefix(k, nodeRolePrefix) {
			return k
		}
	}

	return ""
}

func GetFirstNodeRole(nodeSelector map[string]string) string {
	if nodeSelector == nil {
		return ""
	}

	// FIXME: should we protect against multiple labels and return
	// an empty string if there are multiple?
	for k := range nodeSelector {
		if strings.HasPrefix(k, nodeRolePrefix) {
			return strings.TrimPrefix(k, nodeRolePrefix)
		}
	}

	return ""
}

func GetScanNameFromProfile(profileName string, nodeSelector map[string]string) string {
	role := GetFirstNodeRole(nodeSelector)
	if role == "" {
		return profileName
	}

	return fmt.Sprintf("%s-%s", profileName, role)
}

func GetNodeRoles(nodeSelector map[string]string) []string {
	roles := []string{}
	if nodeSelector == nil {
		return roles
	}

	// FIXME: should we protect against multiple labels and return
	// an empty string if there are multiple?
	for k := range nodeSelector {
		if strings.HasPrefix(k, nodeRolePrefix) {
			roles = append(roles, strings.TrimPrefix(k, nodeRolePrefix))
		}
	}

	return roles
}

// AnyMcfgPoolLabelMatches verifies if the given nodeSelector matches the nodeSelector
// in any of the given MachineConfigPools
func AnyMcfgPoolLabelMatches(nodeSelector map[string]string, poolList *mcfgv1.MachineConfigPoolList) (bool, *mcfgv1.MachineConfigPool) {
	foundPool := &mcfgv1.MachineConfigPool{}
	for i := range poolList.Items {
		if McfgPoolLabelMatches(nodeSelector, &poolList.Items[i]) {
			return true, &poolList.Items[i]
		}
	}
	return false, foundPool
}

// isMcfgPoolUsingKC check if a MachineConfig Pool is using a custom Kubelet Config
// if any custom Kublet Config used, return name of generated latest KC machine config from the custom kubelet config
func IsMcfgPoolUsingKC(pool *mcfgv1.MachineConfigPool) (bool, string, error) {
	maxNum := -1
	// currentKCMC store and find kueblet MachineConfig with larges num at the end, therefore the latest kueblet MachineConfig
	var currentKCMC string
	for i := range pool.Spec.Configuration.Source {
		kcName := pool.Spec.Configuration.Source[i].Name
		// The prefix has to start with 99 since the kubeletconfig generated machine config will always start with 99
		if strings.HasPrefix(kcName, "99-") && strings.Contains(kcName, generatedKubelet) {
			// First find if there is just one cutom KubeletConfig
			if maxNum == -1 {
				if strings.HasSuffix(kcName, generatedKubeletSuffix) {
					maxNum = 0
					currentKCMC = kcName
					continue
				}
			}

			lastByteNum := kcName[len(kcName)-1:]
			num, err := strconv.Atoi(lastByteNum)
			if err != nil {
				return false, "", fmt.Errorf("string-int convertion error for KC remediation: %w", err)
			}
			if num > maxNum {
				maxNum = num
				currentKCMC = kcName
			}

		}
	}
	// no custom kubelet machine config is found
	if maxNum == -1 {
		return false, currentKCMC, nil
	}

	return true, currentKCMC, nil
}

func GetScanType(annotations map[string]string) compliancev1alpha1.ComplianceScanType {
	// The default type is platform
	platformType, ok := annotations[compliancev1alpha1.ProductTypeAnnotation]
	if !ok {
		return compliancev1alpha1.ScanTypePlatform
	}

	switch strings.ToLower(platformType) {
	case strings.ToLower(string(compliancev1alpha1.ScanTypeNode)):
		return compliancev1alpha1.ScanTypeNode
	default:
		break
	}

	return compliancev1alpha1.ScanTypePlatform
}

func GetKCFromMC(mc *mcfgv1.MachineConfig, client runtimeclient.Client) (*mcfgv1.KubeletConfig, error) {
	if mc == nil {
		return nil, fmt.Errorf("machine config is nil")
	}
	if len(mc.GetOwnerReferences()) != 0 {
		if mc.GetOwnerReferences()[0].Kind == "KubeletConfig" {
			kubeletName := mc.GetOwnerReferences()[0].Name
			kubeletConfig := &mcfgv1.KubeletConfig{}
			kcKey := types.NamespacedName{Name: kubeletName}
			if err := client.Get(context.TODO(), kcKey, kubeletConfig); err != nil {
				return nil, fmt.Errorf("couldn't get current KubeletConfig: %w", err)
			}
			return kubeletConfig, nil
		}
	}
	return nil, fmt.Errorf("machine config %s doesn't have a KubeletConfig owner reference", mc.GetName())
}

// removeNodeSizingEnvParams remove KubeletConfig Parameter related to /etc/node-sizing-enabled.env,
// as it is not rendered in the MachineConfig to file /etc/kubernetes/kubelet.conf
func removeNodeSizingEnvParams(mc []byte) ([]byte, error) {
	var data map[string]json.RawMessage

	if err := json.Unmarshal(mc, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal kubelet config: %w", err)
	}

	for _, key := range nodeSizingEnvList {
		delete(data, key)
	}
	return json.Marshal(data)
}

// McfgPoolLabelMatches verifies if the given nodeSelector matches the given MachineConfigPool's nodeSelector
func McfgPoolLabelMatches(nodeSelector map[string]string, pool *mcfgv1.MachineConfigPool) bool {
	if nodeSelector == nil {
		return false
	}

	if pool.Spec.NodeSelector == nil {
		return false
	}
	// TODO(jaosorior): Make this work with MatchExpression
	if pool.Spec.NodeSelector.MatchLabels == nil {
		return false
	}

	return reflect.DeepEqual(nodeSelector, pool.Spec.NodeSelector.MatchLabels)
}

func GetNodeRoleSelector(role string) map[string]string {
	if role == cmpv1alpha1.AllRoles {
		return map[string]string{}
	}
	return map[string]string{
		nodeRolePrefix + role: "",
	}
}
