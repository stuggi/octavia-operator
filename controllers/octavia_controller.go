/*
Copyright 2022.

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

package controllers

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	redisv1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/deployment"
	"github.com/openstack-k8s-operators/lib-common/modules/common/env"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/job"
	"github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	nad "github.com/openstack-k8s-operators/lib-common/modules/common/networkattachment"
	common_rbac "github.com/openstack-k8s-operators/lib-common/modules/common/rbac"
	"github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	oko_secret "github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	mariadbv1 "github.com/openstack-k8s-operators/mariadb-operator/api/v1beta1"
	octaviav1 "github.com/openstack-k8s-operators/octavia-operator/api/v1beta1"
	"github.com/openstack-k8s-operators/octavia-operator/pkg/octavia"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// OctaviaReconciler reconciles an Octavia object
type OctaviaReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Log     logr.Logger
	Scheme  *runtime.Scheme
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *OctaviaReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("Octavia")
}

// +kubebuilder:rbac:groups=octavia.openstack.org,resources=octavias,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=octavia.openstack.org,resources=octavias/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=octavia.openstack.org,resources=octavias/finalizers,verbs=update
// +kubebuilder:rbac:groups=octavia.openstack.org,resources=octaviaapis,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=octavia.openstack.org,resources=octaviaapis/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=octavia.openstack.org,resources=octaviaapis/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=mariadb.openstack.org,resources=mariadbdatabases,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=mariadb.openstack.org,resources=mariadbdatabases/finalizers,verbs=update
// +kubebuilder:rbac:groups=mariadb.openstack.org,resources=mariadbaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mariadb.openstack.org,resources=mariadbaccounts/finalizers,verbs=update
// +kubebuilder:rbac:groups=keystone.openstack.org,resources=keystoneapis,verbs=get;list;watch;
// +kubebuilder:rbac:groups=keystone.openstack.org,resources=keystoneservices,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=keystone.openstack.org,resources=keystoneendpoints,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=redis.openstack.org,resources=redises,verbs=get;list;watch;create;update;patch;delete

// service account, role, rolebinding
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update
// service account permissions that are needed to grant permission to the above
// +kubebuilder:rbac:groups="security.openshift.io",resourceNames=anyuid;privileged,resources=securitycontextconstraints,verbs=use
// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete;get;list;patch;update;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Octavia object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *OctaviaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)

	// Fetch the Octavia instance
	instance := &octaviav1.Octavia{}
	err := r.Client.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			// For additional cleanup logic use finalizers. Return and don't requeue.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		Log.Error(err, fmt.Sprintf("could not fetch instance %s", instance.Name))
		return ctrl.Result{}, err
	}

	helper, err := helper.NewHelper(
		instance,
		r.Client,
		r.Kclient,
		r.Scheme,
		Log,
	)
	if err != nil {
		Log.Error(err, fmt.Sprintf("could not instantiate helper for instance %s", instance.Name))
		return ctrl.Result{}, err
	}

	// initialize status if Conditions is nil, but do not reset if it already
	// exists
	isNewInstance := instance.Status.Conditions == nil
	if isNewInstance {
		instance.Status.Conditions = condition.Conditions{}
	}

	// Save a copy of the condtions so that we can restore the LastTransitionTime
	// when a condition's state doesn't change.
	savedConditions := instance.Status.Conditions.DeepCopy()

	// Always patch the instance status when exiting this function so we can
	// persist any changes.
	defer func() {
		condition.RestoreLastTransitionTimes(
			&instance.Status.Conditions, savedConditions)
		if instance.Status.Conditions.IsUnknown(condition.ReadyCondition) {
			instance.Status.Conditions.Set(
				instance.Status.Conditions.Mirror(condition.ReadyCondition))
		}
		err := helper.PatchInstance(ctx, instance)
		if err != nil {
			_err = err
			return
		}
	}()

	//
	// initialize status
	//

	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(condition.DBReadyCondition, condition.InitReason, condition.DBReadyInitMessage),
		condition.UnknownCondition(condition.DBSyncReadyCondition, condition.InitReason, condition.DBSyncReadyInitMessage),
		condition.UnknownCondition(condition.RabbitMqTransportURLReadyCondition, condition.InitReason, condition.RabbitMqTransportURLReadyInitMessage),
		condition.UnknownCondition(condition.InputReadyCondition, condition.InitReason, condition.InputReadyInitMessage),
		condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
		condition.UnknownCondition(condition.ServiceAccountReadyCondition, condition.InitReason, condition.ServiceAccountReadyInitMessage),
		condition.UnknownCondition(condition.RoleReadyCondition, condition.InitReason, condition.RoleReadyInitMessage),
		condition.UnknownCondition(condition.RoleBindingReadyCondition, condition.InitReason, condition.RoleBindingReadyInitMessage),
		condition.UnknownCondition(octaviav1.OctaviaAPIReadyCondition, condition.InitReason, octaviav1.OctaviaAPIReadyInitMessage),
		condition.UnknownCondition(condition.NetworkAttachmentsReadyCondition, condition.InitReason, condition.NetworkAttachmentsReadyInitMessage),
		condition.UnknownCondition(condition.ExposeServiceReadyCondition, condition.InitReason, condition.ExposeServiceReadyInitMessage),
		condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
		amphoraControllerInitCondition(octaviav1.HealthManager),
		amphoraControllerInitCondition(octaviav1.Housekeeping),
		amphoraControllerInitCondition(octaviav1.Worker),
	)

	instance.Status.Conditions.Init(&cl)

	// If we're not deleting this and the service object doesn't have our finalizer, add it.
	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) || isNewInstance {
		return ctrl.Result{}, nil
	}

	if instance.Status.Hash == nil {
		instance.Status.Hash = map[string]string{}
	}

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, instance, helper)
}

// fields to index to reconcile when change
const (
	passwordSecretField     = ".spec.secret"
	caBundleSecretNameField = ".spec.tls.caBundleSecretName"
	tlsAPIInternalField     = ".spec.tls.api.internal.secretName"
	tlsAPIPublicField       = ".spec.tls.api.public.secretName"
)

var (
	allWatchFields = []string{
		passwordSecretField,
		caBundleSecretNameField,
		tlsAPIInternalField,
		tlsAPIPublicField,
	}
)

// SetupWithManager sets up the controller with the Manager.
func (r *OctaviaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&octaviav1.Octavia{}).
		Owns(&mariadbv1.MariaDBDatabase{}).
		Owns(&mariadbv1.MariaDBAccount{}).
		Owns(&octaviav1.OctaviaAPI{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&corev1.Service{}).
		Owns(&rabbitmqv1.TransportURL{}).
		Owns(&redisv1.Redis{}).
		Complete(r)
}

func (r *OctaviaReconciler) reconcileDelete(ctx context.Context, instance *octaviav1.Octavia, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	util.LogForObject(helper, "Reconciling Service delete", instance)

	// remove db finalizer first
	octaviaDb, err := mariadbv1.GetDatabaseByNameAndAccount(ctx, helper, octavia.DatabaseCRName, instance.Spec.DatabaseAccount, instance.Namespace)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if !k8s_errors.IsNotFound(err) {
		if err := octaviaDb.DeleteFinalizer(ctx, helper); err != nil {
			return ctrl.Result{}, err
		}
	}

	persistenceDb, err := mariadbv1.GetDatabaseByNameAndAccount(ctx, helper, octavia.PersistenceDatabaseCRName, instance.Spec.PersistenceDatabaseAccount, instance.Namespace)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if !k8s_errors.IsNotFound(err) {
		if err := persistenceDb.DeleteFinalizer(ctx, helper); err != nil {
			return ctrl.Result{}, err
		}
	}

	// We did all the cleanup on the objects we created so we can remove the
	// finalizer from ourselves to allow the deletion
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	Log.Info(fmt.Sprintf("Reconciled Service '%s' delete successfully", instance.Name))

	util.LogForObject(helper, "Reconciled Service delete successfully", instance)
	return ctrl.Result{}, nil
}

func (r *OctaviaReconciler) reconcileInit(
	ctx context.Context,
	instance *octaviav1.Octavia,
	helper *helper.Helper,
	serviceLabels map[string]string,
	serviceAnnotations map[string]string,
) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service init")

	// ConfigMap
	configMapVars := make(map[string]env.Setter)

	//
	// check for required OpenStack secret holding passwords for service/admin user and add hash to the vars map
	//
	ospSecret, hash, err := oko_secret.GetSecret(ctx, helper, instance.Spec.Secret, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.InputReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				condition.InputReadyWaitingMessage))
			return ctrl.Result{RequeueAfter: time.Second * 10}, fmt.Errorf("OpenStack secret %s not found", instance.Spec.Secret)
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.InputReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	configMapVars[ospSecret.Name] = env.SetValue(hash)

	transportURLSecret, hash, err := oko_secret.GetSecret(ctx, helper, instance.Status.TransportURLSecret, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.InputReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				condition.InputReadyWaitingMessage))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, fmt.Errorf("TransportURL secret %s not found", instance.Status.TransportURLSecret)
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.InputReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	configMapVars[transportURLSecret.Name] = env.SetValue(hash)

	octaviaDb, persistenceDb, result, err := r.ensureDB(ctx, helper, instance)
	if err != nil {
		return ctrl.Result{}, err
	} else if (result != ctrl.Result{}) {
		return result, nil
	}

	//
	// create Configmap required for octavia input
	// - %-scripts configmap holding scripts to e.g. bootstrap the service
	// - %-config configmap holding minimal octavia config required to get the service up, user can add additional files to be added to the service
	// - parameters which has passwords gets added from the OpenStack secret via the init container
	//
	err = r.generateServiceConfigMaps(ctx, instance, helper, &configMapVars, octaviaDb, persistenceDb)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	//
	// create hash over all the different input resources to identify if any those changed
	// and a restart/recreate is required.
	//
	_, hashChanged, err := r.createHashOfInputHashes(ctx, instance, configMapVars)
	if err != nil {
		return ctrl.Result{}, err
	} else if hashChanged {
		// Hash changed and instance status should be updated (which will be done by main defer func),
		// so we need to return and reconcile again
		return ctrl.Result{}, nil
	}
	// Create Secrets - end

	instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.ServiceConfigReadyMessage)

	//
	// run octavia db sync
	//
	dbSyncHash := instance.Status.Hash[octaviav1.DbSyncHash]
	jobDef := octavia.DbSyncJob(instance, serviceLabels, serviceAnnotations)
	Log.Info("Initializing db sync job")
	dbSyncjob := job.NewJob(
		jobDef,
		octaviav1.DbSyncHash,
		instance.Spec.PreserveJobs,
		time.Duration(5)*time.Second,
		dbSyncHash,
	)
	ctrlResult, err := dbSyncjob.DoJob(
		ctx,
		helper,
	)
	if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DBSyncReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.DBSyncReadyRunningMessage))
		return ctrlResult, nil
	}
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DBSyncReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.DBSyncReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	if dbSyncjob.HasChanged() {
		instance.Status.Hash[octaviav1.DbSyncHash] = dbSyncjob.GetHash()
	}
	instance.Status.Conditions.MarkTrue(condition.DBSyncReadyCondition, condition.DBSyncReadyMessage)

	// run octavia db sync - end

	Log.Info("Reconciled Service init successfully")
	return ctrl.Result{}, nil
}

func (r *OctaviaReconciler) reconcileUpdate(ctx context.Context, instance *octaviav1.Octavia, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service update")

	// TODO: should have minor update tasks if required
	// - delete dbsync hash from status to rerun it?

	Log.Info("Reconciled Service update successfully")
	return ctrl.Result{}, nil
}

func (r *OctaviaReconciler) reconcileUpgrade(ctx context.Context, instance *octaviav1.Octavia, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service upgrade")

	// TODO: should have major version upgrade tasks
	// -delete dbsync hash from status to rerun it?

	Log.Info("Reconciled Service upgrade successfully")
	return ctrl.Result{}, nil
}

func (r *OctaviaReconciler) reconcileNormal(ctx context.Context, instance *octaviav1.Octavia, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service")

	// Service account, role, binding
	rbacRules := []rbacv1.PolicyRule{
		{
			APIGroups:     []string{"security.openshift.io"},
			ResourceNames: []string{"anyuid", "privileged"},
			Resources:     []string{"securitycontextconstraints"},
			Verbs:         []string{"use"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods"},
			Verbs:     []string{"create", "get", "list", "watch", "update", "patch", "delete"},
		},
	}
	rbacResult, err := common_rbac.ReconcileRbac(ctx, helper, instance, rbacRules)
	if err != nil {
		return rbacResult, err
	} else if (rbacResult != ctrl.Result{}) {
		return rbacResult, nil
	}

	transportURL, op, err := r.transportURLCreateOrUpdate(instance)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.RabbitMqTransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.RabbitMqTransportURLReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	if op != controllerutil.OperationResultNone {
		Log.Info(fmt.Sprintf("TransportURL %s successfully reconciled - operation: %s", transportURL.Name, string(op)))
	}

	instance.Status.TransportURLSecret = transportURL.Status.SecretName

	if instance.Status.TransportURLSecret == "" {
		Log.Info(fmt.Sprintf("Waiting for the TransportURL %s secret to be created", transportURL.Name))
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.InputReadyWaitingMessage))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}
	instance.Status.Conditions.MarkTrue(condition.RabbitMqTransportURLReadyCondition, condition.RabbitMqTransportURLReadyMessage)

	err = octavia.EnsureAmphoraCerts(ctx, instance, helper, &Log)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	if err = octavia.EnsureQuotas(ctx, instance, &r.Log, helper); err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.InputReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	instance.Status.Conditions.MarkTrue(condition.InputReadyCondition, condition.InputReadyMessage)

	//
	// TODO check when/if Init, Update, or Upgrade should/could be skipped
	//

	serviceLabels := map[string]string{
		common.AppSelector: octavia.ServiceName,
	}

	for _, networkAttachment := range instance.Spec.OctaviaAPI.NetworkAttachments {
		_, err := nad.GetNADWithName(ctx, helper, networkAttachment, instance.Namespace)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.NetworkAttachmentsReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					condition.NetworkAttachmentsReadyWaitingMessage,
					networkAttachment))
				return ctrl.Result{RequeueAfter: time.Second * 10}, fmt.Errorf("network-attachment-definition %s not found", networkAttachment)
			}
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.NetworkAttachmentsReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.NetworkAttachmentsReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
	}

	serviceAnnotations, err := nad.CreateNetworksAnnotation(instance.Namespace, instance.Spec.OctaviaAPI.NetworkAttachments)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed create network annotation from %s: %w",
			instance.Spec.OctaviaAPI.NetworkAttachments, err)
	}
	instance.Status.Conditions.MarkTrue(condition.NetworkAttachmentsReadyCondition, condition.NetworkAttachmentsReadyMessage)

	// Handle service init
	ctrlResult, err := r.reconcileInit(ctx, instance, helper, serviceLabels, serviceAnnotations)
	if err != nil {
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		return ctrlResult, nil
	}
	instance.Status.Conditions.MarkTrue(condition.NetworkAttachmentsReadyCondition, condition.NetworkAttachmentsReadyMessage)

	// Handle service update
	ctrlResult, err = r.reconcileUpdate(ctx, instance, helper)
	if err != nil {
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		return ctrlResult, nil
	}

	// Handle service upgrade
	ctrlResult, err = r.reconcileUpgrade(ctx, instance, helper)
	if err != nil {
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		return ctrlResult, nil
	}

	Log.Info(fmt.Sprintf("Calling for deploy for API with %s", instance.Status.DatabaseHostname))

	// TODO(beagles): look into adding condition types/messages in a common file
	octaviaAPI, op, err := r.apiDeploymentCreateOrUpdate(instance)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			octaviav1.OctaviaAPIReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			octaviav1.OctaviaAPIReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		Log.Info(fmt.Sprintf("Deployment %s successfully reconciled - operation: %s", instance.Name, string(op)))
	}

	// Mirror OctaviaAPI status' ReadyCount to this parent CR
	// TODO(beagles): We need to have a way to aggregate conditions from the other services into this
	//
	instance.Status.OctaviaAPIReadyCount = octaviaAPI.Status.ReadyCount
	conditionStatus := octaviaAPI.Status.Conditions.Mirror(octaviav1.OctaviaAPIReadyCondition)
	if conditionStatus != nil {
		instance.Status.Conditions.Set(conditionStatus)
	} else {
		instance.Status.Conditions.MarkTrue(octaviav1.OctaviaAPIReadyCondition, condition.DeploymentReadyMessage)
	}

	// ------------------------------------------------------------------------------------------------------------
	// Amphora reconciliation
	// ------------------------------------------------------------------------------------------------------------

	// Create load balancer management network and get its Id (networkInfo is actually a struct and contains
	// multiple details.
	networkInfo, err := octavia.EnsureAmphoraManagementNetwork(
		ctx,
		instance.Namespace,
		instance.Spec.TenantName,
		&instance.Spec.LbMgmtNetworks,
		&Log,
		helper,
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	Log.Info(fmt.Sprintf("Using management network \"%s\"", networkInfo.TenantNetworkID))

	octaviaHealthManager, op, err := r.amphoraControllerDaemonSetCreateOrUpdate(instance, networkInfo,
		instance.Spec.OctaviaHealthManager, octaviav1.HealthManager)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			amphoraControllerReadyCondition(octaviav1.HealthManager),
			condition.ErrorReason,
			condition.SeverityWarning,
			amphoraControllerErrorMessage(octaviav1.HealthManager),
			err.Error()))
		return ctrl.Result{}, err
	}

	if op != controllerutil.OperationResultNone {
		Log.Info(fmt.Sprintf("Deployment of OctaviaHealthManager for %s successfully reconciled - operation: %s", instance.Name, string(op)))
	}

	instance.Status.OctaviaHealthManagerReadyCount = octaviaHealthManager.Status.ReadyCount
	conditionStatus = octaviaHealthManager.Status.Conditions.Mirror(amphoraControllerReadyCondition(octaviav1.HealthManager))
	if conditionStatus != nil {
		instance.Status.Conditions.Set(conditionStatus)
	} else {
		instance.Status.Conditions.MarkTrue(amphoraControllerReadyCondition(octaviav1.HealthManager), condition.DeploymentReadyMessage)
	}

	//
	// We do not try and reconcile the other controller PODs until after the health manager Pods are all deployed.
	//
	if octaviaHealthManager.Status.ReadyCount != octaviaHealthManager.Status.DesiredNumberScheduled {
		Log.Info("Health managers are not ready. Housekeeping and Worker services pending")
		return ctrl.Result{}, nil
	}

	// Skip the other amphora controller pods until the health managers are all up and running.
	octaviaHousekeeping, op, err := r.amphoraControllerDaemonSetCreateOrUpdate(instance, networkInfo,
		instance.Spec.OctaviaHousekeeping, octaviav1.Housekeeping)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			amphoraControllerReadyCondition(octaviav1.Housekeeping),
			condition.ErrorReason,
			condition.SeverityWarning,
			amphoraControllerErrorMessage(octaviav1.Housekeeping),
			err.Error()))
		return ctrl.Result{}, err
	}

	if op != controllerutil.OperationResultNone {
		Log.Info(fmt.Sprintf("Deployment of OctaviaHousekeeping for %s successfully reconciled - operation: %s", instance.Name, string(op)))
	}

	instance.Status.OctaviaHousekeepingReadyCount = octaviaHousekeeping.Status.ReadyCount
	conditionStatus = octaviaHousekeeping.Status.Conditions.Mirror(amphoraControllerReadyCondition(octaviav1.Housekeeping))
	if conditionStatus != nil {
		instance.Status.Conditions.Set(conditionStatus)
	} else {
		instance.Status.Conditions.MarkTrue(amphoraControllerReadyCondition(octaviav1.Housekeeping), condition.DeploymentReadyMessage)
	}

	octaviaWorker, op, err := r.amphoraControllerDaemonSetCreateOrUpdate(instance, networkInfo,
		instance.Spec.OctaviaWorker, octaviav1.Worker)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			amphoraControllerReadyCondition(octaviav1.Worker),
			condition.ErrorReason,
			condition.SeverityWarning,
			amphoraControllerErrorMessage(octaviav1.Worker),
			err.Error()))
		return ctrl.Result{}, err
	}

	if op != controllerutil.OperationResultNone {
		Log.Info(fmt.Sprintf("Deployment of OctaviaWorker for %s successfully reconciled - operation: %s", instance.Name, string(op)))
	}

	instance.Status.OctaviaWorkerReadyCount = octaviaWorker.Status.ReadyCount
	conditionStatus = octaviaWorker.Status.Conditions.Mirror(amphoraControllerReadyCondition(octaviav1.Worker))
	if conditionStatus != nil {
		instance.Status.Conditions.Set(conditionStatus)
	} else {
		instance.Status.Conditions.MarkTrue(amphoraControllerReadyCondition(octaviav1.Worker), condition.DeploymentReadyMessage)
	}

	// remove finalizers from unused MariaDBAccount records
	err = mariadbv1.DeleteUnusedMariaDBAccountFinalizers(ctx, helper, octavia.DatabaseCRName, instance.Spec.DatabaseAccount, instance.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = mariadbv1.DeleteUnusedMariaDBAccountFinalizers(ctx, helper, octavia.PersistenceDatabaseCRName, instance.Spec.PersistenceDatabaseAccount, instance.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Amphora SSH key config for debugging
	err = octavia.EnsureAmpSSHConfig(ctx, instance, helper, &Log)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctrlResult, err = r.reconcileAmphoraImages(ctx, instance, helper)
	if (ctrlResult != ctrl.Result{}) {
		return ctrlResult, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	// create Deployment - end

	// Update the lastObserved generation before evaluating conditions
	instance.Status.ObservedGeneration = instance.Generation
	// We reached the end of the Reconcile, update the Ready condition based on
	// the sub conditions
	if instance.Status.Conditions.AllSubConditionIsTrue() {
		instance.Status.Conditions.MarkTrue(
			condition.ReadyCondition, condition.ReadyMessage)
	}
	Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

// ensureDB - set up the main database and the "persistence" database.
// this then drives the ability to generate the config
func (r *OctaviaReconciler) ensureDB(
	ctx context.Context,
	h *helper.Helper,
	instance *octaviav1.Octavia,
) (*mariadbv1.Database, *mariadbv1.Database, ctrl.Result, error) {

	// ensure MariaDBAccount exists.  This account record may be created by
	// openstack-operator or the cloud operator up front without a specific
	// MariaDBDatabase configured yet.   Otherwise, a MariaDBAccount CR is
	// created here with a generated username as well as a secret with
	// generated password.   The MariaDBAccount is created without being
	// yet associated with any MariaDBDatabase.

	_, _, err := mariadbv1.EnsureMariaDBAccount(
		ctx, h, instance.Spec.DatabaseAccount,
		instance.Namespace, false, octavia.DatabaseUsernamePrefix,
	)

	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			mariadbv1.MariaDBAccountReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			mariadbv1.MariaDBAccountNotReadyMessage,
			err.Error()))

		return nil, nil, ctrl.Result{}, err
	}

	_, _, err = mariadbv1.EnsureMariaDBAccount(
		ctx, h, instance.Spec.PersistenceDatabaseAccount,
		instance.Namespace, false, octavia.DatabaseUsernamePrefix,
	)

	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			mariadbv1.MariaDBAccountReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			mariadbv1.MariaDBAccountNotReadyMessage,
			err.Error()))

		return nil, nil, ctrl.Result{}, err
	}
	instance.Status.Conditions.MarkTrue(
		mariadbv1.MariaDBAccountReadyCondition,
		mariadbv1.MariaDBAccountReadyMessage)

	//
	// create service DB instance
	//
	octaviaDb := mariadbv1.NewDatabaseForAccount(
		instance.Spec.DatabaseInstance, // mariadb/galera service to target
		octavia.DatabaseName,           // name used in CREATE DATABASE in mariadb
		octavia.DatabaseCRName,         // CR name for MariaDBDatabase
		instance.Spec.DatabaseAccount,  // CR name for MariaDBAccount
		instance.Namespace,             // namespace
	)

	persistenceDb := mariadbv1.NewDatabaseForAccount(
		instance.Spec.DatabaseInstance,           // mariadb/galera service to target
		octavia.PersistenceDatabaseName,          // name used in CREATE DATABASE in mariadb
		octavia.PersistenceDatabaseCRName,        // CR name for MariaDBDatabase
		instance.Spec.PersistenceDatabaseAccount, // CR name for MariaDBAccount
		instance.Namespace,                       // namespace
	)

	dbs := []*mariadbv1.Database{octaviaDb, persistenceDb}

	for _, db := range dbs {
		// create or patch the DB
		ctrlResult, err := db.CreateOrPatchAll(ctx, h)

		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.DBReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.DBReadyErrorMessage,
				err.Error()))
			return octaviaDb, persistenceDb, ctrl.Result{}, err
		}
		if (ctrlResult != ctrl.Result{}) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.DBReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				condition.DBReadyRunningMessage))
			return octaviaDb, persistenceDb, ctrlResult, nil
		}

		// wait for the DB to be setup
		ctrlResult, err = db.WaitForDBCreated(ctx, h)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.DBReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.DBReadyErrorMessage,
				err.Error()))
			return octaviaDb, persistenceDb, ctrlResult, err
		}
		if (ctrlResult != ctrl.Result{}) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.DBReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				condition.DBReadyRunningMessage))
			return octaviaDb, persistenceDb, ctrlResult, nil
		}
	}

	// update Status.DatabaseHostname, used to bootstrap/config the service
	instance.Status.DatabaseHostname = dbs[0].GetDatabaseHostname()
	instance.Status.Conditions.MarkTrue(condition.DBReadyCondition, condition.DBReadyMessage)

	return octaviaDb, persistenceDb, ctrl.Result{}, nil

	// create service DB - end
}

func (r *OctaviaReconciler) reconcileAmphoraImages(
	ctx context.Context,
	instance *octaviav1.Octavia,
	helper *helper.Helper,
) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	if instance.Spec.AmphoraImageContainerImage == "" {
		if instance.Status.Hash[octaviav1.ImageUploadHash] != "" {
			Log.Info("Reseting image upload hash")
			instance.Status.Hash[octaviav1.ImageUploadHash] = ""
		}
		return ctrl.Result{}, nil
	}

	hash, err := util.ObjectHash(instance.Spec.AmphoraImageContainerImage)
	if err != nil {
		return ctrl.Result{}, err
	}
	if hash == instance.Status.Hash[octaviav1.ImageUploadHash] {
		// No change
		return ctrl.Result{}, nil
	}

	serviceLabels := map[string]string{
		common.AppSelector: octavia.ServiceName + "-image",
	}

	Log.Info("Initializing amphora image upload deployment")
	depl := deployment.NewDeployment(
		octavia.ImageUploadDeployment(instance, serviceLabels),
		time.Duration(5)*time.Second,
	)
	ctrlResult, err := depl.CreateOrPatch(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.DeploymentReadyErrorMessage,
			err.Error()))
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.DeploymentReadyRunningMessage))
		return ctrlResult, nil
	}

	readyCount := depl.GetDeployment().Status.ReadyReplicas
	if readyCount == 0 {
		// Not ready, wait for the next loop
		Log.Info("Image Upload Pod not ready")
		return ctrl.Result{Requeue: true, RequeueAfter: 1 * time.Second}, nil
	}
	instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)

	exportLabels := util.MergeStringMaps(
		serviceLabels,
		map[string]string{
			service.AnnotationEndpointKey: "internal",
		},
	)

	svc, err := service.NewService(
		service.GenericService(&service.GenericServiceDetails{
			Name:      "octavia-image-upload-internal",
			Namespace: instance.Namespace,
			Labels:    exportLabels,
			Selector:  serviceLabels,
			Ports: []corev1.ServicePort{
				{
					Name:       "octavia-image-upload-internal",
					Port:       octavia.ApacheInternalPort,
					TargetPort: intstr.FromInt(8080),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		}),
		5,
		nil,
	)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ExposeServiceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ExposeServiceReadyErrorMessage,
			err.Error()))

		return ctrl.Result{}, err
	}
	svc.AddAnnotation(map[string]string{
		service.AnnotationEndpointKey: "internal",
	})
	svc.AddAnnotation(map[string]string{
		service.AnnotationIngressCreateKey: "false",
	})

	ctrlResult, err = svc.CreateOrPatch(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ExposeServiceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ExposeServiceReadyErrorMessage,
			err.Error()))

		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ExposeServiceReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.ExposeServiceReadyRunningMessage))
		return ctrlResult, nil
	}
	endpoint, err := svc.GetAPIEndpoint(nil, nil, "")
	if err != nil {
		return ctrl.Result{}, err
	}
	instance.Status.Conditions.MarkTrue(condition.ExposeServiceReadyCondition, condition.ExposeServiceReadyMessage)

	urlMap, err := r.getLocalImageURLs(ctx, helper, endpoint)
	if err != nil {
		Log.Info(fmt.Sprintf("Cannot get amphora image list: %s", err))
		return ctrl.Result{Requeue: true, RequeueAfter: 1 * time.Second}, err
	}

	ok, err := octavia.EnsureAmphoraImages(ctx, instance, &r.Log, helper, urlMap)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		// Images are not ready
		Log.Info("Waiting for amphora images to be ready")
		return ctrl.Result{Requeue: true, RequeueAfter: 5 * time.Second}, nil
	}
	Log.Info(fmt.Sprintf("Setting image upload hash - %s", hash))
	instance.Status.Hash[octaviav1.ImageUploadHash] = hash

	// Tasks are successfull, the deployment can be deleted
	Log.Info("Deleting amphora image upload deployment")
	depl.Delete(ctx, helper)

	return ctrl.Result{}, nil
}

func (r *OctaviaReconciler) getLocalImageURLs(
	ctx context.Context,
	helper *helper.Helper,
	endpoint string,
) ([]octavia.OctaviaAmphoraImage, error) {
	// Get the list of images and their hashes
	listUrl := fmt.Sprintf("%s/octavia-amphora-images.sha256sum", endpoint)

	resp, err := http.Get(listUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	ret := []octavia.OctaviaAmphoraImage{}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 {
			name, _ := strings.CutSuffix(fields[1], ".qcow2")
			ret = append(ret, octavia.OctaviaAmphoraImage{
				Name:     name,
				URL:      fmt.Sprintf("%s/%s", endpoint, fields[1]),
				Checksum: fields[0],
			})
		}
	}

	return ret, nil
}

// generateServiceConfigMaps - create create configmaps which hold scripts and service configuration
// TODO add DefaultConfigOverwrite
func (r *OctaviaReconciler) generateServiceConfigMaps(
	ctx context.Context,
	instance *octaviav1.Octavia,
	h *helper.Helper,
	envVars *map[string]env.Setter,
	octaviaDb *mariadbv1.Database,
	persistenceDb *mariadbv1.Database,
) error {
	//
	// create Configmap/Secret required for octavia input
	// - %-scripts configmap holding scripts to e.g. bootstrap the service
	// - %-config configmap holding minimal octavia config required to get the service up, user can add additional files to be added to the service
	// - parameters which has passwords gets added from the ospSecret via the init container
	//

	cmLabels := labels.GetLabels(instance, labels.GetGroupLabel(octavia.ServiceName), map[string]string{})

	var tlsCfg *tls.Service
	if instance.Spec.OctaviaAPI.TLS.Ca.CaBundleSecretName != "" {
		tlsCfg = &tls.Service{}
	}

	// customData hold any customization for the service.
	// custom.conf is going to /etc/<service>/<service>.conf.d
	// all other files get placed into /etc/<service> to allow overwrite of e.g. logging.conf or policy.json
	// TODO: make sure custom.conf can not be overwritten
	customData := map[string]string{
		common.CustomServiceConfigFileName: instance.Spec.CustomServiceConfig,
		"my.cnf":                           octaviaDb.GetDatabaseClientConfig(tlsCfg), //(mschuppert) for now just get the default my.cnf
	}
	for key, data := range instance.Spec.DefaultConfigOverwrite {
		customData[key] = data
	}

	databaseAccount := octaviaDb.GetAccount()
	dbSecret := octaviaDb.GetSecret()
	persistenceDatabaseAccount := persistenceDb.GetAccount()
	persistenceDbSecret := persistenceDb.GetSecret()

	// We only need a minimal 00-config.conf that is only used by db-sync job,
	// hence only passing the database related parameters
	templateParameters := map[string]interface{}{
		"MinimalConfig": true, // This tells the template to generate a minimal config
		"DatabaseConnection": fmt.Sprintf("mysql+pymysql://%s:%s@%s/%s?read_default_file=/etc/my.cnf",
			databaseAccount.Spec.UserName,
			string(dbSecret.Data[mariadbv1.DatabasePasswordSelector]),
			instance.Status.DatabaseHostname,
			octavia.DatabaseName,
		),
		"PersistenceDatabaseConnection": fmt.Sprintf("mysql+pymysql://%s:%s@%s/%s?read_default_file=/etc/my.cnf",
			persistenceDatabaseAccount.Spec.UserName,
			string(persistenceDbSecret.Data[mariadbv1.DatabasePasswordSelector]),
			instance.Status.DatabaseHostname,
			octavia.PersistenceDatabaseName,
		),
	}
	templateParameters["ServiceUser"] = instance.Spec.ServiceUser

	cms := []util.Template{
		// ScriptsConfigMap
		{
			Name:               fmt.Sprintf("%s-scripts", instance.Name),
			Namespace:          instance.Namespace,
			Type:               util.TemplateTypeScripts,
			InstanceType:       instance.Kind,
			AdditionalTemplate: map[string]string{"common.sh": "/common/common.sh"},
			Labels:             cmLabels,
		},
		// ConfigMap
		{
			Name:          fmt.Sprintf("%s-config-data", instance.Name),
			Namespace:     instance.Namespace,
			Type:          util.TemplateTypeConfig,
			InstanceType:  instance.Kind,
			CustomData:    customData,
			ConfigOptions: templateParameters,
			Labels:        cmLabels,
		},
	}
	err := secret.EnsureSecrets(ctx, h, instance, cms, envVars)
	if err != nil {
		return err
	}

	return nil
}

// createHashOfInputHashes - creates a hash of hashes which gets added to the resources which requires a restart
// if any of the input resources change, like configs, passwords, ...
//
// returns the hash, whether the hash changed (as a bool) and any error
func (r *OctaviaReconciler) createHashOfInputHashes(
	ctx context.Context,
	instance *octaviav1.Octavia,
	envVars map[string]env.Setter,
) (string, bool, error) {
	Log := r.GetLogger(ctx)
	var hashMap map[string]string
	changed := false
	mergedMapVars := env.MergeEnvs([]corev1.EnvVar{}, envVars)
	hash, err := util.ObjectHash(mergedMapVars)
	if err != nil {
		return hash, changed, err
	}
	if hashMap, changed = util.SetHash(instance.Status.Hash, common.InputHashName, hash); changed {
		instance.Status.Hash = hashMap
		Log.Info(fmt.Sprintf("Input maps hash %s - %s", common.InputHashName, hash))
	}
	return hash, changed, nil
}

func (r *OctaviaReconciler) apiDeploymentCreateOrUpdate(instance *octaviav1.Octavia) (*octaviav1.OctaviaAPI, controllerutil.OperationResult, error) {
	deployment := &octaviav1.OctaviaAPI{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-api", instance.Name),
			Namespace: instance.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(context.TODO(), r.Client, deployment, func() error {
		deployment.Spec = instance.Spec.OctaviaAPI
		deployment.Spec.DatabaseInstance = instance.Spec.DatabaseInstance
		deployment.Spec.DatabaseHostname = instance.Status.DatabaseHostname
		deployment.Spec.DatabaseAccount = instance.Spec.DatabaseAccount
		deployment.Spec.PersistenceDatabaseAccount = instance.Spec.PersistenceDatabaseAccount
		deployment.Spec.ServiceUser = instance.Spec.ServiceUser
		deployment.Spec.TransportURLSecret = instance.Status.TransportURLSecret
		deployment.Spec.Secret = instance.Spec.Secret
		deployment.Spec.ServiceAccount = instance.RbacResourceName()
		deployment.Spec.TLS = instance.Spec.OctaviaAPI.TLS
		if len(deployment.Spec.NodeSelector) == 0 {
			deployment.Spec.NodeSelector = instance.Spec.NodeSelector
		}
		err := controllerutil.SetControllerReference(instance, deployment, r.Scheme)
		if err != nil {
			return err
		}
		return nil
	})

	return deployment, op, err
}

func (r *OctaviaReconciler) transportURLCreateOrUpdate(
	instance *octaviav1.Octavia,
) (*rabbitmqv1.TransportURL,
	controllerutil.OperationResult, error) {
	transportURL := &rabbitmqv1.TransportURL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-octavia-transport", instance.Name),
			Namespace: instance.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(context.TODO(), r.Client, transportURL, func() error {
		transportURL.Spec.RabbitmqClusterName = instance.Spec.RabbitMqClusterName
		err := controllerutil.SetControllerReference(instance, transportURL, r.Scheme)
		return err
	})
	return transportURL, op, err
}

func (r *OctaviaReconciler) amphoraControllerDaemonSetCreateOrUpdate(
	instance *octaviav1.Octavia,
	networkInfo octavia.NetworkProvisioningSummary,
	controllerSpec octaviav1.OctaviaAmphoraControllerSpec,
	role string,
) (*octaviav1.OctaviaAmphoraController,
	controllerutil.OperationResult, error) {

	daemonset := &octaviav1.OctaviaAmphoraController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", instance.Name, role),
			Namespace: instance.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(context.TODO(), r.Client, daemonset, func() error {
		daemonset.Spec = controllerSpec
		daemonset.Spec.Role = role
		daemonset.Spec.DatabaseInstance = instance.Spec.DatabaseInstance
		daemonset.Spec.DatabaseHostname = instance.Status.DatabaseHostname
		daemonset.Spec.DatabaseAccount = instance.Spec.DatabaseAccount
		daemonset.Spec.PersistenceDatabaseAccount = instance.Spec.PersistenceDatabaseAccount
		daemonset.Spec.ServiceUser = instance.Spec.ServiceUser
		daemonset.Spec.Secret = instance.Spec.Secret
		daemonset.Spec.TransportURLSecret = instance.Status.TransportURLSecret
		daemonset.Spec.ServiceAccount = instance.RbacResourceName()
		daemonset.Spec.LbMgmtNetworkID = networkInfo.TenantNetworkID
		daemonset.Spec.LbSecurityGroupID = networkInfo.SecurityGroupID
		daemonset.Spec.AmphoraCustomFlavors = instance.Spec.AmphoraCustomFlavors
		daemonset.Spec.TLS = instance.Spec.OctaviaAPI.TLS.Ca
		if len(daemonset.Spec.NodeSelector) == 0 {
			daemonset.Spec.NodeSelector = instance.Spec.NodeSelector
		}
		err := controllerutil.SetControllerReference(instance, daemonset, r.Scheme)
		if err != nil {
			return err
		}
		return nil
	})

	return daemonset, op, err
}

func amphoraControllerReadyCondition(role string) condition.Type {
	condMap := map[string]condition.Type{
		octaviav1.HealthManager: octaviav1.OctaviaHealthManagerReadyCondition,
		octaviav1.Housekeeping:  octaviav1.OctaviaHousekeepingReadyCondition,
		octaviav1.Worker:        octaviav1.OctaviaWorkerReadyCondition,
	}
	return condMap[role]
}

func amphoraControllerInitCondition(role string) *condition.Condition {
	condMap := map[string]*condition.Condition{
		octaviav1.HealthManager: condition.UnknownCondition(
			amphoraControllerReadyCondition(role),
			condition.InitReason,
			octaviav1.OctaviaHealthManagerReadyInitMessage),
		octaviav1.Housekeeping: condition.UnknownCondition(
			amphoraControllerReadyCondition(role),
			condition.InitReason,
			octaviav1.OctaviaHousekeepingReadyInitMessage),
		octaviav1.Worker: condition.UnknownCondition(
			amphoraControllerReadyCondition(role),
			condition.InitReason,
			octaviav1.OctaviaWorkerReadyInitMessage),
	}
	return condMap[role]
}

func amphoraControllerErrorMessage(role string) string {
	condMap := map[string]string{
		octaviav1.HealthManager: octaviav1.OctaviaHealthManagerReadyErrorMessage,
		octaviav1.Housekeeping:  octaviav1.OctaviaHousekeepingReadyErrorMessage,
		octaviav1.Worker:        octaviav1.OctaviaWorkerReadyErrorMessage,
	}
	return condMap[role]
}
