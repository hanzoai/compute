// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

// coordinator.go answers ONE money-safety question, identically on every replica:
// "may THIS replica claim a billing lease right now?" It is the single-writer gate
// that makes hourly metering EXACTLY-ONCE under the multi-replica visor Deployment.
//
// WHY THIS EXISTS. The billing leases (MeterLease, BillingLease, CostCursor) live on
// the `_global` coordination DB. Under the Base backend that DB is a POD-LOCAL SQLite
// file, so two replicas each hold their OWN copy: an unguarded insert-once lease wins
// on BOTH files (independent primary-key spaces) and every running machine is debited
// twice an hour. The fix is exactly-once as TWO composed guarantees:
//
//	(1) single-writer gate  — only the HRW-elected owner of "_global" runs the metering
//	    sweep AND performs lease claims; every other replica skips (this file).
//	(2) durable lease history across handoff — a new owner Pulls the prior owner's lease
//	    rows from the object store BEFORE it can re-claim, so the insert-once PK holds
//	    GLOBALLY, not per-pod (store_base.go PullSharedLeases/PushShared, driven by the
//	    claim path in meter_lease.go / billing_lease.go).
//
// The election is the SAME deterministic HRW (Rendezvous) primitive the HA-SQLite
// substrate uses (github.com/hanzoai/vfs/replica) — no coordinator, no lock service,
// no Postgres, no Redis. It is applied to the single coordination key `_global`, so
// every replica computes the same owner from the same live membership set.
//
// FAIL-CLOSED. For a paid product the safe failure is NOT to bill (a missed hour is
// reconciled; a double debit is not). Any uncertainty about ownership — an empty or
// unreadable membership set while running in a cluster — yields "not owner", so the
// replica skips rather than risk a duplicate debit.

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/vfs/replica"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// coordKey is the single coordination resource every replica elects an owner for:
// the `_global` coord DB that holds every billing lease. HRW over this one key names
// exactly one billing owner cluster-wide.
const coordKey = "_global"

// membershipTimeout bounds the pod-list the ownership check makes. The sweep is
// hourly, so a tight timeout costs nothing on the hot path and never wedges a tick.
const membershipTimeout = 5 * time.Second

// membershipSource resolves the live replica set and this replica's own stable ID —
// the two inputs HRW election needs. It is an injectable seam: production wires the
// in-cluster Kubernetes source (k8sMembership); tests substitute a fixed set to force
// owner / non-owner without a cluster.
//
//	self    : this replica's stable ID (its pod name).
//	members : the live writer-eligible replica set, or an error the caller fails
//	          CLOSED on. A nil error with a non-empty set is the only "proceed" signal.
type membershipSource interface {
	self() string
	members(ctx context.Context) ([]replica.Member, error)
}

// membership is the process-wide source, swappable in tests. Defaults to the
// in-cluster Kubernetes source; outside a cluster that source reports single-process
// ownership (there is exactly one writer, so self owns everything).
var membership membershipSource = &k8sMembership{}

// billingOwner reports whether THIS replica is the elected single writer for the
// billing coord DB right now. It is the ONE predicate the ticker gate and every lease
// claim share, so there is exactly one notion of "am I allowed to bill".
//
// Fail-closed: if membership cannot be read, or the live set is empty, this returns
// false and the caller skips (no claim, no debit). The only "true" is a confident
// HRW win over a known, non-empty membership set.
func billingOwner() bool {
	ctx, cancel := context.WithTimeout(context.Background(), membershipTimeout)
	defer cancel()
	members, err := membership.members(ctx)
	if err != nil || len(members) == 0 {
		return false // unknown membership → fail closed (never risk a double debit).
	}
	return replica.IsOwner(coordKey, membership.self(), members)
}

// IsBillingOwner is the exported ownership gate the metering ticker checks before it
// arms an hourly sweep, so non-owner replicas never even enumerate machines or call
// commerce. It is an optimization layered over the authoritative per-claim gate inside
// ClaimMeterHour / ClaimBillingUnit — both consult the SAME predicate, so the ticker
// gate can never admit a claim the lease path would refuse.
func IsBillingOwner() bool { return billingOwner() }

// k8sMembership resolves membership from the Kubernetes API, reusing the SAME
// in-cluster access pattern the autoscaler already uses (rest.InClusterConfig →
// kubernetes.NewForConfig → CoreV1().Pods().List). The visor ServiceAccount already
// carries the pods:[list,watch] grant (k8s/rbac.yaml), so no new RBAC is required.
//
// selfID is the pod name (stable for the pod's life), taken from POD_NAME (Downward
// API) or the container hostname, which in a Deployment pod IS the pod name.
type k8sMembership struct {
	once   sync.Once
	client kubernetes.Interface // nil ⇒ not in a cluster (single-process / local dev)
	ns     string
}

// k8sAppLabel selects the visor replica set — matches the Deployment pod label
// (k8s/deployment.yaml: app: visor). Every writer-eligible replica carries it.
const k8sAppLabel = "app=visor"

func (m *k8sMembership) init() {
	m.once.Do(func() {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			// Not in a cluster: local dev / a single standalone process. There is
			// exactly one writer, so leave client nil and report single-process
			// ownership below. This is SAFE precisely because it is a single process.
			return
		}
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return
		}
		m.client = client
		m.ns = podNamespace()
	})
}

func (m *k8sMembership) self() string { return selfID() }

// members returns the live, writer-eligible replica set. Outside a cluster (client
// nil) it returns a single-member set of self — the sole process is the sole writer.
// Inside a cluster it lists Running+Ready visor pods; a list error is surfaced so the
// caller fails CLOSED (an in-cluster replica that cannot see its peers must NOT assume
// it is the owner).
func (m *k8sMembership) members(ctx context.Context) ([]replica.Member, error) {
	m.init()
	if m.client == nil {
		// Single-process mode: one writer, itself. Never an error (no cluster to fail
		// closed against).
		return []replica.Member{{ID: selfID()}}, nil
	}
	list, err := m.client.CoreV1().Pods(m.ns).List(ctx, metav1.ListOptions{LabelSelector: k8sAppLabel})
	if err != nil {
		return nil, err // fail closed: peers unknown.
	}
	members := make([]replica.Member, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		if podWriterEligible(p) {
			members = append(members, replica.Member{ID: p.Name, Addr: p.Status.PodIP})
		}
	}
	return members, nil
}

// podWriterEligible reports whether a pod is a live writer candidate: Running and
// Ready, not terminating. A Pending/terminating/unready pod is excluded so a rolling
// pod that is coming up or going down never skews the election toward a replica that
// cannot actually run the sweep.
func podWriterEligible(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil {
		return false // terminating.
	}
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false // no Ready condition yet.
}

// selfID is this replica's stable identity for HRW weighting: POD_NAME (Downward API)
// when set, else the container hostname (which is the pod name in a Deployment pod).
// A stable, unique-per-replica string is all HRW needs.
func selfID() string {
	if n := strings.TrimSpace(os.Getenv("POD_NAME")); n != "" {
		return n
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "self" // last-resort constant: in single-process mode the value only
		// has to be stable within the process, and there is one member.
	}
	return h
}

// podNamespace resolves the pod's own namespace for the peer list: POD_NAMESPACE
// (Downward API) when set, else the standard in-cluster service-account namespace
// file, else the visor default namespace.
func podNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(b)); ns != "" {
			return ns
		}
	}
	return "hanzo"
}
