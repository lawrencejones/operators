/*


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
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	rbacv1alpha1 "github.com/gocardless/theatre/apis/rbac/v1alpha1"
	rbacutils "github.com/gocardless/theatre/pkg/rbac"
	"github.com/gocardless/theatre/pkg/recutil"
)

// DirectoryRoleBindingReconciler reconciles a DirectoryRoleBinding object
type DirectoryRoleBindingReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	ctx             context.Context
	provider        DirectoryProvider
	refreshInterval time.Duration
}

// +kubebuilder:rbac:groups=rbac.crd.gocardless.com,resources=directoryrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.crd.gocardless.com,resources=directoryrolebindings/status,verbs=get;update;patch

func (r *DirectoryRoleBindingReconciler) ReconcileObject(req ctrl.Request) (ctrl.Result, error) {
	var err error
	logger := r.Log.WithValues("directoryrolebinding", req.NamespacedName)

	var drb rbacv1alpha1.DirectoryRoleBinding
	err = r.Get(r.ctx, req.NamespacedName, &drb)
	if err != nil {

	}

	rb := &rbacv1.RoleBinding{}
	err = r.Get(r.ctx, req.NamespacedName, rb)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get DirectoryRoleBinding: %w", err)
		}

		rb = &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rb.Name,
				Namespace: drb.Namespace,
				Labels:    drb.Labels,
			},
			RoleRef:  drb.Spec.RoleRef,
			Subjects: []rbacv1.Subject{},
		}

		if err := controllerutil.SetControllerReference(drb, rb, scheme.Scheme); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to set controller reference")
		}

		if err = r.Create(r.ctx, rb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create RoleBinding %w", err)
		}

		// logger.Log("event", EventRoleBindingCreated, "msg", fmt.Sprintf(
		// 	"Created RoleBinding: %s", identifier,
		// ))
	}

	subjects, err := r.resolve(drb.Spec.Subjects)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to resolve subjects")
	}

	add, remove := rbacutils.Diff(subjects, rb.Subjects), rbacutils.Diff(rb.Subjects, subjects)
	if len(add) > 0 || len(remove) > 0 {
		// logger.Log("event", EventSubjectsModified, "add", len(add), "remove", len(remove), "msg", fmt.Sprintf(
		// 	"Modifying subject list, adding %d and removing %d", len(add), len(remove),
		// ))

		// for _, member := range add {
		// 	logging.WithNoRecord(logger).Log("event", EventSubjectAdd, "subject", member.Name)
		// }

		// for _, member := range remove {
		// 	logging.WithNoRecord(logger).Log("event", EventSubjectRemove, "subject", member.Name)
		// }

		rb.Subjects = subjects
		if err := r.Update(r.ctx, rb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update RoleBinding: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: r.refreshInterval}, nil
}

func (r *DirectoryRoleBindingReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(&controller.Options{
			Reconciler: recutil.ResolveAndReconcile(
				ctx, mgr, &rbacv1alpha1.DirectoryRoleBinding{},
				func(request reconcile.Request, obj runtime.Object) (reconcile.Result, error) {
					return r.ReconcileObject(request, obj.(*rbacv1alpha1.DirectoryRoleBinding))
				},
			),
		}).
		For(&rbacv1alpha1.DirectoryRoleBinding{}).
		Watches(
			&source.Kind{Type: &rbacv1.RoleBinding{}},
			&handler.EnqueueRequestForOwner{
				IsController: true,
				OwnerType:    &rbacv1alpha1.DirectoryRoleBinding{},
			},
		).
		Complete(r)
}