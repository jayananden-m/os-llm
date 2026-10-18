package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	servingv1alpha1 "github.com/jayananden-m/os-llm/operator/api/v1alpha1"
)

var _ = Describe("ModelDeployment Controller", func() {
	const name = "test-model"
	ctx := context.Background()
	key := types.NamespacedName{Name: name, Namespace: "default"}

	BeforeEach(func() {
		err := k8sClient.Get(ctx, key, &servingv1alpha1.ModelDeployment{})
		if apierrors.IsNotFound(err) {
			replicas := int32(2)
			md := &servingv1alpha1.ModelDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: servingv1alpha1.ModelDeploymentSpec{
					Model:    "test-model",
					Image:    "os-llm/mockllm:dev",
					Replicas: &replicas,
				},
			}
			Expect(k8sClient.Create(ctx, md)).To(Succeed())
		}
	})

	AfterEach(func() {
		md := &servingv1alpha1.ModelDeployment{}
		if err := k8sClient.Get(ctx, key, md); err == nil {
			Expect(k8sClient.Delete(ctx, md)).To(Succeed())
		}
	})

	It("creates a Deployment and Service owned by the ModelDeployment", func() {
		r := &ModelDeploymentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
		Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("os-llm/mockllm:dev"))
		Expect(dep.OwnerReferences).To(HaveLen(1))

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8000))) // CRD default applied

		md := &servingv1alpha1.ModelDeployment{}
		Expect(k8sClient.Get(ctx, key, md)).To(Succeed())
		Expect(md.Status.ServiceName).To(Equal(name))
		Expect(md.Status.Selector).To(Equal("serving.os-llm.dev/model-deployment=" + name))
	})

	It("is idempotent: a second reconcile changes nothing", func() {
		r := &ModelDeploymentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		before := dep.ResourceVersion

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		Expect(dep.ResourceVersion).To(Equal(before))
	})
})
