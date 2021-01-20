// dbaas-controller
// Copyright (C) 2020 Percona LLC
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

// Package k8sclient provides client for kubernetes.
package k8sclient

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/AlekSi/pointer"
	"github.com/pkg/errors"

	"github.com/percona-platform/dbaas-controller/service/k8sclient/internal/common"
	"github.com/percona-platform/dbaas-controller/service/k8sclient/internal/psmdb"
	"github.com/percona-platform/dbaas-controller/service/k8sclient/internal/pxc"
	"github.com/percona-platform/dbaas-controller/service/k8sclient/kubectl"
	"github.com/percona-platform/dbaas-controller/utils/logger"
)

// ClusterKind is a kind of a cluster.
type ClusterKind string

const (
	perconaXtraDBClusterKind = ClusterKind("PerconaXtraDBCluster")
	perconaServerMongoDBKind = ClusterKind("PerconaServerMongoDB")
	passwordLength           = 24
)

// ClusterState represents XtraDB cluster CR state.
type ClusterState int32

const (
	// ClusterStateInvalid represents unknown state.
	ClusterStateInvalid ClusterState = 0
	// ClusterStateChanging represents a cluster being changed (initializing).
	ClusterStateChanging ClusterState = 1
	// ClusterStateFailed represents a failed cluster (error).
	ClusterStateFailed ClusterState = 2
	// ClusterStateReady represents a cluster without pending changes (ready).
	ClusterStateReady ClusterState = 3
	// ClusterStateDeleting represents a cluster which are in deleting state (deleting).
	ClusterStateDeleting ClusterState = 4
	// ClusterStatePaused represents a paused cluster state (status.state.ready and spec.pause.true).
	ClusterStatePaused ClusterState = 5
)

const (
	pmmClientImage = "perconalab/pmm-client:dev-latest"

	k8sAPIVersion     = "v1"
	k8sMetaKindSecret = "Secret"

	pxcCRVersion         = "1.7.0"
	pxcBackupImage       = "percona/percona-xtradb-cluster-operator:1.6.0-pxc8.0-backup"
	pxcImage             = "percona/percona-xtradb-cluster:8.0.20-11.1"
	pxcBackupStorageName = "pxc-backup-storage-%s"
	pxcAPIVersion        = "pxc.percona.com/v1-6-0"
	pxcProxySQLImage     = "percona/percona-xtradb-cluster-operator:1.6.0-proxysql"
	pxcSecretNameTmpl    = "dbaas-%s-pxc-secrets"
	defaultPXCSecretName = "my-cluster-secrets"

	psmdbCRVersion         = "1.6.0"
	psmdbBackupImage       = "percona/percona-server-mongodb-operator:1.5.0-backup"
	psmdbImage             = "percona/percona-server-mongodb:4.2.8-8"
	psmdbAPIVersion        = "psmdb.percona.com/v1-6-0"
	psmdbSecretNameTmpl    = "dbaas-%s-psmdb-secrets"
	defaultPSMDBSecretName = "my-cluster-name-secrets"
)

// OperatorStatus represents status of operator.
type OperatorStatus int32

const (
	// OperatorStatusOK represents that operators are installed and have supported API version.
	OperatorStatusOK OperatorStatus = 1
	// OperatorStatusUnsupported represents that operators are installed, but doesn't have supported API version.
	OperatorStatusUnsupported OperatorStatus = 2
	// OperatorStatusNotInstalled represents that operators are not installed.
	OperatorStatusNotInstalled OperatorStatus = 3
)

// Operators contains statuses of operators.
type Operators struct {
	Xtradb OperatorStatus
	Psmdb  OperatorStatus
}

// ComputeResources represents container computer resources requests or limits.
type ComputeResources struct {
	CPUM        string
	MemoryBytes string
}

// PXC contains information related to PXC containers in Percona XtraDB cluster.
type PXC struct {
	ComputeResources *ComputeResources
	DiskSize         string
}

// ProxySQL contains information related to ProxySQL containers in Percona XtraDB cluster.
type ProxySQL struct {
	ComputeResources *ComputeResources
	DiskSize         string
}

// Replicaset contains information related to Replicaset containers in PSMDB cluster.
type Replicaset struct {
	ComputeResources *ComputeResources
	DiskSize         string
}

// XtraDBParams contains all parameters required to create or update Percona XtraDB cluster.
type XtraDBParams struct {
	Name             string
	PMMPublicAddress string
	Size             int32
	Suspend          bool
	Resume           bool
	PXC              *PXC
	ProxySQL         *ProxySQL
}

// Cluster contains common information related to cluster.
type Cluster struct {
	Name string
}

// PSMDBParams contains all parameters required to create or update percona server for mongodb cluster.
type PSMDBParams struct {
	Name             string
	PMMPublicAddress string
	Size             int32
	Suspend          bool
	Resume           bool
	Replicaset       *Replicaset
}

// XtraDBCluster contains information related to xtradb cluster.
type XtraDBCluster struct {
	Name     string
	Size     int32
	State    ClusterState
	Message  string
	PXC      *PXC
	ProxySQL *ProxySQL
	Pause    bool
}

// PSMDBCluster contains information related to psmdb cluster.
type PSMDBCluster struct {
	Name       string
	Pause      bool
	Size       int32
	State      ClusterState
	Message    string
	Replicaset *Replicaset
}

// PSMDBCredentials represents PSMDB connection credentials.
type PSMDBCredentials struct {
	Username   string
	Password   string
	Host       string
	Port       int32
	Replicaset string
}

// XtraDBCredentials represents XtraDB connection credentials.
type XtraDBCredentials struct {
	Username string
	Password string
	Host     string
	Port     int32
}

// StorageClass represents a cluster storage class information.
// We use the Items.Provisioner to detect if we are running against minikube or AWS.
// Returned value examples:
// - AWS EKS: kubernetes.io/aws-ebs
// - Minukube: k8s.io/minikube-hostpath.
type StorageClass struct {
	APIVersion string `json:"apiVersion"`
	Items      []struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Annotations struct {
				KubectlKubernetesIoLastAppliedConfiguration string `json:"kubectl.kubernetes.io/last-applied-configuration"`
				StorageclassKubernetesIoIsDefaultClass      string `json:"storageclass.kubernetes.io/is-default-class"`
			} `json:"annotations"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
			Labels            struct {
				AddonmanagerKubernetesIoMode string `json:"addonmanager.kubernetes.io/mode"`
			} `json:"labels"`
			Name            string `json:"name"`
			ResourceVersion string `json:"resourceVersion"`
			SelfLink        string `json:"selfLink"`
			UID             string `json:"uid"`
		} `json:"metadata"`
		Provisioner       string `json:"provisioner"`
		ReclaimPolicy     string `json:"reclaimPolicy"`
		VolumeBindingMode string `json:"volumeBindingMode"`
	} `json:"items"`
	Kind     string `json:"kind"`
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
		SelfLink        string `json:"selfLink"`
	} `json:"metadata"`
}

// pxcStatesMap matches pxc app states to cluster states.
var pxcStatesMap = map[pxc.AppState]ClusterState{ //nolint:gochecknoglobals
	pxc.AppStateUnknown: ClusterStateInvalid,
	pxc.AppStateInit:    ClusterStateChanging,
	pxc.AppStateReady:   ClusterStateReady,
	pxc.AppStateError:   ClusterStateFailed,
}

// psmdbStatesMap matches psmdb app states to cluster states.
var psmdbStatesMap = map[psmdb.AppState]ClusterState{ //nolint:gochecknoglobals
	psmdb.AppStateUnknown: ClusterStateInvalid,
	psmdb.AppStatePending: ClusterStateChanging,
	psmdb.AppStateInit:    ClusterStateChanging,
	psmdb.AppStateReady:   ClusterStateReady,
	psmdb.AppStateError:   ClusterStateFailed,
}

var (
	// ErrXtraDBClusterNotReady The PXC cluster is not in ready state.
	ErrXtraDBClusterNotReady = errors.New("XtraDB cluster is not ready")
	// ErrPSMDBClusterNotReady The PSMDB cluster is not ready.
	ErrPSMDBClusterNotReady = errors.New("PSMDB cluster is not ready")
)

// K8sClient is a client for Kubernetes.
type K8sClient struct {
	kubeCtl *kubectl.KubeCtl
	l       logger.Logger
}

// New returns new K8sClient object.
func New(ctx context.Context, kubeconfig string) (*K8sClient, error) {
	l := logger.Get(ctx)
	l = l.WithField("component", "K8sClient")
	kubeCtl, err := kubectl.NewKubeCtl(ctx, kubeconfig)
	if err != nil {
		return nil, err
	}
	return &K8sClient{
		kubeCtl: kubeCtl,
		l:       l,
	}, nil
}

// Cleanup removes temporary files created by that object.
func (c *K8sClient) Cleanup() error {
	return c.kubeCtl.Cleanup()
}

// ListXtraDBClusters returns list of Percona XtraDB clusters and their statuses.
func (c *K8sClient) ListXtraDBClusters(ctx context.Context) ([]XtraDBCluster, error) {
	perconaXtraDBClusters, err := c.getPerconaXtraDBClusters(ctx)
	if err != nil {
		return nil, err
	}

	deletingClusters, err := c.getDeletingXtraDBClusters(ctx, perconaXtraDBClusters)
	if err != nil {
		return nil, err
	}
	res := append(perconaXtraDBClusters, deletingClusters...)

	return res, nil
}

// CreateSecret creates secret resource to use as credential source for clusters.
func (c *K8sClient) CreateSecret(ctx context.Context, secretName string, data map[string][]byte) error {
	secret := common.Secret{
		TypeMeta: common.TypeMeta{
			APIVersion: k8sAPIVersion,
			Kind:       k8sMetaKindSecret,
		},
		ObjectMeta: common.ObjectMeta{
			Name: secretName,
		},
		Type: common.SecretTypeOpaque,
		Data: data,
	}
	return c.kubeCtl.Apply(ctx, secret)
}

func randSeq(n int) string {
	rand.Seed(time.Now().UnixNano())
	// PSMDB do not support all special characters in password https://jira.percona.com/browse/K8SPSMDB-364
	symbols := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789~=+%^*/(){}!$|")
	symbolsLen := len(symbols)
	b := make([]rune, n)
	for i := range b {
		b[i] = symbols[rand.Intn(symbolsLen)]
	}
	return string(b)
}

// CreateXtraDBCluster creates Percona XtraDB cluster with provided parameters.
func (c *K8sClient) CreateXtraDBCluster(ctx context.Context, params *XtraDBParams) error {
	var secret common.Secret
	err := c.kubeCtl.Get(ctx, k8sMetaKindSecret, defaultPXCSecretName, &secret)
	if err != nil {
		return errors.Wrap(err, "cannot get default PXC secrets")
	}

	secretName := fmt.Sprintf(pxcSecretNameTmpl, params.Name)
	pwd := randSeq(passwordLength)

	// TODO: add a link to ticket to set random password for all other users.
	data := secret.Data
	data["root"] = []byte(pwd)

	err = c.CreateSecret(ctx, secretName, data)
	if err != nil {
		return errors.Wrap(err, "cannot create secret for PXC")
	}

	storageName := fmt.Sprintf(pxcBackupStorageName, params.Name)
	res := &pxc.PerconaXtraDBCluster{
		TypeMeta: common.TypeMeta{
			APIVersion: pxcAPIVersion,
			Kind:       string(perconaXtraDBClusterKind),
		},
		ObjectMeta: common.ObjectMeta{
			Name: params.Name,
		},
		Spec: pxc.PerconaXtraDBClusterSpec{
			CRVersion:         pxcCRVersion,
			AllowUnsafeConfig: true,
			SecretsName:       secretName,

			PXC: &pxc.PodSpec{
				Size:       params.Size,
				Resources:  c.setComputeResources(params.PXC.ComputeResources),
				Image:      pxcImage,
				VolumeSpec: c.volumeSpec(params.PXC.DiskSize),
				Affinity: &pxc.PodAffinity{
					TopologyKey: pointer.ToString(pxc.AffinityTopologyKeyOff),
				},
				PodDisruptionBudget: &common.PodDisruptionBudgetSpec{
					MaxUnavailable: pointer.ToInt(1),
				},
			},

			ProxySQL: &pxc.PodSpec{
				Enabled:    true,
				Size:       params.Size,
				Resources:  c.setComputeResources(params.ProxySQL.ComputeResources),
				Image:      pxcProxySQLImage,
				VolumeSpec: c.volumeSpec(params.ProxySQL.DiskSize),
				Affinity: &pxc.PodAffinity{
					TopologyKey: pointer.ToString(pxc.AffinityTopologyKeyOff),
				},
			},

			PMM: &pxc.PMMSpec{
				Enabled:    params.PMMPublicAddress != "",
				ServerHost: params.PMMPublicAddress,
				ServerUser: "admin",
				Image:      pmmClientImage,
				Resources: &common.PodResources{
					Requests: &common.ResourcesList{
						Memory: "500M",
						CPU:    "500m",
					},
				},
			},

			Backup: &pxc.PXCScheduledBackup{
				Image: pxcBackupImage,
				Schedule: []pxc.PXCScheduledBackupSchedule{{
					Name:        "test",
					Schedule:    "*/30 * * * *",
					Keep:        3,
					StorageName: storageName,
				}},
				Storages: map[string]*pxc.BackupStorageSpec{
					storageName: {
						Type:   pxc.BackupStorageFilesystem,
						Volume: c.volumeSpec(params.PXC.DiskSize),
					},
				},
				ServiceAccountName: "percona-xtradb-cluster-operator",
			},
		},
	}

	// This enables ingress for the cluster and exposes the cluster to the world.
	// The cluster will have an internal IP and a world accessible hostname.
	// This feature cannot be tested with minikube. Please use EKS for testing.
	if isMinikube, err := c.isMinikube(ctx); err == nil && !isMinikube {
		res.Spec.ProxySQL.ServiceType = common.ServiceTypeLoadBalancer
	}

	return c.kubeCtl.Apply(ctx, res)
}

// UpdateXtraDBCluster changes size of provided Percona XtraDB cluster.
func (c *K8sClient) UpdateXtraDBCluster(ctx context.Context, params *XtraDBParams) error {
	var cluster pxc.PerconaXtraDBCluster
	err := c.kubeCtl.Get(ctx, string(perconaXtraDBClusterKind), params.Name, &cluster)
	if err != nil {
		return err
	}

	// This is to prevent concurrent updates
	if cluster.Status.PXC.Status != pxc.AppStateReady {
		return errors.Wrapf(ErrXtraDBClusterNotReady, "state is %v", cluster.Status.Status) //nolint:wrapcheck
	}

	if params.Resume {
		cluster.Spec.Pause = false
	}
	if params.Suspend {
		cluster.Spec.Pause = true
	}

	if params.Size > 0 {
		cluster.Spec.PXC.Size = params.Size
		cluster.Spec.ProxySQL.Size = params.Size
	}

	if params.PXC != nil {
		cluster.Spec.PXC.Resources = c.updateComputeResources(params.PXC.ComputeResources, cluster.Spec.PXC.Resources)
	}

	if params.ProxySQL != nil {
		cluster.Spec.ProxySQL.Resources = c.updateComputeResources(params.ProxySQL.ComputeResources, cluster.Spec.ProxySQL.Resources)
	}

	return c.kubeCtl.Apply(ctx, &cluster)
}

// DeleteXtraDBCluster deletes Percona XtraDB cluster with provided name.
func (c *K8sClient) DeleteXtraDBCluster(ctx context.Context, name string) error {
	res := &pxc.PerconaXtraDBCluster{
		TypeMeta: common.TypeMeta{
			APIVersion: pxcAPIVersion,
			Kind:       string(perconaXtraDBClusterKind),
		},
		ObjectMeta: common.ObjectMeta{
			Name: name,
		},
	}
	err := c.kubeCtl.Delete(ctx, res)
	if err != nil {
		return errors.Wrap(err, "cannot delete PXC")
	}

	secret := &common.Secret{
		TypeMeta: common.TypeMeta{
			APIVersion: k8sAPIVersion,
			Kind:       k8sMetaKindSecret,
		},
		ObjectMeta: common.ObjectMeta{
			Name: fmt.Sprintf(pxcSecretNameTmpl, name),
		},
	}

	err = c.kubeCtl.Delete(ctx, secret)
	if err != nil {
		c.l.Errorf("cannot delete secret for %s: %v", name, err)
	}

	return nil
}

// GetXtraDBCluster returns an XtraDB cluster credentials.
func (c *K8sClient) GetXtraDBCluster(ctx context.Context, name string) (*XtraDBCredentials, error) {
	var cluster pxc.PerconaXtraDBCluster
	err := c.kubeCtl.Get(ctx, string(perconaXtraDBClusterKind), name, &cluster)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get XtraDb cluster")
	}

	password := ""
	var secret common.Secret
	// Retrieve secrets only for initializing or ready cluster.
	if cluster.Status.Status == pxc.AppStateReady || cluster.Status.Status == pxc.AppStateInit {
		err = c.kubeCtl.Get(ctx, k8sMetaKindSecret, fmt.Sprintf(pxcSecretNameTmpl, name), &secret)
		if err != nil {
			return nil, errors.Wrap(err, "cannot get XtraDb cluster secrets")
		}
		password = string(secret.Data["root"])
	}

	credentials := &XtraDBCredentials{
		Host:     cluster.Status.Host,
		Port:     3306,
		Username: "root",
		Password: password,
	}

	return credentials, nil
}

// GetXtraDBCluster returns an XtraDB cluster credentials.
func (c *K8sClient) getStorageClass(ctx context.Context) (*StorageClass, error) {
	var storageClass *StorageClass

	err := c.kubeCtl.Get(ctx, "storageclass", "", &storageClass)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get storageClass")
	}

	return storageClass, nil
}

func (c *K8sClient) isMinikube(ctx context.Context) (bool, error) {
	sc, err := c.getStorageClass(ctx)
	if err != nil {
		return false, err
	}

	if len(sc.Items) == 0 {
		return false, fmt.Errorf("cannot get storage class. empty items list")
	}

	if strings.Contains(sc.Items[0].Provisioner, "minikube") {
		return true, nil
	}

	return false, nil
}

func (c *K8sClient) restartDBClusterCmd(name, kind string) []string {
	return []string{"rollout", "restart", "StatefulSets", fmt.Sprintf("%s-%s", name, kind)}
}

// RestartXtraDBCluster restarts Percona XtraDB cluster with provided name.
// FIXME: https://jira.percona.com/browse/PMM-6980
func (c *K8sClient) RestartXtraDBCluster(ctx context.Context, name string) error {
	_, err := c.kubeCtl.Run(ctx, c.restartDBClusterCmd(name, "pxc"), nil)
	if err != nil {
		return err
	}

	// TODO: implement logic to handle the case when there is HAProxy instead of ProxySQL.
	_, err = c.kubeCtl.Run(ctx, c.restartDBClusterCmd(name, "proxysql"), nil)
	if err != nil {
		return err
	}

	return nil
}

// getPerconaXtraDBClusters returns Percona XtraDB clusters.
func (c *K8sClient) getPerconaXtraDBClusters(ctx context.Context) ([]XtraDBCluster, error) {
	var list pxc.PerconaXtraDBClusterList
	err := c.kubeCtl.Get(ctx, string(perconaXtraDBClusterKind), "", &list)
	if err != nil {
		return nil, errors.Wrap(err, "couldn't get Percona XtraDB clusters")
	}

	res := make([]XtraDBCluster, len(list.Items))
	for i, cluster := range list.Items {
		val := XtraDBCluster{
			Name:    cluster.Name,
			Size:    cluster.Spec.ProxySQL.Size,
			State:   getPXCState(cluster.Status.Status),
			Message: strings.Join(cluster.Status.Messages, ";"),
			PXC: &PXC{
				DiskSize:         c.getDiskSize(cluster.Spec.PXC.VolumeSpec),
				ComputeResources: c.getComputeResources(cluster.Spec.PXC.Resources),
			},
			ProxySQL: &ProxySQL{
				DiskSize:         c.getDiskSize(cluster.Spec.ProxySQL.VolumeSpec),
				ComputeResources: c.getComputeResources(cluster.Spec.ProxySQL.Resources),
			},
			Pause: cluster.Spec.Pause,
		}

		res[i] = val
	}
	return res, nil
}

func getPXCState(state pxc.AppState) ClusterState {
	clusterState, ok := pxcStatesMap[state]
	if !ok {
		l := logger.Get(context.Background())
		l = l.WithField("component", "K8sClient")
		l.Warn("Cannot get cluster state. Setting status to ClusterStateChanging")
		return ClusterStateChanging
	}
	return clusterState
}

// getDeletingClusters returns clusters which are not fully deleted yet.
func (c *K8sClient) getDeletingClusters(ctx context.Context, managedBy string, runningClusters map[string]struct{}) ([]Cluster, error) {
	var list common.PodList

	err := c.kubeCtl.Get(ctx, "pods", "", &list)
	if err != nil {
		return nil, errors.Wrap(err, "couldn't get kubernetes pods")
	}

	res := make([]Cluster, 0)
	for _, pod := range list.Items {
		clusterName := pod.Labels["app.kubernetes.io/instance"]
		if _, ok := runningClusters[clusterName]; ok {
			continue
		}

		if pod.Labels["app.kubernetes.io/managed-by"] != managedBy {
			continue
		}

		cluster := Cluster{
			Name: clusterName,
		}
		res = append(res, cluster)

		runningClusters[clusterName] = struct{}{}
	}
	return res, nil
}

// getDeletingXtraDBClusters returns Percona XtraDB clusters which are not fully deleted yet.
func (c *K8sClient) getDeletingXtraDBClusters(ctx context.Context, clusters []XtraDBCluster) ([]XtraDBCluster, error) {
	runningClusters := make(map[string]struct{}, len(clusters))
	for _, cluster := range clusters {
		runningClusters[cluster.Name] = struct{}{}
	}

	deletingClusters, err := c.getDeletingClusters(ctx, "percona-xtradb-cluster-operator", runningClusters)
	if err != nil {
		return nil, err
	}

	xtradbClusters := make([]XtraDBCluster, len(deletingClusters))
	for i, cluster := range deletingClusters {
		xtradbClusters[i] = XtraDBCluster{
			Name:     cluster.Name,
			Size:     0,
			State:    ClusterStateDeleting,
			PXC:      new(PXC),
			ProxySQL: new(ProxySQL),
		}
	}
	return xtradbClusters, nil
}

// ListPSMDBClusters returns list of psmdb clusters and their statuses.
func (c *K8sClient) ListPSMDBClusters(ctx context.Context) ([]PSMDBCluster, error) {
	clusters, err := c.getPSMDBClusters(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get PSMDB clusters")
	}

	deletingClusters, err := c.getDeletingPSMDBClusters(ctx, clusters)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get deleting PSMDB clusters")
	}
	res := append(clusters, deletingClusters...)

	return res, nil
}

// CreatePSMDBCluster creates percona server for mongodb cluster with provided parameters.
func (c *K8sClient) CreatePSMDBCluster(ctx context.Context, params *PSMDBParams) error {
	var secret common.Secret
	err := c.kubeCtl.Get(ctx, k8sMetaKindSecret, defaultPSMDBSecretName, &secret)
	if err != nil {
		return errors.Wrap(err, "cannot get default PSMDB secrets")
	}

	secretName := fmt.Sprintf(psmdbSecretNameTmpl, params.Name)
	pwd := randSeq(passwordLength)

	data := secret.Data
	data["MONGODB_USER_ADMIN_PASSWORD"] = []byte(pwd)

	err = c.CreateSecret(ctx, secretName, data)
	if err != nil {
		return errors.Wrap(err, "cannot create secret for PXC")
	}

	res := &psmdb.PerconaServerMongoDB{
		TypeMeta: common.TypeMeta{
			APIVersion: psmdbAPIVersion,
			Kind:       string(perconaServerMongoDBKind),
		},
		ObjectMeta: common.ObjectMeta{
			Name: params.Name,
		},
		Spec: psmdb.PerconaServerMongoDBSpec{
			CRVersion: psmdbCRVersion,
			Image:     psmdbImage,
			Secrets: &psmdb.SecretsSpec{
				Users: secretName,
			},
			Mongod: &psmdb.MongodSpec{
				Net: &psmdb.MongodSpecNet{
					Port: 27017,
				},
				OperationProfiling: &psmdb.MongodSpecOperationProfiling{
					Mode: psmdb.OperationProfilingModeSlowOp,
				},
				Security: &psmdb.MongodSpecSecurity{
					RedactClientLogData:  false,
					EnableEncryption:     pointer.ToBool(true),
					EncryptionKeySecret:  "my-cluster-name-mongodb-encryption-key",
					EncryptionCipherMode: psmdb.MongodChiperModeCBC,
				},
				Storage: &psmdb.MongodSpecStorage{
					Engine: psmdb.StorageEngineWiredTiger,
					MMAPv1: &psmdb.MongodSpecMMAPv1{
						NsSize:     16,
						Smallfiles: false,
					},
					WiredTiger: &psmdb.MongodSpecWiredTiger{
						CollectionConfig: &psmdb.MongodSpecWiredTigerCollectionConfig{
							BlockCompressor: &psmdb.WiredTigerCompressorSnappy,
						},
						EngineConfig: &psmdb.MongodSpecWiredTigerEngineConfig{
							DirectoryForIndexes: false,
							JournalCompressor:   &psmdb.WiredTigerCompressorSnappy,
						},
						IndexConfig: &psmdb.MongodSpecWiredTigerIndexConfig{
							PrefixCompression: true,
						},
					},
				},
			},
			Sharding: &psmdb.ShardingSpec{
				Enabled: true,
				ConfigsvrReplSet: &psmdb.ReplsetSpec{
					Size:       3,
					VolumeSpec: c.volumeSpec(params.Replicaset.DiskSize),
				},
				Mongos: &psmdb.ReplsetSpec{
					Size: params.Size,
				},
				OperationProfiling: &psmdb.MongodSpecOperationProfiling{
					Mode: psmdb.OperationProfilingModeSlowOp,
				},
			},
			Replsets: []*psmdb.ReplsetSpec{
				{
					Name:      "rs0",
					Size:      params.Size,
					Resources: c.setComputeResources(params.Replicaset.ComputeResources),
					Arbiter: psmdb.Arbiter{
						Enabled: false,
						Size:    1,
						MultiAZ: psmdb.MultiAZ{
							Affinity: &psmdb.PodAffinity{
								TopologyKey: pointer.ToString("kubernetes.io/hostname"),
							},
						},
					},
					VolumeSpec: c.volumeSpec(params.Replicaset.DiskSize),
					PodDisruptionBudget: &common.PodDisruptionBudgetSpec{
						MaxUnavailable: pointer.ToInt(1),
					},
					MultiAZ: psmdb.MultiAZ{
						Affinity: &psmdb.PodAffinity{
							TopologyKey: pointer.ToString(psmdb.AffinityOff),
						},
					},
				},
			},

			PMM: psmdb.PmmSpec{
				Enabled:    params.PMMPublicAddress != "",
				ServerHost: params.PMMPublicAddress,
				Image:      pmmClientImage,
				Resources: &common.PodResources{
					Requests: &common.ResourcesList{
						Memory: "500M",
						CPU:    "500m",
					},
				},
			},

			Backup: psmdb.BackupSpec{
				Enabled:            true,
				Image:              psmdbBackupImage,
				ServiceAccountName: "percona-server-mongodb-operator",
			},
		},
	}
	if params.Replicaset != nil {
		res.Spec.Replsets[0].Resources = c.setComputeResources(params.Replicaset.ComputeResources)
		res.Spec.Sharding.Mongos.Resources = c.setComputeResources(params.Replicaset.ComputeResources)
	}

	if isMinikube, err := c.isMinikube(ctx); err == nil && !isMinikube {
		// This enables ingress for the cluster and exposes the cluster to the world.
		// The cluster will have an internal IP and a world accessible hostname.
		// This feature cannot be tested with minikube. Please use EKS for testing.
		res.Spec.Sharding.Mongos.Expose = psmdb.Expose{
			Enabled:    true,
			ExposeType: common.ServiceTypeLoadBalancer,
		}
	} else {
		// https://www.percona.com/doc/kubernetes-operator-for-psmongodb/minikube.html
		// > Install Percona Server for MongoDB on Minikube
		// > ...
		// > set affinity.antiAffinityTopologyKey key to "none"
		// > (the Operator will be unable to spread the cluster on several nodes)
		res.Spec.Replsets[0].Arbiter.Affinity.TopologyKey = pointer.ToString("none")
	}

	return c.kubeCtl.Apply(ctx, res)
}

// UpdatePSMDBCluster changes size of provided percona server for mongodb cluster.
func (c *K8sClient) UpdatePSMDBCluster(ctx context.Context, params *PSMDBParams) error {
	var cluster psmdb.PerconaServerMongoDB
	err := c.kubeCtl.Get(ctx, string(perconaServerMongoDBKind), params.Name, &cluster)
	if err != nil {
		return errors.Wrap(err, "UpdatePSMDBCluster get error")
	}

	// This is to prevent concurrent updates
	if cluster.Status.Status != psmdb.AppStateReady {
		return errors.Wrapf(ErrPSMDBClusterNotReady, "state is %v", cluster.Status.Status) //nolint:wrapcheck
	}

	if params.Size > 0 {
		cluster.Spec.Replsets[0].Size = params.Size
	}

	if params.Resume {
		cluster.Spec.Pause = false
	}
	if params.Suspend {
		cluster.Spec.Pause = true
	}

	if params.Replicaset != nil {
		cluster.Spec.Replsets[0].Resources = c.updateComputeResources(params.Replicaset.ComputeResources, cluster.Spec.Replsets[0].Resources)
	}

	return c.kubeCtl.Apply(ctx, cluster)
}

// DeletePSMDBCluster deletes percona server for mongodb cluster with provided name.
func (c *K8sClient) DeletePSMDBCluster(ctx context.Context, name string) error {
	res := &psmdb.PerconaServerMongoDB{
		TypeMeta: common.TypeMeta{
			APIVersion: psmdbAPIVersion,
			Kind:       string(perconaServerMongoDBKind),
		},
		ObjectMeta: common.ObjectMeta{
			Name: name,
		},
	}
	err := c.kubeCtl.Delete(ctx, res)
	if err != nil {
		return errors.Wrap(err, "cannot delete PSMDB")
	}

	secret := &common.Secret{
		TypeMeta: common.TypeMeta{
			APIVersion: k8sAPIVersion,
			Kind:       k8sMetaKindSecret,
		},
		ObjectMeta: common.ObjectMeta{
			Name: fmt.Sprintf(psmdbSecretNameTmpl, name),
		},
	}

	err = c.kubeCtl.Delete(ctx, secret)
	if err != nil {
		c.l.Errorf("cannot delete secret for %s: %v", name, err)
	}

	return nil
}

// RestartPSMDBCluster restarts Percona server for mongodb cluster with provided name.
// FIXME: https://jira.percona.com/browse/PMM-6980
func (c *K8sClient) RestartPSMDBCluster(ctx context.Context, name string) error {
	_, err := c.kubeCtl.Run(ctx, c.restartDBClusterCmd(name, "rs0"), nil)

	return err
}

// GetPSMDBCluster returns a PSMDB cluster.
func (c *K8sClient) GetPSMDBCluster(ctx context.Context, name string) (*PSMDBCredentials, error) {
	var cluster psmdb.PerconaServerMongoDB
	err := c.kubeCtl.Get(ctx, string(perconaServerMongoDBKind), name, &cluster)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get PSMDB cluster")
	}

	password := ""
	username := ""
	var secret common.Secret
	// Retrieve secrets only for initializing or ready cluster.
	if cluster.Status.Status == psmdb.AppStateReady || cluster.Status.Status == psmdb.AppStateInit {
		err = c.kubeCtl.Get(ctx, k8sMetaKindSecret, fmt.Sprintf(psmdbSecretNameTmpl, name), &secret)
		if err != nil {
			return nil, errors.Wrap(err, "cannot get PSMDB cluster secrets")
		}
		username = string(secret.Data["MONGODB_USER_ADMIN_USER"])
		password = string(secret.Data["MONGODB_USER_ADMIN_PASSWORD"])
	}

	credentials := &PSMDBCredentials{
		Username:   username,
		Password:   password,
		Host:       cluster.Status.Host,
		Port:       27017,
		Replicaset: "rs0",
	}

	return credentials, nil
}

// getPSMDBClusters returns Percona Server for MongoDB clusters.
func (c *K8sClient) getPSMDBClusters(ctx context.Context) ([]PSMDBCluster, error) {
	var list psmdb.PerconaServerMongoDBList
	err := c.kubeCtl.Get(ctx, string(perconaServerMongoDBKind), "", &list)
	if err != nil {
		return nil, errors.Wrap(err, "couldn't get percona server MongoDB clusters")
	}

	res := make([]PSMDBCluster, len(list.Items))
	for i, cluster := range list.Items {
		message := cluster.Status.Message
		conditions := cluster.Status.Conditions
		if message == "" && len(conditions) > 0 {
			message = conditions[len(conditions)-1].Message
		}
		val := PSMDBCluster{
			Name:    cluster.Name,
			Size:    cluster.Spec.Replsets[0].Size,
			State:   getReplicasetStatus(cluster),
			Pause:   cluster.Spec.Pause,
			Message: message,
			Replicaset: &Replicaset{
				DiskSize:         c.getDiskSize(cluster.Spec.Replsets[0].VolumeSpec),
				ComputeResources: c.getComputeResources(cluster.Spec.Replsets[0].Resources),
			},
		}

		res[i] = val
	}
	return res, nil
}

/*
  When a cluster is being initialized but there are not enough nodes to form a cluster (less than 3)
  the operator returns State=Error but that's not the real cluster state.
  While the cluster is being initialized, we need to return the lowest state value found in the
  replicaset list of members.
*/
func getReplicasetStatus(cluster psmdb.PerconaServerMongoDB) ClusterState {
	if strings.ToLower(string(cluster.Status.Status)) != string(psmdb.AppStateError) {
		return psmdbStatesMap[cluster.Status.Status]
	}

	if len(cluster.Status.Replsets) == 0 {
		return ClusterStateInvalid
	}

	// We shouldn't return ready state.
	status := ClusterStateFailed

	// We need to extract the lowest value so the first time, that's the lowest value.
	// Its is not possible to get the initial value in other way since cluster.Status.Replsets is a map
	// not an array.
	for _, replset := range cluster.Status.Replsets {
		replStatus := psmdbStatesMap[replset.Status]
		if replStatus < status {
			status = replStatus
		}
	}

	return status
}

// getDeletingXtraDBClusters returns Percona XtraDB clusters which are not fully deleted yet.
func (c *K8sClient) getDeletingPSMDBClusters(ctx context.Context, clusters []PSMDBCluster) ([]PSMDBCluster, error) {
	runningClusters := make(map[string]struct{}, len(clusters))
	for _, cluster := range clusters {
		runningClusters[cluster.Name] = struct{}{}
	}

	deletingClusters, err := c.getDeletingClusters(ctx, "percona-server-mongodb-operator", runningClusters)
	if err != nil {
		return nil, err
	}

	xtradbClusters := make([]PSMDBCluster, len(deletingClusters))
	for i, cluster := range deletingClusters {
		xtradbClusters[i] = PSMDBCluster{
			Name:       cluster.Name,
			Size:       0,
			State:      ClusterStateDeleting,
			Replicaset: new(Replicaset),
		}
	}
	return xtradbClusters, nil
}

func (c *K8sClient) getComputeResources(resources *common.PodResources) *ComputeResources {
	if resources == nil || resources.Limits == nil {
		return nil
	}
	res := new(ComputeResources)
	if resources.Limits.CPU != "" {
		res.CPUM = resources.Limits.CPU
	}
	if resources.Limits.Memory != "" {
		res.MemoryBytes = resources.Limits.Memory
	}
	return res
}

func (c *K8sClient) setComputeResources(res *ComputeResources) *common.PodResources {
	if res == nil {
		return nil
	}
	r := &common.PodResources{
		Limits: new(common.ResourcesList),
	}
	r.Limits.CPU = res.CPUM
	r.Limits.Memory = res.MemoryBytes
	return r
}

func (c *K8sClient) updateComputeResources(res *ComputeResources, podResources *common.PodResources) *common.PodResources {
	if res == nil {
		return podResources
	}
	if podResources == nil || podResources.Limits == nil {
		podResources = &common.PodResources{
			Limits: new(common.ResourcesList),
		}
	}

	podResources.Limits.CPU = res.CPUM
	podResources.Limits.Memory = res.MemoryBytes
	return podResources
}

func (c *K8sClient) getDiskSize(volumeSpec *common.VolumeSpec) string {
	if volumeSpec == nil || volumeSpec.PersistentVolumeClaim == nil {
		return "0"
	}
	quantity, ok := volumeSpec.PersistentVolumeClaim.Resources.Requests[common.ResourceStorage]
	if !ok {
		return "0"
	}
	return quantity
}

func (c *K8sClient) volumeSpec(diskSize string) *common.VolumeSpec {
	return &common.VolumeSpec{
		PersistentVolumeClaim: &common.PersistentVolumeClaimSpec{
			Resources: common.ResourceRequirements{
				Requests: common.ResourceList{
					common.ResourceStorage: diskSize,
				},
			},
		},
	}
}

// CheckOperators checks if operator installed and have required API version.
func (c *K8sClient) CheckOperators(ctx context.Context) (*Operators, error) {
	output, err := c.kubeCtl.Run(ctx, []string{"api-versions"}, "")
	if err != nil {
		return nil, errors.Wrap(err, "can't get api versions list")
	}

	apiVersions := strings.Split(string(output), "\n")

	return &Operators{
		Xtradb: c.checkOperatorStatus(apiVersions, pxcAPIVersion),
		Psmdb:  c.checkOperatorStatus(apiVersions, psmdbAPIVersion),
	}, nil
}

func (c *K8sClient) checkOperatorStatus(installedVersions []string, expectedAPIVersion string) (operator OperatorStatus) {
	apiNamespace := strings.Split(expectedAPIVersion, "/")[0]
	installed := false
	for _, version := range installedVersions {
		switch {
		case version == expectedAPIVersion:
			return OperatorStatusOK
		case strings.HasPrefix(version, apiNamespace):
			installed = true
		}
	}
	if installed {
		return OperatorStatusUnsupported
	}
	return OperatorStatusNotInstalled
}