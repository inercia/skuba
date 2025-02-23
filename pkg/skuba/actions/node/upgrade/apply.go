/*
 * Copyright (c) 2019 SUSE LLC.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package upgrade

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	kubeadmconfigutil "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	"github.com/SUSE/skuba/internal/pkg/skuba/deployments"
	"github.com/SUSE/skuba/internal/pkg/skuba/kubeadm"
	"github.com/SUSE/skuba/internal/pkg/skuba/kubernetes"
	"github.com/SUSE/skuba/internal/pkg/skuba/node"
	upgradenode "github.com/SUSE/skuba/internal/pkg/skuba/upgrade/node"
	"github.com/SUSE/skuba/pkg/skuba"
)

func Apply(target *deployments.Target) error {
	fmt.Printf("%s\n", skuba.CurrentVersion().String())

	if err := fillTargetWithNodeName(target); err != nil {
		return err
	}

	currentClusterVersion, err := kubeadm.GetCurrentClusterVersion()
	if err != nil {
		return err
	}
	currentVersion := currentClusterVersion.String()
	latestVersion := kubernetes.LatestVersion().String()
	fmt.Printf("Current Kubernetes cluster version: %s\n", currentVersion)
	fmt.Printf("Latest Kubernetes version: %s\n", latestVersion)
	fmt.Println()

	nodeVersionInfoUpdate, err := upgradenode.UpdateStatus(target.Nodename)
	if err != nil {
		return err
	}

	if nodeVersionInfoUpdate.IsUpdated() {
		fmt.Printf("Node %s is up to date\n", target.Nodename)
		return nil
	}

	// Check if the target node is the first control plane to be updated
	if nodeVersionInfoUpdate.IsFirstControlPlaneNodeToBeUpgraded() {
		upgradeable, err := kubernetes.AllWorkerNodesTolerateVersion(nodeVersionInfoUpdate.Update.APIServerVersion)
		if err != nil {
			return err
		}
		if upgradeable {
			fmt.Println("Fetching the cluster configuration...")

			initCfg, err := kubeadm.GetClusterConfiguration()
			if err != nil {
				return err
			}
			node.AddTargetInformationToInitConfigurationWithClusterVersion(target, initCfg, nodeVersionInfoUpdate.Update.APIServerVersion)
			kubeadm.SetContainerImagesWithClusterVersion(initCfg, nodeVersionInfoUpdate.Update.APIServerVersion)
			initCfgContents, err := kubeadmconfigutil.MarshalInitConfigurationToBytes(initCfg, schema.GroupVersion{
				Group:   "kubeadm.k8s.io",
				Version: "v1beta2",
			})
			if err != nil {
				return err
			}

			fmt.Printf("Performing node %s (%s) upgrade, please wait...\n", target.Nodename, target.Target)

			err = target.Apply(deployments.KubernetesBaseOSConfiguration{
				KubeadmVersion:    nodeVersionInfoUpdate.Update.APIServerVersion.String(),
				KubernetesVersion: nodeVersionInfoUpdate.Current.APIServerVersion.String(),
			}, "kubernetes.install-base-packages")
			if err != nil {
				return err
			}
			err = target.Apply(deployments.UpgradeConfiguration{
				KubeadmConfigContents: string(initCfgContents),
			}, "kubeadm.upgrade.apply")
			if err != nil {
				return err
			}
			err = target.Apply(deployments.KubernetesBaseOSConfiguration{
				KubeadmVersion:    nodeVersionInfoUpdate.Update.APIServerVersion.String(),
				KubernetesVersion: nodeVersionInfoUpdate.Update.APIServerVersion.String(),
			}, "kubernetes.install-base-packages")
			if err != nil {
				return err
			}
			if err := target.Apply(nil, "kubernetes.restart-services"); err != nil {
				return err
			}
		}
	} else {
		// there is already at least one updated control plane node
		upgradeable := true
		if nodeVersionInfoUpdate.Current.IsControlPlane() {
			upgradeable, err = kubernetes.AllWorkerNodesTolerateVersion(currentClusterVersion)
			if err != nil {
				return err
			}
		}
		if upgradeable {
			fmt.Printf("Performing node %s (%s) upgrade, please wait...\n", target.Nodename, target.Target)

			err := target.Apply(deployments.KubernetesBaseOSConfiguration{
				KubeadmVersion:    nodeVersionInfoUpdate.Update.KubeletVersion.String(),
				KubernetesVersion: nodeVersionInfoUpdate.Current.KubeletVersion.String(),
			}, "kubernetes.install-base-packages")
			if err != nil {
				return err
			}
			if err := target.Apply(nil, "kubeadm.upgrade.node"); err != nil {
				return err
			}
			err = target.Apply(deployments.KubernetesBaseOSConfiguration{
				KubeadmVersion:    nodeVersionInfoUpdate.Update.KubeletVersion.String(),
				KubernetesVersion: nodeVersionInfoUpdate.Update.KubeletVersion.String(),
			}, "kubernetes.install-base-packages")
			if err != nil {
				return err
			}
			if err := target.Apply(nil, "kubernetes.restart-services"); err != nil {
				return err
			}
		}
	}

	fmt.Printf("Node %s (%s) successfully upgraded\n", target.Nodename, target.Target)

	return nil
}

func fillTargetWithNodeName(target *deployments.Target) error {
	machineId, err := target.DownloadFileContents("/etc/machine-id")
	if err != nil {
		return err
	}
	node, err := kubernetes.GetNodeWithMachineId(strings.TrimSuffix(machineId, "\n"))
	if err != nil {
		return err
	}
	target.Nodename = node.ObjectMeta.Name
	return nil
}
