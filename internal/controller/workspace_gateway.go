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
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

const (
	// workspaceFinalizer keeps the Workspace until the controller blocks its gateway key.
	workspaceFinalizer = "koodam.dev/workspace-gateway"
	// workspaceGatewayDir has one file for each field of the gateway Secret.
	workspaceGatewayDir = "/etc/koodam/gateway"
	// DefaultWorkspaceKeyTTL is the TTL of a Workspace key. The controller renews the key after
	// half of the TTL.
	DefaultWorkspaceKeyTTL = 24 * time.Hour

	// condGatewayKey tells the state of the gateway key. The reason "Revoking" is in the status
	// before the block, so a retry finishes the revoke and does not renew a blocked key.
	condGatewayKey = "GatewayKey"
	reasonRevoking = "Revoking"
)

func gatewaySecretName(ws *corev1alpha1.Workspace) string { return ws.Name + "-gateway" }

// wantKey is true if the pod must have a gateway key now.
func wantKey(ws *corev1alpha1.Workspace) bool {
	return ws.Spec.Gateway != nil && !ws.Spec.Suspend && ws.DeletionTimestamp.IsZero()
}

func (r *WorkspaceReconciler) keyTTL() time.Duration {
	if r.KeyTTL > 0 {
		return r.KeyTTL
	}
	return DefaultWorkspaceKeyTTL
}

// readKey reads the gateway Secret. It returns nil if there is no Secret, and errNotOwned if
// the Secret is not from ws.
func (r *WorkspaceReconciler) readKey(ctx context.Context, ws *corev1alpha1.Workspace) (*corev1.Secret, error) {
	s := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: gatewaySecretName(ws)}, s)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(s, ws) || len(s.Data[keyField]) == 0 {
		return nil, errNotOwned
	}
	return s, nil
}

// applyKey makes or renews the gateway key if the pod must have one. Otherwise it revokes the
// key. It runs before the StatefulSet changes, so a suspend blocks the key before the pod stops.
// The key value goes only to the Secret: never to a log, an event, the status or an error.
func (r *WorkspaceReconciler) applyKey(ctx context.Context, ws *corev1alpha1.Workspace) error {
	if ws.Spec.Gateway == nil && ws.Status.KeyHash == "" && !controllerutil.ContainsFinalizer(ws, workspaceFinalizer) {
		return nil
	}
	secret, err := r.readKey(ctx, ws)
	owned := !errors.Is(err, errNotOwned)
	if owned && err != nil {
		return err
	}
	key, model := "", ""
	if secret != nil {
		key, model = string(secret.Data[keyField]), string(secret.Data[modelField])
	}
	c := meta.FindStatusCondition(ws.Status.Conditions, condGatewayKey)
	revoking := c != nil && c.Reason == reasonRevoking
	g := ws.Spec.Gateway
	stale := revoking || (ws.Status.KeyHash != "" && gateway.HashKey(key) != ws.Status.KeyHash) ||
		(g != nil && key != "" && model != g.Model)
	if !wantKey(ws) || stale {
		if err := r.revokeKey(ctx, ws, secret); err != nil {
			return err
		}
		if !wantKey(ws) {
			if (g == nil || !ws.DeletionTimestamp.IsZero()) && controllerutil.RemoveFinalizer(ws, workspaceFinalizer) {
				return r.updateMeta(ctx, ws)
			}
			return nil
		}
		key = ""
	}
	if !owned {
		return errNotOwned
	}
	if controllerutil.AddFinalizer(ws, workspaceFinalizer) {
		if err := r.updateMeta(ctx, ws); err != nil {
			return err
		}
	}
	if key == "" {
		if key, err = gateway.NewKeyValue(); err != nil {
			return err
		}
		s := &corev1.Secret{ObjectMeta: workspaceMeta(ws, gatewaySecretName(ws)), Immutable: new(true),
			StringData: gatewayEnv(r.GatewayURL, g.Model, key)}
		if err := r.Create(ctx, s); err != nil {
			return err
		}
	}
	if ws.Status.KeyHash == "" {
		// Write the hash before the key exists, so that a revoke can always find the key.
		hash := gateway.HashKey(key)
		ws.Status.KeyAlias = fmt.Sprintf("ws-%s-%s-%s", ws.Namespace, ws.Name, hash[:8])
		ws.Status.KeyHash = hash
		if err := r.Status().Update(ctx, ws); err != nil {
			return err
		}
	}
	spec := gateway.KeySpec{Value: key, Alias: ws.Status.KeyAlias, Model: g.Model,
		Budget: g.BudgetPerDay.AsApproximateFloat64(), BudgetPeriod: 24 * time.Hour, TTL: r.keyTTL(),
		Metadata: map[string]string{"uid": string(ws.UID), "namespace": ws.Namespace, "workspace": ws.Name}}
	// A renew of a key that is not in the gateway, after a crash before CreateKey, makes the key.
	err = r.Gateway.RenewKey(ctx, spec)
	if errors.Is(err, gateway.ErrKeyNotFound) {
		err = r.Gateway.CreateKey(ctx, spec)
	}
	if err != nil {
		return err
	}
	meta.SetStatusCondition(&ws.Status.Conditions, metav1.Condition{Type: condGatewayKey,
		Status: metav1.ConditionTrue, Reason: "Active", Message: "The gateway key is ready.",
		ObservedGeneration: ws.Generation})
	return nil
}

// updateMeta writes the finalizers. It keeps the status in memory, which Reconcile writes later,
// because Update gives back the status of the API server.
func (r *WorkspaceReconciler) updateMeta(ctx context.Context, ws *corev1alpha1.Workspace) error {
	st := ws.Status.DeepCopy()
	err := r.Update(ctx, ws)
	ws.Status = *st
	return err
}

// revokeKey blocks the key first, then reads the usage, deletes the Secret and clears the key
// from the status. secret is the gateway Secret of ws, or nil. Each step can run again.
func (r *WorkspaceReconciler) revokeKey(ctx context.Context, ws *corev1alpha1.Workspace, secret *corev1.Secret) error {
	hash := ws.Status.KeyHash
	if hash == "" && secret != nil {
		// The Secret exists, but the status write before CreateKey did not complete.
		hash = gateway.HashKey(string(secret.Data[keyField]))
	}
	if hash == "" {
		return nil
	}
	cond := metav1.Condition{Type: condGatewayKey, Status: metav1.ConditionFalse, Reason: reasonRevoking,
		Message: "The revoke of the gateway key started.", ObservedGeneration: ws.Generation}
	if c := meta.FindStatusCondition(ws.Status.Conditions, condGatewayKey); c == nil || c.Reason != reasonRevoking {
		meta.SetStatusCondition(&ws.Status.Conditions, cond)
		if err := r.Status().Update(ctx, ws); err != nil {
			return err
		}
	}
	if err := r.Gateway.BlockKey(ctx, hash); err != nil {
		return err
	}
	cond.Reason, cond.Message = "Revoked", "The gateway key is blocked and its Secret is deleted."
	u, err := r.Gateway.Usage(ctx, hash)
	switch {
	case errors.Is(err, gateway.ErrKeyNotFound):
		cond.Message = "The gateway had no key for this Workspace."
	case errors.Is(err, gateway.ErrSpendNotSettled), err == nil:
		if err != nil {
			cond.Message += " The spend did not settle, so the usage can be low."
		}
		ws.Status.Usage = &corev1alpha1.WorkspaceUsage{Spend: strconv.FormatFloat(u.Spend, 'f', -1, 64),
			Calls: u.Calls, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
			Source: corev1alpha1.UsageFromGateway}
	default:
		return err
	}
	if secret != nil {
		// The UID precondition stops the delete if the Secret changed after the read.
		uid := secret.UID
		if err := r.Delete(ctx, secret, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	logf.FromContext(ctx).Info("Revoked the gateway key", "keyAlias", ws.Status.KeyAlias)
	ws.Status.KeyAlias, ws.Status.KeyHash = "", ""
	meta.SetStatusCondition(&ws.Status.Conditions, cond)
	return r.Status().Update(ctx, ws)
}
