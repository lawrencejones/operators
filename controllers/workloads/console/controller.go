package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	rbacv1alpha1 "github.com/gocardless/theatre/v2/apis/rbac/v1alpha1"
	workloadsv1alpha1 "github.com/gocardless/theatre/v2/apis/workloads/v1alpha1"
	"github.com/gocardless/theatre/v2/pkg/logging"
	"github.com/gocardless/theatre/v2/pkg/recutil"
)

const (
	// Resource-level events

	EventDelete           = "Delete"
	EventSuccessfulCreate = "SuccessfulCreate"
	EventSuccessfulUpdate = "SuccessfulUpdate"
	EventNoCreateOrUpdate = "NoCreateOrUpdate"

	// Warning events

	EventUnknownOutcome       = "UnknownOutcome"
	EventInvalidSpecification = "InvalidSpecification"
	EventTemplateUnsupported  = "TemplateUnsupported"

	// Console log keys

	ConsolePendingAuthorisation = "ConsolePendingAuthorisation"
	ConsoleAuthorised           = "ConsoleAuthorised"
	ConsoleStarted              = "ConsoleStarted"
	ConsoleEnded                = "ConsoleEnded"
	ConsoleDestroyed            = "ConsoleDestroyed"

	Job                  = "job"
	Console              = "console"
	ConsoleAuthorisation = "consoleauthorisation"
	ConsoleTemplate      = "consoletemplate"
	Role                 = "role"
	DirectoryRoleBinding = "directoryrolebinding"

	DefaultTTLBeforeRunning = 1 * time.Hour
	DefaultTTLAfterFinished = 24 * time.Hour
)

type IgnoreCreatePredicate struct {
	predicate.Funcs
}

func (IgnoreCreatePredicate) Create(e event.CreateEvent) bool {
	return false
}

type ConsoleReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

func (r *ConsoleReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	logger := r.Log.WithValues("component", "Console")
	return ctrl.NewControllerManagedBy(mgr).
		For(&workloadsv1alpha1.Console{}).
		Watches(
			&source.Kind{Type: &workloadsv1alpha1.Console{}},
			&handler.EnqueueRequestForObject{},
		).
		Watches(
			&source.Kind{Type: &workloadsv1alpha1.ConsoleAuthorisation{}},
			&handler.EnqueueRequestForOwner{
				IsController: true,
				OwnerType:    &workloadsv1alpha1.Console{},
			},
			// Don't unnecessarily reconcile when the controller initially creates the
			// authorisation object.
			builder.WithPredicates(IgnoreCreatePredicate{}),
		).
		Watches(
			&source.Kind{Type: &batchv1.Job{}},
			&handler.EnqueueRequestForOwner{
				IsController: true,
				OwnerType:    &workloadsv1alpha1.Console{},
			},
		).
		Complete(
			recutil.ResolveAndReconcile(
				ctx, logger, mgr, &workloadsv1alpha1.Console{},
				func(logger logr.Logger, request reconcile.Request, obj runtime.Object) (reconcile.Result, error) {
					return r.Reconcile(logger, ctx, request, obj.(*workloadsv1alpha1.Console))
				},
			),
		)
}

func (r *ConsoleReconciler) Reconcile(logger logr.Logger, ctx context.Context, req ctrl.Request, csl *workloadsv1alpha1.Console) (ctrl.Result, error) {
	logger = logger.WithValues("console", req.NamespacedName)

	// Fetch console template
	tpl, err := r.getConsoleTemplate(ctx, csl, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to retrieve console template")
	}

	// Set the template as owner of the console
	// This means the console will be deleted if the template is deleted
	csl, err = setConsoleOwner(csl, tpl, r.Scheme)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to set controller reference on console object")
	}

	csl = setConsoleTTLs(csl, tpl)
	csl = r.setConsoleTimeout(logger, csl, tpl)

	// We call this function here to ensure that we perform an update on the
	// console object *if* one is needed; i.e. it defends against not correctly
	// wrapping an `Update()` call in a conditional. It also makes the console
	// object updates more consistent with the other resources we maintain in this
	// control loop.
	//
	// This will in turn call the recutil.CreateOrUpdate function, which _could_
	// be considered a slight impedance mismatch in this context: That function
	// will go and retrieve the latest version of the object from the Kubernetes
	// API before performing the update, but this isn't strictly necessary here
	// because we've already been provided a fresh version of the object when
	// entering the reconciliation loop.
	// For the moment though, the simplification and safety outweighs the cost of
	// the extra API call.
	if err := r.createOrUpdate(ctx, logger, csl, csl, Console, consoleDiff); err != nil {
		return ctrl.Result{}, err
	}

	// Get the command for the console to run
	var command []string
	if command, err = r.getCommand(csl, tpl); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "neither the console or template have a command to evaluate")
	}

	// Create an authorisation object, if required.
	var (
		authRule      *workloadsv1alpha1.ConsoleAuthorisationRule
		authorisation *workloadsv1alpha1.ConsoleAuthorisation
	)

	if tpl.HasAuthorisationRules() {
		rule, err := tpl.GetAuthorisationRuleForCommand(command)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to determine authorisation rule for console command")
		}

		authRule = &rule
		if err := r.createAuthorisationObjects(ctx, logger, csl, req.NamespacedName, authRule.Subjects); err != nil {
			return ctrl.Result{}, err
		}

		authorisation, err = r.getConsoleAuthorisation(ctx, req.NamespacedName)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to retrieve console authorisation")
		}
	}

	job, err := r.getJob(ctx, req.NamespacedName)
	if err != nil {
		job = nil
	}

	// Only create/update a job when the console is authorised and pending job
	// creation or when a job already exists, i.e. if we've already passed the
	// Creating phase, but the job no longer exists (it's been destroyed external
	// to this controller) then don't recreate it.
	authorised := isConsoleAuthorised(authRule, authorisation)
	if (authorised && csl.PendingJob()) || job != nil {
		job = r.buildJob(logger, req.NamespacedName, csl, tpl)
		if err := r.createOrUpdate(ctx, logger, csl, job, Job, jobDiff); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Update the status fields in case they're out of sync, or the console spec
	// has been updated
	statusCtx := consoleStatusContext{
		Command:           command,
		IsAuthorised:      authorised,
		Authorisation:     authorisation,
		AuthorisationRule: authRule,
		Job:               job,
	}

	csl, err = r.generateStatusAndAuditEvents(ctx, logger, req.NamespacedName, csl, statusCtx)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to generate console status or audit events")
	}

	if err := r.createOrUpdate(ctx, logger, csl, csl, Console, consoleDiff); err != nil {
		return ctrl.Result{}, err
	}

	var res ctrl.Result
	switch {
	case csl.PendingAuthorisation():
		// Requeue for when the console has reached its before-running TTL, so that
		// it can be deleted if it has not yet been authorised by that point.
		res = requeueAfterInterval(logger, time.Until(*csl.GetGCTime()))
	case csl.Pending():
		// Requeue every second while job has been created but there is not yet a
		// running pod: we won't receive an event via the job watcher when this
		// event happens, so this is a cheaper alternative to watching the
		// pods resource and triggering reconciliations via that.
		res = requeueAfterInterval(logger, time.Second)
	case csl.Running():
		// Create or update the role
		// Role grants permissions for a specific resource name, we need to
		// wait until the Pod is running to know the resource name
		role := buildRole(req.NamespacedName, csl.Status.PodName)
		if err := r.createOrUpdate(ctx, logger, csl, role, Role, recutil.RoleDiff); err != nil {
			return res, err
		}

		// Create or update the directory role binding
		subjects := append(
			tpl.Spec.AdditionalAttachSubjects,
			rbacv1.Subject{Kind: "User", Name: csl.Spec.User},
		)

		drb := buildDirectoryRoleBinding(req.NamespacedName, role, subjects)
		if err := r.createOrUpdate(ctx, logger, csl, drb, DirectoryRoleBinding, recutil.DirectoryRoleBindingDiff); err != nil {
			return ctrl.Result{}, err
		}
	case csl.PostRunning():
		// Requeue for when the console has reached its after finished TTL so it can be deleted
		res = requeueAfterInterval(logger, time.Until(*csl.GetGCTime()))
	}

	if csl.EligibleForGC() {
		logger.Info("Deleting expired console", "event", EventDelete, "kind", Console)
		if err = r.Delete(ctx, csl, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{Requeue: false}, nil
	}

	return res, err
}

func (r *ConsoleReconciler) getConsoleTemplate(ctx context.Context, csl *workloadsv1alpha1.Console, name types.NamespacedName) (*workloadsv1alpha1.ConsoleTemplate, error) {
	tplName := types.NamespacedName{
		Name:      csl.Spec.ConsoleTemplateRef.Name,
		Namespace: name.Namespace,
	}

	tpl := &workloadsv1alpha1.ConsoleTemplate{}
	return tpl, r.Get(ctx, tplName, tpl)
}

func (r *ConsoleReconciler) getCommand(csl *workloadsv1alpha1.Console, template *workloadsv1alpha1.ConsoleTemplate) ([]string, error) {
	if len(csl.Spec.Command) > 0 {
		return csl.Spec.Command, nil
	}
	return template.GetDefaultCommandWithArgs()
}

func (r *ConsoleReconciler) getConsoleAuthorisation(ctx context.Context, name types.NamespacedName) (*workloadsv1alpha1.ConsoleAuthorisation, error) {
	auth := &workloadsv1alpha1.ConsoleAuthorisation{}
	return auth, r.Get(ctx, name, auth)
}

func (r *ConsoleReconciler) getJob(ctx context.Context, name types.NamespacedName) (*batchv1.Job, error) {
	jobName := types.NamespacedName{
		Name:      getJobName(name.Name),
		Namespace: name.Namespace,
	}

	job := &batchv1.Job{}
	return job, r.Get(ctx, jobName, job)
}

func setConsoleOwner(console *workloadsv1alpha1.Console, consoleTemplate *workloadsv1alpha1.ConsoleTemplate, scheme *runtime.Scheme) (*workloadsv1alpha1.Console, error) {
	updatedCsl := console.DeepCopy()
	if err := controllerutil.SetControllerReference(consoleTemplate, updatedCsl, scheme); err != nil {
		return nil, errors.Wrap(err, "failed to set controller reference")
	}

	return updatedCsl, nil
}

func setConsoleTTLs(console *workloadsv1alpha1.Console, consoleTemplate *workloadsv1alpha1.ConsoleTemplate) *workloadsv1alpha1.Console {
	defaultTTLSecondsBeforeRunning := int32(DefaultTTLBeforeRunning.Seconds())
	defaultTTLSecondsAfterFinished := int32(DefaultTTLAfterFinished.Seconds())

	updatedCsl := console.DeepCopy()

	if console.Spec.TTLSecondsBeforeRunning != nil {
		updatedCsl.Spec.TTLSecondsBeforeRunning = console.Spec.TTLSecondsBeforeRunning
	} else if consoleTemplate.Spec.DefaultTTLSecondsBeforeRunning != nil {
		updatedCsl.Spec.TTLSecondsBeforeRunning = consoleTemplate.Spec.DefaultTTLSecondsBeforeRunning
	} else {
		updatedCsl.Spec.TTLSecondsBeforeRunning = &defaultTTLSecondsBeforeRunning
	}

	if console.Spec.TTLSecondsAfterFinished != nil {
		updatedCsl.Spec.TTLSecondsAfterFinished = console.Spec.TTLSecondsAfterFinished
	} else if consoleTemplate.Spec.DefaultTTLSecondsAfterFinished != nil {
		updatedCsl.Spec.TTLSecondsAfterFinished = consoleTemplate.Spec.DefaultTTLSecondsAfterFinished
	} else {
		updatedCsl.Spec.TTLSecondsAfterFinished = &defaultTTLSecondsAfterFinished
	}

	return updatedCsl
}

func (r *ConsoleReconciler) createOrUpdate(ctx context.Context, logger logr.Logger, csl *workloadsv1alpha1.Console, expected recutil.ObjWithMeta, kind string, diffFunc recutil.DiffFunc) error {
	// If operating on the console itself, don't attempt to set the controller
	// reference, as this isn't valid.
	if kind != Console {
		if err := controllerutil.SetControllerReference(csl, expected, r.Scheme); err != nil {
			return err
		}
	}

	outcome, err := recutil.CreateOrUpdate(ctx, r, expected, diffFunc)
	if err != nil {
		return errors.Wrap(err, "CreateOrUpdate failed")
	}

	// Use the same 'kind: obj-name' format as in the core controllers, when
	// emitting events.
	objDesc := fmt.Sprintf("%s: %s", kind, expected.GetName())

	switch outcome {
	case recutil.Create:
		logger.Info("Created "+objDesc, "event", EventSuccessfulCreate)
	case recutil.Update:
		logger.Info("Updated "+objDesc, "event", EventSuccessfulUpdate)
	case recutil.None:
		logging.WithNoRecord(logger).Info(
			"Nothing to do for "+objDesc,
			"event", EventNoCreateOrUpdate,
		)
	default:
		// This is only possible in case we implement new outcomes and forget to
		// add a case here; in which case we should log a warning.
		msg := fmt.Sprintf("Unknown outcome %s for %s", outcome, objDesc)
		logger.Info(
			msg,
			"event", EventUnknownOutcome,
			"error", msg,
		)
	}

	return nil
}

// Ensure the console timeout is between [0, template.MaxTimeoutSeconds]
func (r *ConsoleReconciler) setConsoleTimeout(logger logr.Logger, console *workloadsv1alpha1.Console, template *workloadsv1alpha1.ConsoleTemplate) *workloadsv1alpha1.Console {
	var timeout int
	max := template.Spec.MaxTimeoutSeconds

	switch {
	case console.Spec.TimeoutSeconds < 1:
		timeout = template.Spec.DefaultTimeoutSeconds
	case console.Spec.TimeoutSeconds > max:
		msg := fmt.Sprintf("Specified timeout exceeded the template maximum; reduced to %ds", max)
		logger.Info(
			msg,
			"event", EventInvalidSpecification,
			"error", msg,
		)
		timeout = max
	default:
		timeout = console.Spec.TimeoutSeconds
	}

	updatedCsl := console.DeepCopy()
	updatedCsl.Spec.TimeoutSeconds = timeout

	return updatedCsl
}

func isConsoleAuthorised(rule *workloadsv1alpha1.ConsoleAuthorisationRule, auth *workloadsv1alpha1.ConsoleAuthorisation) bool {
	if rule == nil {
		return true
	}
	if auth == nil {
		return false
	}

	if len(auth.Spec.Authorisations) >= rule.ConsoleAuthorisers.AuthorisationsRequired {
		return true
	}

	return false
}

// consoleStatusContext is a wrapper for the objects required to calculate the
// status of a console and generate audit log events - primarily to help keep
// function signatures concise.
type consoleStatusContext struct {
	Command           []string
	IsAuthorised      bool
	Authorisation     *workloadsv1alpha1.ConsoleAuthorisation
	AuthorisationRule *workloadsv1alpha1.ConsoleAuthorisationRule
	Pod               *corev1.Pod
	Job               *batchv1.Job
}

func (r *ConsoleReconciler) generateStatusAndAuditEvents(ctx context.Context, logger logr.Logger, name types.NamespacedName, csl *workloadsv1alpha1.Console, statusCtx consoleStatusContext) (*workloadsv1alpha1.Console, error) {
	var (
		pod     *corev1.Pod
		podList corev1.PodList
	)

	if statusCtx.Job != nil {
		inNamespace := client.InNamespace(name.Namespace)
		matchLabels := client.MatchingLabels(map[string]string{"job-name": statusCtx.Job.ObjectMeta.Name})
		if err := r.List(ctx, &podList, inNamespace, matchLabels); err != nil {
			return nil, errors.Wrap(err, "failed to list pods for console job")
		}
	}
	if len(podList.Items) > 0 {
		pod = &podList.Items[0]
	} else {
		pod = nil
	}

	statusCtx.Pod = pod

	logger = getAuditLogger(logger, csl, statusCtx)
	newStatus := calculateStatus(csl, statusCtx)

	if csl.Creating() && newStatus.Phase == workloadsv1alpha1.ConsolePendingAuthorisation {
		logger.Info("Console pending authorisation", "event", ConsolePendingAuthorisation)
	}

	// Console phase from Pending Authorisation
	if csl.PendingAuthorisation() && newStatus.Phase != workloadsv1alpha1.ConsolePendingAuthorisation {
		logger.Info("Console authorised", "event", ConsoleAuthorised)
	}

	// Console phase from Pending to Running
	if csl.Pending() && newStatus.Phase == workloadsv1alpha1.ConsoleRunning {
		logger.Info("Console started", "event", ConsoleStarted)
	}

	// Console phase from Running to Stopped, with a CompletionTime: the job
	// completed successfully
	if csl.Running() && newStatus.Phase == workloadsv1alpha1.ConsoleStopped &&
		newStatus.CompletionTime != nil {
		duration := statusCtx.Job.Status.CompletionTime.Sub(statusCtx.Job.Status.StartTime.Time).Seconds()
		logger.Info("Console ended", "event", ConsoleEnded, "duration", duration)
	}

	// Console phase from Running to Stopped without CompletionTime.
	// Either:
	// - The job's activeDeadlineSeconds was reached, and the job was marked as
	//   failed and the pod deleted.
	// - The pod ended with a non-zero exit code, and the job was marked as failed.
	if csl.Running() && newStatus.Phase == workloadsv1alpha1.ConsoleStopped &&
		newStatus.CompletionTime == nil {
		duration := csl.Status.ExpiryTime.Sub(statusCtx.Job.Status.StartTime.Time).Seconds()
		logger.Info("Console ended due to expiration", "event", ConsoleEnded, "duration", duration)
	}

	// Console phase transitioned to Stopped, but wasn't Running or Stopped beforehand.
	// This could indicate a bug, or the console may have transitioned through
	// more than one phase in between reconciliation loops.
	if !csl.Running() && !csl.Stopped() && newStatus.Phase == workloadsv1alpha1.ConsoleStopped {
		logger.Info("Console ended: duration unknown", "event", ConsoleEnded)
	}

	// Console was in PendingAuthorisation phase, but is about to be deleted.
	if csl.PendingAuthorisation() && csl.EligibleForGC() {
		logger.Info("Console expired due to lack of authorisation", "event", ConsoleEnded)
	}

	// Console phase has changed to destroyed (i.e. the job has been removed)
	if !csl.Destroyed() && newStatus.Phase == workloadsv1alpha1.ConsoleDestroyed {
		logger.Info("Console destroyed", "event", ConsoleDestroyed)
	}

	updatedCsl := csl.DeepCopy()
	updatedCsl.Status = newStatus

	return updatedCsl, nil
}

func calculateStatus(csl *workloadsv1alpha1.Console, statusCtx consoleStatusContext) workloadsv1alpha1.ConsoleStatus {
	newStatus := csl.DeepCopy().Status

	if statusCtx.Job != nil {
		// We want to give the console session *at least* the time specified in the
		// timeout, therefore base the expiry time on the job creation time, rather
		// than the console creation time, to take into account any delays in
		// reconciling the console object.
		// TODO: We may actually want to use a base of when the Pod entered the
		// Running phase, as image pull time could be significant in some cases.
		jobCreationTime := statusCtx.Job.ObjectMeta.CreationTimestamp.Time
		expiryTime := metav1.NewTime(
			jobCreationTime.Add(time.Second * time.Duration(csl.Spec.TimeoutSeconds)),
		)
		newStatus.ExpiryTime = &expiryTime
		newStatus.CompletionTime = statusCtx.Job.Status.CompletionTime
	}
	if statusCtx.Pod != nil {
		newStatus.PodName = statusCtx.Pod.ObjectMeta.Name
	}

	newStatus.Phase = calculatePhase(statusCtx)

	return newStatus
}

func calculatePhase(statusCtx consoleStatusContext) workloadsv1alpha1.ConsolePhase {
	if !statusCtx.IsAuthorised {
		return workloadsv1alpha1.ConsolePendingAuthorisation
	}

	if statusCtx.Job == nil {
		return workloadsv1alpha1.ConsoleDestroyed
	}

	// Currently a job can only have two conditions: Complete and Failed
	// Both indicate that the console has stopped
	for _, c := range statusCtx.Job.Status.Conditions {
		if c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed {
			return workloadsv1alpha1.ConsoleStopped
		}
	}

	// If the pod exists and is running, then the console is running
	if statusCtx.Pod != nil && statusCtx.Pod.Status.Phase == corev1.PodRunning {
		return workloadsv1alpha1.ConsoleRunning
	}

	// Otherwise, assume the console is pending (i.e. still starting up)
	return workloadsv1alpha1.ConsolePending
}

func requeueAfterInterval(logger logr.Logger, interval time.Duration) reconcile.Result {
	logging.WithNoRecord(logger).Info(
		"Reconciliation requeued",
		"event", recutil.EventRequeued,
		"reconcile_after", interval,
	)
	return reconcile.Result{Requeue: true, RequeueAfter: interval}
}

func (r *ConsoleReconciler) buildJob(logger logr.Logger, name types.NamespacedName, csl *workloadsv1alpha1.Console, template *workloadsv1alpha1.ConsoleTemplate) *batchv1.Job {
	timeout := int64(csl.Spec.TimeoutSeconds)

	username := strings.SplitN(csl.Spec.User, "@", 2)[0]
	jobTemplate := template.Spec.Template.DeepCopy()

	numContainers := len(jobTemplate.Spec.Containers)

	// If there's no containers in the spec then the controller will be emitting
	// warnings anyway, as the job will be rejected
	if numContainers > 0 {
		container := &jobTemplate.Spec.Containers[0]

		// Only replace the template command if one is specified
		if len(csl.Spec.Command) > 0 {
			container.Command = csl.Spec.Command[:1]
			container.Args = csl.Spec.Command[1:]
		}

		if !csl.Spec.Noninteractive {
			// Set these properties to ensure that it's possible to send input to the
			// container when attaching
			container.Stdin = true
			container.TTY = true
		}
	}

	if numContainers > 1 {
		msg := "A console template can only contain a single container"
		logger.Info(
			msg,
			"event", EventTemplateUnsupported,
			"error", msg,
		)
	}

	// Job API SetDefaults_Job
	// https://github.com/kubernetes/kubernetes/blob/master/pkg/apis/batch/v1/defaults.go#L28
	completions := int32(1)
	parallelism := int32(1)

	// Do not retry console jobs if they fail. There is no guarantee that the
	// command that the user submits will be idempotent.
	// This also prevents multiple pods from being spawned by a job, which is
	// important as other parts of the controller assume there will only ever be
	// 1 pod per job.
	backoffLimit := int32(0)
	jobTemplate.Spec.RestartPolicy = corev1.RestartPolicyNever

	jobName := getJobName(name.Name)

	// Merged labels from the console template and console. In case of
	// conflicts second label set wins.
	// The labels on the console can be user-defined, so we do not want to allow a
	// user to create a console with a label that implies that it's for an application
	// different to the console.
	jobLabels := labels.Merge(csl.Labels, template.Labels)
	jobLabels = labels.Merge(jobLabels,
		map[string]string{
			"console-name": sanitiseLabel(csl.Name),
			"user":         sanitiseLabel(username),
		})

	jobTemplate.ObjectMeta.Labels = labels.Merge(
		jobLabels,
		jobTemplate.ObjectMeta.Labels,
	)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: name.Namespace,
			Labels:    jobLabels,
		},
		Spec: batchv1.JobSpec{
			Template:              corev1.PodTemplateSpec(*jobTemplate),
			Completions:           &completions,
			Parallelism:           &parallelism,
			ActiveDeadlineSeconds: &timeout,
			BackoffLimit:          &backoffLimit,
		},
	}
}

func buildRole(name types.NamespacedName, podName string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"create"},
				APIGroups:     []string{""},
				Resources:     []string{"pods/exec", "pods/attach"},
				ResourceNames: []string{podName},
			},
			{
				Verbs:         []string{"get"},
				APIGroups:     []string{""},
				Resources:     []string{"pods/log"},
				ResourceNames: []string{podName},
			},
			{
				Verbs:         []string{"get", "delete"},
				APIGroups:     []string{""},
				Resources:     []string{"pods"},
				ResourceNames: []string{podName},
			},
		},
	}
}

func buildDirectoryRoleBinding(name types.NamespacedName, role *rbacv1.Role, subjects []rbacv1.Subject) *rbacv1alpha1.DirectoryRoleBinding {
	return &rbacv1alpha1.DirectoryRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Spec: rbacv1alpha1.DirectoryRoleBindingSpec{
			Subjects: subjects,
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "Role",
				Name:     name.Name,
			},
		},
	}
}

func (r *ConsoleReconciler) createAuthorisationObjects(ctx context.Context, logger logr.Logger, csl *workloadsv1alpha1.Console, name types.NamespacedName, subjects []rbacv1.Subject) error {
	authorisation := &workloadsv1alpha1.ConsoleAuthorisation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Labels:    csl.Labels,
		},
		Spec: workloadsv1alpha1.ConsoleAuthorisationSpec{
			ConsoleRef:     corev1.LocalObjectReference{Name: name.Name},
			Authorisations: []rbacv1.Subject{},
		},
	}

	// Create the consoleauthorisation
	if err := r.createOrUpdate(ctx, logger, csl, authorisation, ConsoleAuthorisation, authorisationDiff); err != nil {
		return errors.Wrap(err, "failed to create consoleauthorisation")
	}

	// We already create roles and directory rolebindings with the same name as
	// the console to provide permissions on the job/pod. Therefore for the name
	// of these objects, suffix the console name with '-authorisation'.
	rbacName := types.NamespacedName{
		Name:      fmt.Sprintf("%s-%s", name.Name, "authorisation"),
		Namespace: name.Namespace,
	}

	// Create the role that allows updating this consoleauthorisation
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbacName.Name,
			Namespace: name.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"get", "patch", "update"},
				APIGroups:     []string{"workloads.crd.gocardless.com"},
				Resources:     []string{"consoleauthorisations"},
				ResourceNames: []string{name.Name},
			},
		},
	}

	if err := r.createOrUpdate(ctx, logger, csl, role, Role, recutil.RoleDiff); err != nil {
		return errors.Wrap(err, "failed to create role for consoleauthorisation")
	}

	// Create or update the directory role binding
	drb := buildDirectoryRoleBinding(rbacName, role, subjects)
	if err := r.createOrUpdate(ctx, logger, csl, drb, DirectoryRoleBinding, recutil.DirectoryRoleBindingDiff); err != nil {
		return errors.Wrap(err, "failed to create directory rolebinding for consoleauthorisation")
	}

	return nil
}

// authorisationDiff is a reconcile.DiffFunc for ConsoleAuthorisations
func authorisationDiff(expectedObj runtime.Object, existingObj runtime.Object) recutil.Outcome {
	expected := expectedObj.(*workloadsv1alpha1.ConsoleAuthorisation)
	existing := existingObj.(*workloadsv1alpha1.ConsoleAuthorisation)
	operation := recutil.None

	// compare labels
	if !reflect.DeepEqual(expected.ObjectMeta.Labels, existing.ObjectMeta.Labels) {
		existing.ObjectMeta.Labels = expected.ObjectMeta.Labels
		operation = recutil.Update
	}

	// compare all spec fields other than `authorisations`, which will be mutated
	// by the authorising user.

	if !reflect.DeepEqual(expected.Spec.ConsoleRef, existing.Spec.ConsoleRef) {
		existing.Spec.ConsoleRef = expected.Spec.ConsoleRef
		operation = recutil.Update
	}

	return operation
}

// consoleDiff is a reconcile.DiffFunc for Consoles
func consoleDiff(expectedObj runtime.Object, existingObj runtime.Object) recutil.Outcome {
	expected := expectedObj.(*workloadsv1alpha1.Console)
	existing := existingObj.(*workloadsv1alpha1.Console)
	operation := recutil.None

	// Because this controller is responsible for the Console object the diff
	// calculation is simple: if any of the spec or status fields, or the
	// controller reference, have changed then perform an update.
	if !reflect.DeepEqual(expected.ObjectMeta.OwnerReferences, existing.ObjectMeta.OwnerReferences) {
		existing.ObjectMeta.OwnerReferences = expected.ObjectMeta.OwnerReferences
		operation = recutil.Update
	}

	if !reflect.DeepEqual(expected.Spec, existing.Spec) {
		existing.Spec = expected.Spec
		operation = recutil.Update
	}

	if !reflect.DeepEqual(expected.Status, existing.Status) {
		existing.Status = expected.Status
		operation = recutil.Update
	}

	return operation
}

// jobDiff is a reconcile.DiffFunc for Jobs
func jobDiff(expectedObj runtime.Object, existingObj runtime.Object) recutil.Outcome {
	expected := expectedObj.(*batchv1.Job)
	existing := existingObj.(*batchv1.Job)
	operation := recutil.None

	// compare all mutable fields in jobSpec and labels
	if !reflect.DeepEqual(expected.ObjectMeta.Labels, existing.ObjectMeta.Labels) {
		existing.ObjectMeta.Labels = expected.ObjectMeta.Labels
		operation = recutil.Update
	}

	if !reflect.DeepEqual(expected.Spec.ActiveDeadlineSeconds, existing.Spec.ActiveDeadlineSeconds) {
		existing.Spec.ActiveDeadlineSeconds = expected.Spec.ActiveDeadlineSeconds
		operation = recutil.Update
	}

	if !reflect.DeepEqual(expected.Spec.BackoffLimit, existing.Spec.BackoffLimit) {
		existing.Spec.BackoffLimit = expected.Spec.BackoffLimit
		operation = recutil.Update
	}

	if !reflect.DeepEqual(expected.Spec.Completions, existing.Spec.Completions) {
		existing.Spec.Completions = expected.Spec.Completions
		operation = recutil.Update
	}

	if !reflect.DeepEqual(expected.Spec.Parallelism, existing.Spec.Parallelism) {
		existing.Spec.Parallelism = expected.Spec.Parallelism
		operation = recutil.Update
	}

	if !reflect.DeepEqual(expected.Spec.TTLSecondsAfterFinished, existing.Spec.TTLSecondsAfterFinished) {
		existing.Spec.TTLSecondsAfterFinished = expected.Spec.TTLSecondsAfterFinished
		operation = recutil.Update
	}

	return operation
}

// getAuditLogger provides a decorated logger for audit purposes
func getAuditLogger(logger logr.Logger, c *workloadsv1alpha1.Console, statusCtx consoleStatusContext) logr.Logger {
	loggerCtx := logging.WithNoRecord(logger)
	// Append any label-based keys before doing anything else.
	// This ensures that if there's duplicate keys (e.g. a `name` label on the
	// console) then we won't clobber the keys which we explicitly set below with
	// the values of those in the console labels, when eventually parsing the log
	// entry.
	loggerCtx = logging.WithLabels(loggerCtx, c.Labels, "console_")

	cmdString, _ := json.Marshal(statusCtx.Command)
	requiresAuth := statusCtx.AuthorisationRule != nil && statusCtx.AuthorisationRule.AuthorisationsRequired > 0

	loggerCtx = loggerCtx.WithValues(
		"kind", Console,
		"console_name", c.Name,
		"console_user", c.Spec.User,

		"console_requires_authorisation", requiresAuth,
		// Note that a console that does not require authorisation is considered
		// authorised by default.
		"console_is_authorised", statusCtx.IsAuthorised,
		"command", string(cmdString),
		"reason", c.Spec.Reason,
	)

	if statusCtx.Pod != nil {
		loggerCtx = loggerCtx.WithValues("console_pod_name", statusCtx.Pod.Name)
	}

	if statusCtx.AuthorisationRule != nil {
		loggerCtx = loggerCtx.WithValues(
			"console_authorisation_rule_name", statusCtx.AuthorisationRule.Name,
			"console_authorisation_authorisers_required", statusCtx.AuthorisationRule.AuthorisationsRequired,
		)
	}

	if statusCtx.Authorisation != nil {
		subjectNames := []string{}
		for _, subject := range statusCtx.Authorisation.Spec.Authorisations {
			subjectNames = append(subjectNames, subject.Name)
		}

		authorisers, _ := json.Marshal(subjectNames)
		loggerCtx = loggerCtx.WithValues("console_authorisers", string(authorisers))
	}

	return loggerCtx
}

// Ensure that the job name (after suffixing with `-console`) does not exceed 63
// characters. This is the string length limit on labels and the job name is added
// as a label to the pods it creates.
func getJobName(consoleName string) string {
	return fmt.Sprintf("%s-%s", truncateString(consoleName, 55), "console")
}

// Kubernetes labels must satisfy (([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])? and not
// exceed 63 characters in length.
// We don't bother with the first and last character sanitisation here - just anything
// dodgy in the middle.
// This is mostly so that, in tests, we correctly handle the system:unsecured user.
func sanitiseLabel(l string) string {
	return truncateString(regexp.MustCompile(`[^A-z0-9\-_.]`).ReplaceAllString(l, "-"), 63)
}

func truncateString(str string, length int) string {
	if len(str) > length {
		return str[0:length]
	}
	return str
}
