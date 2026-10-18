package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	servingv1alpha1 "github.com/jayananden-m/os-llm/operator/api/v1alpha1"
)

// LabelModelDeployment marks every object and pod a ModelDeployment owns.
const LabelModelDeployment = "serving.os-llm.dev/model-deployment"

// ModelDeploymentReconciler turns a ModelDeployment into a Deployment and a Service.
type ModelDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=serving.os-llm.dev,resources=modeldeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=serving.os-llm.dev,resources=modeldeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=serving.os-llm.dev,resources=modeldeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile compares what should exist with what does exist and closes the gap.
// It never asks what changed, so running it twice is always safe.
func (r *ModelDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var md servingv1alpha1.ModelDeployment
	if err := r.Get(ctx, req.NamespacedName, &md); err != nil {
		// Deleted: owner references let Kubernetes garbage-collect the children.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	selector := map[string]string{LabelModelDeployment: md.Name}
	objLabels := map[string]string{
		LabelModelDeployment:        md.Name,
		"app.kubernetes.io/name":    md.Name,
		"app.kubernetes.io/part-of": "os-llm",
	}

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: md.Name, Namespace: md.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = objLabels
		svc.Spec.Selector = selector
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       md.Spec.Port,
			TargetPort: intstr.FromString("http"),
			Protocol:   corev1.ProtocolTCP,
		}}
		return controllerutil.SetControllerReference(&md, svc, r.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile service: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		log.Info("service reconciled", "operation", op)
	}

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: md.Name, Namespace: md.Namespace}}
	op, err = controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		dep.Labels = objLabels
		if dep.Spec.Selector == nil { // a Deployment's selector is immutable after creation
			dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		}
		dep.Spec.Replicas = md.Spec.Replicas
		dep.Spec.Template.Labels = objLabels
		dep.Spec.Template.Spec.Containers = []corev1.Container{serverContainer(&md)}
		return controllerutil.SetControllerReference(&md, dep, r.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile deployment: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		log.Info("deployment reconciled", "operation", op)
	}

	return ctrl.Result{}, r.updateStatus(ctx, &md, dep, svc.Name, labels.SelectorFromSet(selector).String())
}

// serverContainer builds the model server container. Fields the API server would
// otherwise default are set explicitly, so every reconcile compares equal and
// no-op updates are skipped.
func serverContainer(md *servingv1alpha1.ModelDeployment) corev1.Container {
	return corev1.Container{
		Name:            "server",
		Image:           md.Spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args:            md.Spec.Args,
		Env:             append([]corev1.EnvVar{{Name: "OSLLM_MODEL", Value: md.Spec.Model}}, md.Spec.Env...),
		Ports: []corev1.ContainerPort{{
			Name:          "http",
			ContainerPort: md.Spec.Port,
			Protocol:      corev1.ProtocolTCP,
		}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Path:   "/health",
				Port:   intstr.FromString("http"),
				Scheme: corev1.URISchemeHTTP,
			}},
			PeriodSeconds:    5,
			TimeoutSeconds:   1,
			SuccessThreshold: 1,
			FailureThreshold: 3,
		},
		Resources:                md.Spec.Resources,
		TerminationMessagePath:   corev1.TerminationMessagePathDefault,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}

func (r *ModelDeploymentReconciler) updateStatus(ctx context.Context, md *servingv1alpha1.ModelDeployment,
	dep *appsv1.Deployment, serviceName, selector string) error {
	desired := int32(1)
	if md.Spec.Replicas != nil {
		desired = *md.Spec.Replicas
	}
	ready := dep.Status.ReadyReplicas

	md.Status.ObservedGeneration = md.Generation
	md.Status.Replicas = dep.Status.Replicas
	md.Status.ReadyReplicas = ready
	md.Status.ServiceName = serviceName
	md.Status.Selector = selector

	cond := metav1.Condition{
		Type:               "Available",
		ObservedGeneration: md.Generation,
		Message:            fmt.Sprintf("%d/%d replicas ready", ready, desired),
	}
	switch {
	case desired == 0:
		cond.Status, cond.Reason = metav1.ConditionFalse, "ScaledToZero"
	case ready >= desired:
		cond.Status, cond.Reason = metav1.ConditionTrue, "AllReplicasReady"
	default:
		cond.Status, cond.Reason = metav1.ConditionFalse, "ReplicasNotReady"
	}
	meta.SetStatusCondition(&md.Status.Conditions, cond)

	return r.Status().Update(ctx, md)
}

// SetupWithManager watches ModelDeployments and the objects they own, so a
// change to either (including someone editing the Deployment by hand) triggers
// a reconcile.
func (r *ModelDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&servingv1alpha1.ModelDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("modeldeployment").
		Complete(r)
}
