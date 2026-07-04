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

// nebius.go is the Nebius provider adapter — a MachineClientInterface over Nebius
// AI Cloud's compute API (the official gosdk, gRPC + IAM token auth). It follows
// the same shape as the AWS and GCP adapters: a client struct built from the
// stored BYOC credentials, and the four interface methods over the provider's
// compute instance service. The read/manage plane (list/get/start/stop) is fully
// wired; instance creation is gated on the provider-specific network + boot-disk
// wiring the generic CreateMachineSpec does not carry (the same posture the GCP
// adapter takes), with an actionable error rather than a silent stub.
package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nebius/gosdk"
	compute "github.com/nebius/gosdk/proto/nebius/compute/v1"
)

// MachineNebiusClient talks to Nebius compute. parentID is the Nebius project
// (the List parent scope); region is retained for placement on create. The gosdk
// SDK holds the authenticated gRPC connection pool.
type MachineNebiusClient struct {
	sdk      *gosdk.SDK
	parentID string
	region   string
}

// newMachineNebiusClient builds a Nebius client from BYOC credentials, mapping the
// provider's generic (accessKeyId, accessKeySecret, region) triple onto Nebius'
// identifiers: accessKeyId is the Nebius PROJECT id (the parent scope for list),
// accessKeySecret is a Nebius IAM bearer token, and region is the placement
// region. The IAM token is the only secret and, per Hanzo policy, is sourced from
// the KMS-synced Provider credential — never hard-coded. An empty token fails
// closed so an unconfigured provider never issues unauthenticated calls.
func newMachineNebiusClient(accessKeyId string, accessKeySecret string, region string) (MachineNebiusClient, error) {
	token := strings.TrimSpace(accessKeySecret)
	if token == "" {
		return MachineNebiusClient{}, fmt.Errorf("nebius: IAM token is required (BYOC credential unset)")
	}
	sdk, err := gosdk.New(context.Background(), gosdk.WithCredentials(gosdk.IAMToken(token)))
	if err != nil {
		return MachineNebiusClient{}, fmt.Errorf("nebius: init sdk: %w", err)
	}
	return MachineNebiusClient{sdk: sdk, parentID: strings.TrimSpace(accessKeyId), region: region}, nil
}

// nebiusState normalizes a Nebius instance state onto visor's canonical vocabulary
// (the same "Running"/"Stopped"/… set the other providers emit), so the reconcile
// and metering layers read one state language regardless of provider.
func nebiusState(s compute.InstanceStatus_InstanceState) string {
	switch s {
	case compute.InstanceStatus_RUNNING:
		return "Running"
	case compute.InstanceStatus_STOPPED:
		return "Stopped"
	case compute.InstanceStatus_CREATING, compute.InstanceStatus_STARTING:
		return "Starting"
	case compute.InstanceStatus_STOPPING:
		return "Stopping"
	case compute.InstanceStatus_DELETING:
		return "Deleting"
	case compute.InstanceStatus_ERROR:
		return "Error"
	default:
		return "Unknown"
	}
}

// getMachineFromNebiusInstance maps a Nebius Instance onto visor's Machine. Labels
// are joined into the comma-separated Tag string in the SAME "k=v," shape the AWS
// and GCP adapters use, so tag-driven consumers are provider-agnostic.
func getMachineFromNebiusInstance(inst *compute.Instance) *Machine {
	md := inst.GetMetadata()
	machine := &Machine{
		Name:        md.GetId(),
		Id:          md.GetId(),
		DisplayName: md.GetName(),
		Region:      md.GetParentId(),
		State:       nebiusState(inst.GetStatus().GetState()),
	}
	if ts := md.GetCreatedAt(); ts != nil {
		machine.CreatedTime = ts.AsTime().Local().Format("2006-01-02T15:04:05Z07:00")
	}
	for k, v := range md.GetLabels() {
		machine.Tag += fmt.Sprintf("%s=%s,", k, v)
	}
	return machine
}

func (client MachineNebiusClient) GetMachines() ([]*Machine, error) {
	if client.parentID == "" {
		return nil, fmt.Errorf("nebius: project id (parent) is required to list instances")
	}
	ctx := context.Background()
	svc := client.sdk.Services().Compute().V1().Instance()
	machines := []*Machine{}
	pageToken := ""
	for {
		resp, err := svc.List(ctx, &compute.ListInstancesRequest{
			ParentId:  client.parentID,
			PageSize:  200,
			PageToken: pageToken,
		})
		if err != nil {
			return nil, fmt.Errorf("nebius: list instances: %w", err)
		}
		for _, inst := range resp.GetItems() {
			machines = append(machines, getMachineFromNebiusInstance(inst))
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	return machines, nil
}

func (client MachineNebiusClient) GetMachine(name string) (*Machine, error) {
	svc := client.sdk.Services().Compute().V1().Instance()
	inst, err := svc.Get(context.Background(), &compute.GetInstanceRequest{Id: name})
	if err != nil {
		return nil, fmt.Errorf("nebius: get instance %q: %w", name, err)
	}
	return getMachineFromNebiusInstance(inst), nil
}

func (client MachineNebiusClient) UpdateMachineState(name string, state string) (bool, string, error) {
	ctx := context.Background()
	svc := client.sdk.Services().Compute().V1().Instance()
	switch state {
	case "Running":
		if _, err := svc.Start(ctx, &compute.StartInstanceRequest{Id: name}); err != nil {
			return false, "", fmt.Errorf("nebius: start instance %q: %w", name, err)
		}
	case "Stopped":
		if _, err := svc.Stop(ctx, &compute.StopInstanceRequest{Id: name}); err != nil {
			return false, "", fmt.Errorf("nebius: stop instance %q: %w", name, err)
		}
	default:
		return false, fmt.Sprintf("Unsupported state: %s", state), nil
	}
	return true, fmt.Sprintf("Instance: [%s]'s state has been successfully updated to: [%s]", name, state), nil
}

// CreateMachine is gated, not stubbed: a Nebius instance requires a boot disk
// created from a source image AND a network interface bound to a subnet — neither
// of which the generic CreateMachineSpec carries (it has no Nebius subnet id and
// no disk-provisioning shape). Rather than fabricate a partial instance that
// cannot boot, it returns an actionable error naming exactly the wiring needed, in
// the same posture as the GCP adapter. When the resell/BYOC spec grows a
// network+boot-disk descriptor, the real create slots in here behind this contract.
func (client MachineNebiusClient) CreateMachine(spec *CreateMachineSpec) (*Machine, error) {
	return nil, fmt.Errorf("nebius: CreateMachine requires a boot-disk source image and a subnet-bound network interface not present on the generic spec; provision via the Nebius disk+network descriptor to enable")
}
