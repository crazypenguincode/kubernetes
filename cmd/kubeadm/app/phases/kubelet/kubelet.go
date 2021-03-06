/*
Copyright 2017 The Kubernetes Authors.

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

package kubelet

import (
	"encoding/json"
	"fmt"

	"k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	kubeadmutil "k8s.io/kubernetes/cmd/kubeadm/app/util"
	"k8s.io/kubernetes/cmd/kubeadm/app/util/apiclient"
	rbachelper "k8s.io/kubernetes/pkg/apis/rbac/v1"
	kubeletconfigscheme "k8s.io/kubernetes/pkg/kubelet/apis/kubeletconfig/scheme"
	kubeletconfigv1alpha1 "k8s.io/kubernetes/pkg/kubelet/apis/kubeletconfig/v1alpha1"
)

// CreateBaseKubeletConfiguration creates base kubelet configuration for dynamic kubelet configuration feature.
func CreateBaseKubeletConfiguration(cfg *kubeadmapi.MasterConfiguration, client clientset.Interface) error {
	_, kubeletCodecs, err := kubeletconfigscheme.NewSchemeAndCodecs()
	if err != nil {
		return err
	}
	kubeletBytes, err := kubeadmutil.MarshalToYamlForCodecs(cfg.KubeletConfiguration.BaseConfig, kubeletconfigv1alpha1.SchemeGroupVersion, *kubeletCodecs)
	if err != nil {
		return err
	}

	if err = apiclient.CreateOrUpdateConfigMap(client, &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeadmconstants.KubeletBaseConfigurationConfigMap,
			Namespace: metav1.NamespaceSystem,
		},
		Data: map[string]string{
			kubeadmconstants.KubeletBaseConfigurationConfigMapKey: string(kubeletBytes),
		},
	}); err != nil {
		return err
	}

	if err := createKubeletBaseConfigMapRBACRules(client); err != nil {
		return fmt.Errorf("error creating base kubelet configmap RBAC rules: %v", err)
	}

	return UpdateNodeWithConfigMap(client, cfg.NodeName)
}

// UpdateNodeWithConfigMap updates node ConfigSource with KubeletBaseConfigurationConfigMap
func UpdateNodeWithConfigMap(client clientset.Interface, nodeName string) error {
	// Loop on every falsy return. Return with an error if raised. Exit successfully if true is returned.
	return wait.Poll(kubeadmconstants.APICallRetryInterval, kubeadmconstants.UpdateNodeTimeout, func() (bool, error) {
		node, err := client.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		oldData, err := json.Marshal(node)
		if err != nil {
			return false, err
		}

		kubeletCfg, err := client.CoreV1().ConfigMaps(metav1.NamespaceSystem).Get(kubeadmconstants.KubeletBaseConfigurationConfigMap, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		node.Spec.ConfigSource.ConfigMapRef.UID = kubeletCfg.UID

		newData, err := json.Marshal(node)
		if err != nil {
			return false, err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, v1.Node{})
		if err != nil {
			return false, err
		}

		if _, err := client.CoreV1().Nodes().Patch(node.Name, types.StrategicMergePatchType, patchBytes); err != nil {
			if apierrs.IsConflict(err) {
				fmt.Println("Temporarily unable to update node metadata due to conflict (will retry)")
				return false, nil
			}
			return false, err
		}

		return true, nil
	})
}

// createKubeletBaseConfigMapRBACRules creates the RBAC rules for exposing the base kubelet ConfigMap in the kube-system namespace to unauthenticated users
func createKubeletBaseConfigMapRBACRules(client clientset.Interface) error {
	if err := apiclient.CreateOrUpdateRole(client, &rbac.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeadmconstants.KubeletBaseConfigMapRoleName,
			Namespace: metav1.NamespaceSystem,
		},
		Rules: []rbac.PolicyRule{
			rbachelper.NewRule("get").Groups("").Resources("configmaps").Names(kubeadmconstants.KubeletBaseConfigurationConfigMap).RuleOrDie(),
		},
	}); err != nil {
		return err
	}

	return apiclient.CreateOrUpdateRoleBinding(client, &rbac.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeadmconstants.KubeletBaseConfigMapRoleName,
			Namespace: metav1.NamespaceSystem,
		},
		RoleRef: rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "Role",
			Name:     kubeadmconstants.KubeletBaseConfigMapRoleName,
		},
		Subjects: []rbac.Subject{
			{
				Kind: "Group",
				Name: kubeadmconstants.NodesGroup,
			},
		},
	})
}
