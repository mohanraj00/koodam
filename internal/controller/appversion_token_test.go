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
	"errors"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/internal/gateway"
	"github.com/mohanraj00/koodam/internal/tenanttoken"
)

// tokenClaim is a claim check that signs a tenant token.
var tokenClaim = AppClaimCheck{Issuer: "https://tokens.example.com", TenantClaim: "tenant",
	Image: "registry.example.com/check:test", TenantToken: true}

// tokenGateway accepts each key call. The embedded gateway is not used.
type tokenGateway struct{ gateway.Gateway }

func (*tokenGateway) CreateKey(context.Context, gateway.KeySpec) error { return nil }

func (*tokenGateway) RenewKey(context.Context, gateway.KeySpec) error { return nil }

// tokenWorld gives back a fake API with the objects of one AppVersion, and a reconciler for it.
func tokenWorld(t *testing.T, claim AppClaimCheck) (client.WithWatch, *AppVersionReconciler, *corev1beta1.AppVersion) {
	t.Helper()
	app, av, p := versionCostGroupObjects(testTeam, nil)
	av.UID, app.UID = "token-version-uid", "token-app-uid"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testTeam,
		Labels: map[string]string{corev1beta1.ProfileLabel: p.Name}}}
	c := fake.NewClientBuilder().WithScheme(ingWorld(t).Scheme()).WithObjects(ns, app, av, p).
		WithStatusSubresource(&corev1beta1.AppVersion{}).Build()
	return c, &AppVersionReconciler{Client: c, Scheme: c.Scheme(), APIReader: c, Gateway: &tokenGateway{},
		GatewayURL: testGatewayURL, Claim: claim}, av
}

func tokenSecret(t *testing.T, c client.Client, av *corev1beta1.AppVersion) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	err := c.Get(t.Context(), types.NamespacedName{Namespace: av.Namespace, Name: tenantTokenName(av)}, s)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTenantTokenKeyOfAVersion(t *testing.T) {
	ctx := t.Context()
	c, r, av := tokenWorld(t, tokenClaim)
	d, err := r.deployment(ctx, av, "")
	if err != nil {
		t.Fatal(err)
	}
	s := tokenSecret(t, c, av)
	if s == nil || !metav1.IsControlledBy(s, av) || s.Immutable == nil || !*s.Immutable || len(s.Data) != 1 {
		t.Fatal("The version must have an immutable Secret with the seed only, and it must own the Secret.")
	}
	key, err := tenanttoken.PrivateKey(s.Data[tokenSeedField])
	if err != nil || av.Status.TenantTokenKey != tenanttoken.PublicText(key) {
		t.Fatalf("The status must have the public key of the seed: %v", err)
	}
	pod := d.Spec.Template
	check := pod.Spec.InitContainers[0]
	file := tokenDir + "/" + tokenSeedField
	if check.Name != claimContainer || !slices.Contains(check.Args, file) ||
		check.Args[slices.Index(check.Args, file)-1] != "--tenant-token-key-file" {
		t.Errorf("The check container must get the key file: %v", check.Args)
	}
	if pod.Annotations[tokenKeyIDAnnotation] != tokenKeyID(av.Status.TenantTokenKey) {
		t.Error("The pod template must have the ID of the public key.")
	}
	// Only the check container mounts the seed. No field of the pod has the seed or the public key.
	for _, ct := range slices.Concat(pod.Spec.Containers, pod.Spec.InitContainers) {
		mounts := slices.ContainsFunc(ct.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == tokenVolume })
		if mounts != (ct.Name == claimContainer) {
			t.Errorf("Container %s: the mount of the seed is %v.", ct.Name, mounts)
		}
	}
	if text := d.String(); strings.Contains(text, av.Status.TenantTokenKey) || strings.Contains(text, string(s.Data[tokenSeedField])) {
		t.Error("The Deployment must not have the public key or the seed.")
	}

	// A second call uses the same Secret, so the pod template does not change.
	again, err := r.deployment(ctx, av, "")
	if err != nil || again.Annotations[templateHashAnnotation] != d.Annotations[templateHashAnnotation] {
		t.Fatalf("The second call must give the same template: %v", err)
	}
	// After the loss of the Secret, the version gets a new key, and the pod template changes.
	old := av.Status.TenantTokenKey
	if err := c.Delete(ctx, s); err != nil {
		t.Fatal(err)
	}
	lost, err := r.deployment(ctx, av, "")
	if err != nil || av.Status.TenantTokenKey == old || av.Status.TenantTokenKey == "" ||
		lost.Annotations[templateHashAnnotation] == d.Annotations[templateHashAnnotation] {
		t.Fatalf("A new Secret must give a new key and a new template: %v", err)
	}
}

func TestTenantTokenOff(t *testing.T) {
	noClaim, noSwitch := tokenClaim, tokenClaim
	noClaim.TenantClaim, noSwitch.TenantToken = "", false
	for name, claim := range map[string]AppClaimCheck{"no tenant claim": noClaim, "the switch is off": noSwitch,
		"no claim check": {TenantClaim: "tenant", TenantToken: true}} {
		t.Run(name, func(t *testing.T) {
			c, r, av := tokenWorld(t, claim)
			av.Status.TenantTokenKey = "a key from an earlier configuration"
			d, err := r.deployment(t.Context(), av, "")
			if err != nil {
				t.Fatal(err)
			}
			if tokenSecret(t, c, av) != nil || av.Status.TenantTokenKey != "" ||
				strings.Contains(d.String(), "tenant-token") {
				t.Error("With no tenant token, the version has no Secret, no key in the status and no key file.")
			}
		})
	}
}

func TestTenantTokenSecretConflict(t *testing.T) {
	ctx := t.Context()
	for name, secret := range map[string]func(av *corev1beta1.AppVersion) *corev1.Secret{
		"a Secret of a different owner": func(av *corev1beta1.AppVersion) *corev1.Secret {
			return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: av.Namespace, Name: tenantTokenName(av)},
				Data: map[string][]byte{tokenSeedField: make([]byte, tenanttoken.SeedSize)}}
		},
		"a seed that is not valid": func(av *corev1beta1.AppVersion) *corev1.Secret {
			m := appVersionMeta(av)
			m.Name = tenantTokenName(av)
			return &corev1.Secret{ObjectMeta: m, Data: map[string][]byte{tokenSeedField: []byte("short")}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, r, av := tokenWorld(t, tokenClaim)
			if err := c.Create(ctx, secret(av)); err != nil {
				t.Fatal(err)
			}
			if _, err := r.deployment(ctx, av, ""); !errors.Is(err, errTokenSecret) {
				t.Fatalf("The error must be errTokenSecret: %v", err)
			}
			// The version waits, and it makes no object.
			key := client.ObjectKeyFromObject(av)
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			got := &corev1beta1.AppVersion{}
			if err := c.Get(ctx, key, got); err != nil {
				t.Fatal(err)
			}
			cond := meta.FindStatusCondition(got.Status.Conditions, condProvisioned)
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonNameConflict ||
				got.Status.TenantTokenKey != "" {
				t.Errorf("The version must wait with the reason %s: %+v", reasonNameConflict, cond)
			}
			if err := c.Get(ctx, key, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
				t.Errorf("The version must have no Deployment: %v", err)
			}
		})
	}
}

// A template that can give the seed to a container of the user is not valid, and it makes no
// Secret. The token reserves the name of the seed volume. A rule of each pod template refuses a
// reference to the Secret of the seed.
func TestTenantTokenTemplateNames(t *testing.T) {
	_, _, named := tokenWorld(t, tokenClaim)
	secret := corev1.LocalObjectReference{Name: tenantTokenName(named)}
	for name, c := range map[string]struct {
		// tokenRule is true if the rule of the token refuses the template.
		tokenRule bool
		change    func(*corev1.PodSpec)
	}{
		"a volume with the reserved name": {true, func(p *corev1.PodSpec) {
			p.Volumes = []corev1.Volume{{Name: tokenVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
		}},
		"a mount of the seed volume": {true, func(p *corev1.PodSpec) {
			p.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: tokenVolume, MountPath: "/seed"}}
		}},
		"a mount in an init container": {true, func(p *corev1.PodSpec) {
			p.InitContainers = []corev1.Container{{Name: "init-seed", Image: testAgentImage,
				VolumeMounts: []corev1.VolumeMount{{Name: tokenVolume, MountPath: "/seed"}}}}
		}},
		"a volume of the Secret": {false, func(p *corev1.PodSpec) {
			p.Volumes = []corev1.Volume{{Name: "mine", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: secret.Name}}}}
		}},
		"a projected volume of the Secret": {false, func(p *corev1.PodSpec) {
			p.Volumes = []corev1.Volume{{Name: "mine", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: secret}}}}}}}
		}},
		"a variable from the Secret": {false, func(p *corev1.PodSpec) {
			p.Containers[0].Env = []corev1.EnvVar{{Name: "SEED", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: secret, Key: tokenSeedField}}}}
		}},
		"all variables from the Secret": {false, func(p *corev1.PodSpec) {
			p.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: secret}}}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			cl, r, av := tokenWorld(t, tokenClaim)
			c.change(&av.Spec.Template.PodTemplate.Spec)
			_, err := r.deployment(t.Context(), av, "")
			if !isInvalidTemplate(err) || strings.Contains(err.Error(), msgTokenName) != c.tokenRule {
				t.Fatalf("The template must not be valid: %v", err)
			}
			if tokenSecret(t, cl, av) != nil || av.Status.TenantTokenKey != "" {
				t.Error("A template that is not valid must make no Secret and no key.")
			}
			// With no tenant token, the name of the seed volume is not reserved.
			r.Claim.TenantToken = false
			if _, err := r.deployment(t.Context(), av, ""); isInvalidTemplate(err) && strings.Contains(err.Error(), msgTokenName) {
				t.Errorf("With no tenant token, the name must not be reserved: %v", err)
			}
		})
	}
}

// The egress proxy refuses a model call of a version with no key in the status. Thus the status
// has the public key before a pod can get the seed.
func TestTenantTokenKeyIsInTheStatusBeforeThePods(t *testing.T) {
	ctx := t.Context()
	c, r, av := tokenWorld(t, tokenClaim)
	key := client.ObjectKeyFromObject(av)
	var atDeployment []string
	record := func(ctx context.Context, w client.WithWatch) {
		got := &corev1beta1.AppVersion{}
		if w.Get(ctx, key, got) == nil {
			atDeployment = append(atDeployment, got.Status.TenantTokenKey)
		}
	}
	watched := interceptor.NewClient(c, interceptor.Funcs{
		Create: func(ctx context.Context, w client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			if _, ok := o.(*appsv1.Deployment); ok {
				record(ctx, w)
			}
			return w.Create(ctx, o, opts...)
		},
		Update: func(ctx context.Context, w client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if _, ok := o.(*appsv1.Deployment); ok {
				record(ctx, w)
			}
			return w.Update(ctx, o, opts...)
		},
	})
	r.Client = watched
	// The first reconcile adds the finalizer. The next ones make the key and the objects.
	for range 3 {
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		// The fake API does not copy StringData to Data as the API server does.
		sec := &corev1.Secret{}
		if c.Get(ctx, types.NamespacedName{Namespace: av.Namespace, Name: appVersionName(av)}, sec) == nil && len(sec.Data) == 0 {
			sec.Data = map[string][]byte{appKeyField: []byte(sec.StringData[appKeyField])}
			sec.StringData = nil
			if err := c.Update(ctx, sec); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := &corev1beta1.AppVersion{}
	if err := c.Get(ctx, key, got); err != nil || got.Status.TenantTokenKey == "" {
		t.Fatalf("The status must have the key: %v", err)
	}
	if len(atDeployment) == 0 || atDeployment[0] != got.Status.TenantTokenKey {
		t.Fatalf("At the create of the Deployment, the status had the keys %q.", atDeployment)
	}
	first := got.Status.TenantTokenKey

	// After the loss of the Secret, the new key is in the status before the Deployment changes.
	atDeployment = nil
	if err := c.Delete(ctx, tokenSecret(t, c, av)); err != nil {
		t.Fatal(err)
	}
	r = &AppVersionReconciler{Client: watched, Scheme: c.Scheme(), APIReader: c, Gateway: &tokenGateway{},
		GatewayURL: testGatewayURL, Claim: tokenClaim}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, got); err != nil || got.Status.TenantTokenKey == first {
		t.Fatalf("The status must have a new key: %v", err)
	}
	if len(atDeployment) != 1 || atDeployment[0] != got.Status.TenantTokenKey {
		t.Fatalf("At the update of the Deployment, the status had the keys %q.", atDeployment)
	}
}
