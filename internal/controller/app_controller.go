/*
Copyright 2026 Mohan Raj.

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

package controller

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	gatewayAPIGroup = "gateway.networking.k8s.io"
	// appRoutePoll is the time between 2 reads of the route state. The controller has no watch
	// on the HTTPRoute, so that it starts on a cluster with no Gateway API.
	appRoutePoll = 30 * time.Second
)

var httpRouteGVK = schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: "HTTPRoute"}

// AppReconciler makes one Gateway API HTTPRoute for each endpoint of an App. The route sends a
// weight of the traffic to the Service of each version. The controller is not a router: the
// Gateway API implementation of the cluster moves the traffic.
//
// Each route has a controller owner reference to its App, with blockOwnerDeletion. The API server
// permits this reference only with update on apps/finalizers, if the admission plugin
// OwnerReferencesPermissionEnforcement is on.
type AppReconciler struct {
	client.Client
	// APIReader reads the App, the AppVersions, the Services and the routes directly, not from
	// the cache.
	APIReader client.Reader
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=apps/status,verbs=update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=apps/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;create;update;delete

// Reconcile makes the routes of the endpoints, deletes the routes of the endpoints that are gone
// and writes the weights in effect and the route states to the status.
func (r *AppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.APIReader == nil {
		return ctrl.Result{}, errNoAPIReader
	}
	app := &corev1alpha1.App{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, app); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !app.DeletionTimestamp.IsZero() {
		// The garbage collector deletes the routes of the App.
		return ctrl.Result{}, nil
	}
	versions := &corev1alpha1.AppVersionList{}
	if err := r.APIReader.List(ctx, versions, client.InNamespace(app.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	status := make([]corev1alpha1.AppEndpointStatus, 0, len(app.Spec.Endpoints))
	keep := map[string]bool{}
	for i := range app.Spec.Endpoints {
		ep := &app.Spec.Endpoints[i]
		st, err := r.applyRoute(ctx, app, ep, versions.Items)
		if err != nil {
			return ctrl.Result{}, err
		}
		status = append(status, st)
		keep[appRouteName(app, ep.Name)] = true
	}
	if err := r.prune(ctx, app, keep); err != nil {
		return ctrl.Result{}, err
	}
	if !equality.Semantic.DeepEqual(app.Status.Endpoints, status) &&
		(len(app.Status.Endpoints) > 0 || len(status) > 0) {
		app.Status.Endpoints = status
		if err := r.Status().Update(ctx, app); err != nil {
			return ctrl.Result{}, err
		}
	}
	if len(status) == 0 {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: appRoutePoll}, nil
}

// appRouteName is the name of the route of an endpoint. An endpoint name has no dot, so 2
// endpoints of 2 Apps cannot have the same route name.
func appRouteName(app *corev1alpha1.App, endpoint string) string {
	return "kd-" + app.Name + "." + endpoint
}

func newRoute() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(httpRouteGVK)
	return u
}

// applyRoute makes or updates the route of one endpoint and gives back its status.
func (r *AppReconciler) applyRoute(ctx context.Context, app *corev1alpha1.App, ep *corev1alpha1.AppEndpoint,
	versions []corev1alpha1.AppVersion) (corev1alpha1.AppEndpointStatus, error) {
	st := corev1alpha1.AppEndpointStatus{Name: ep.Name}
	refs, down, err := r.backends(ctx, app, ep, versions, &st)
	if err != nil {
		return st, err
	}
	want := appRoute(app, ep, refs)
	have := newRoute()
	err = r.APIReader.Get(ctx, client.ObjectKeyFromObject(want), have)
	switch {
	case meta.IsNoMatchError(err):
		st.RouteState = corev1alpha1.AppRouteNoGatewayAPI
		st.Message = "The cluster does not have the Gateway API HTTPRoute."
		return st, nil
	case apierrors.IsNotFound(err):
		have = want
		err = r.Create(ctx, have)
	case err != nil:
		return st, err
	case !metav1.IsControlledBy(have, app):
		st.RouteState = corev1alpha1.AppRouteNameConflict
		st.Message = "An HTTPRoute with the route name is not from this App."
		return st, nil
	default:
		// The update has the resourceVersion of the read. If the route is equal after the
		// defaults of the API server, the API server does not write it.
		have.SetLabels(want.GetLabels())
		have.Object["spec"] = want.Object["spec"]
		err = r.Update(ctx, have)
	}
	if permanent(err) && !apierrors.IsAlreadyExists(err) {
		// The reason can have a host name of the endpoint. It is not a credential.
		logf.FromContext(ctx).Info("The API server refused the HTTPRoute of an endpoint", "endpoint", ep.Name,
			"reason", string(apierrors.ReasonForError(err)))
		st.RouteState, st.Message = corev1alpha1.AppRouteRefused, "The API server refused the HTTPRoute."
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.RouteState, st.Message = routeState(have)
	if len(down) > 0 {
		st.Message = "These versions cannot serve and get no traffic: " + strings.Join(down, ", ") + ". " + st.Message
	}
	return st, nil
}

// backends gives back the backendRefs of the route. It writes the weights in effect to st. A
// version that cannot serve has the weight 0 and no backendRef. down has the names of the
// versions that have a weight in the spec and cannot serve.
func (r *AppReconciler) backends(ctx context.Context, app *corev1alpha1.App, ep *corev1alpha1.AppEndpoint,
	versions []corev1alpha1.AppVersion, st *corev1alpha1.AppEndpointStatus) (refs []any, down []string, err error) {
	for _, v := range ep.Versions {
		weight := int32(0)
		if v.Weight != nil {
			weight = *v.Weight
		}
		if weight > 0 {
			port, err := r.servingPort(ctx, app, versions, v.Name, ep.Port)
			if err != nil {
				return nil, nil, err
			}
			if port == 0 {
				down, weight = append(down, v.Name), 0
			} else {
				refs = append(refs, backendRef("kd-"+v.Name, port, weight))
			}
		}
		st.Weights = append(st.Weights, corev1alpha1.AppEndpointVersion{Name: v.Name, Weight: &weight})
	}
	return refs, down, nil
}

// backendRef is one backendRef of a route: a Service of this namespace, a port number and a weight.
func backendRef(service string, port, weight int32) map[string]any {
	return map[string]any{"name": service, "port": int64(port), "weight": int64(weight)}
}

// servingPort gives back the number of the Service port with the name port of a version that can
// serve. It gives back 0 if the version is not bound to this App, is not active, has no key or
// has no Service with this port.
func (r *AppReconciler) servingPort(ctx context.Context, app *corev1alpha1.App, versions []corev1alpha1.AppVersion,
	name, port string) (int32, error) {
	for i := range versions {
		av := &versions[i]
		if av.Name != name {
			continue
		}
		if av.Spec.AppRef.Name != app.Name || av.Status.BoundAppUID != app.UID || !av.DeletionTimestamp.IsZero() ||
			av.Spec.Lifecycle == corev1alpha1.AppVersionRetiring || av.Status.KeyHash == "" ||
			meta.FindStatusCondition(av.Status.Conditions, corev1alpha1.AppVersionRevoking) != nil {
			return 0, nil
		}
		svc := &corev1.Service{}
		err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: av.Namespace, Name: appVersionName(av)}, svc)
		if apierrors.IsNotFound(err) || (err == nil && !metav1.IsControlledBy(svc, av)) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		for _, p := range svc.Spec.Ports {
			if p.Name == port {
				return p.Port, nil
			}
		}
	}
	return 0, nil
}

// appRoute is the HTTPRoute of an endpoint. The values are JSON types, as unstructured needs.
func appRoute(app *corev1alpha1.App, ep *corev1alpha1.AppEndpoint, refs []any) *unstructured.Unstructured {
	parent := map[string]any{"group": gatewayAPIGroup, "kind": "Gateway", "name": ep.Gateway.Name}
	if ep.Gateway.Namespace != "" {
		parent["namespace"] = ep.Gateway.Namespace
	}
	if ep.Gateway.SectionName != "" {
		parent["sectionName"] = ep.Gateway.SectionName
	}
	rule := map[string]any{}
	if len(refs) > 0 {
		rule["backendRefs"] = refs
	}
	spec := map[string]any{"parentRefs": []any{parent}, "rules": []any{rule}}
	if len(ep.Hostnames) > 0 {
		hosts := make([]any, 0, len(ep.Hostnames))
		for _, h := range ep.Hostnames {
			hosts = append(hosts, h)
		}
		spec["hostnames"] = hosts
	}
	u := newRoute()
	u.SetName(appRouteName(app, ep.Name))
	u.SetNamespace(app.Namespace)
	u.SetLabels(map[string]string{appLabel: app.Name})
	u.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(app,
		corev1alpha1.GroupVersion.WithKind("App"))})
	u.Object["spec"] = spec
	return u
}

// routeState reads the conditions that the Gateway controllers wrote for the generation of the
// route. The message has the condition type and the reason only.
func routeState(route *unstructured.Unstructured) (state, msg string) {
	parents, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	accepted := len(parents) > 0
	for _, p := range parents {
		parent, _ := p.(map[string]any)
		conds, _, _ := unstructured.NestedSlice(parent, "conditions")
		ok := false
		for _, c := range conds {
			cond, _ := c.(map[string]any)
			typ, status := cond["type"], cond["status"]
			if gen, _ := cond["observedGeneration"].(int64); gen != route.GetGeneration() ||
				(typ != "Accepted" && typ != "ResolvedRefs") {
				continue
			}
			if status == string(metav1.ConditionFalse) {
				reason, _ := cond["reason"].(string)
				return corev1alpha1.AppRouteNotAccepted,
					"A Gateway reports " + typ.(string) + " False with the reason " + reason + "."
			}
			ok = ok || (typ == "Accepted" && status == string(metav1.ConditionTrue))
		}
		accepted = accepted && ok
	}
	if !accepted {
		return corev1alpha1.AppRoutePending, "No Gateway controller accepted the route at this time."
	}
	return corev1alpha1.AppRouteAccepted, "Each parent Gateway accepted the route."
}

// prune deletes the routes of this App that are not in keep.
func (r *AppReconciler) prune(ctx context.Context, app *corev1alpha1.App, keep map[string]bool) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(httpRouteGVK.GroupVersion().WithKind("HTTPRouteList"))
	err := r.APIReader.List(ctx, list, client.InNamespace(app.Namespace), client.MatchingLabels{appLabel: app.Name})
	if meta.IsNoMatchError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for i := range list.Items {
		o := &list.Items[i]
		if keep[o.GetName()] || !metav1.IsControlledBy(o, app) {
			continue
		}
		uid := o.GetUID()
		if err := r.Delete(ctx, o, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// appOfVersion gives back the App of an AppVersion.
func appOfVersion(_ context.Context, obj client.Object) []reconcile.Request {
	av, ok := obj.(*corev1alpha1.AppVersion)
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: av.Namespace, Name: av.Spec.AppRef.Name}}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.App{}).
		Watches(&corev1alpha1.AppVersion{}, handler.EnqueueRequestsFromMapFunc(appOfVersion)).
		Named("app").
		Complete(r)
}
