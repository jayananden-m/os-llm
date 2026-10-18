package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ModelDeploymentSpec is what the user asks for: one model, served by N replicas.
type ModelDeploymentSpec struct {
	// Model is the model ID clients put in the "model" field of a request.
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// Image is the model server image (vLLM, or the mock while developing).
	// +kubebuilder:default="os-llm/mockllm:dev"
	// +optional
	Image string `json:"image,omitempty"`

	// Args are passed to the model server container as-is.
	// +optional
	Args []string `json:"args,omitempty"`

	// Replicas is the number of model server pods.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// MaxInFlightPerReplica is how many requests the gateway sends to one replica at once.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=4
	// +optional
	MaxInFlightPerReplica int32 `json:"maxInFlightPerReplica,omitempty"`

	// MaxQueue is how many requests the gateway holds when every replica is full.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=16
	// +optional
	MaxQueue int32 `json:"maxQueue,omitempty"`

	// Port the model server listens on.
	// +kubebuilder:default=8000
	// +optional
	Port int32 `json:"port,omitempty"`

	// Resources for the model server container (CPU, memory, GPUs).
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// ModelDeploymentStatus is what the controller observed.
type ModelDeploymentStatus struct {
	// ObservedGeneration is the spec generation this status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Replicas is the current number of model server pods.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of pods passing their readiness probe.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// ServiceName is the Service the gateway discovers replicas from.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// Selector is the pod label selector, required by the scale subresource for autoscaling.
	// +optional
	Selector string `json:"selector,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName=md
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ModelDeployment serves one model behind the os-llm gateway.
type ModelDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelDeploymentSpec   `json:"spec,omitempty"`
	Status ModelDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelDeploymentList contains a list of ModelDeployment.
type ModelDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelDeployment `json:"items"`
}

// func init() {
// 	SchemeBuilder.Register(&ModelDeployment{}, &ModelDeploymentList{})
// }

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &ModelDeployment{}, &ModelDeploymentList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
}
