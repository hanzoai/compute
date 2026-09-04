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

package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lsTypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

type MachineLightsailClient struct {
	Client *lightsail.Client
	region string
}

func newMachineLightsailClient(accessKeyId string, accessKeySecret string, region string) (MachineLightsailClient, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     accessKeyId,
				SecretAccessKey: accessKeySecret,
			}, nil
		})),
	)
	if err != nil {
		return MachineLightsailClient{}, err
	}

	client := lightsail.NewFromConfig(cfg)
	return MachineLightsailClient{Client: client, region: region}, nil
}

func getMachineFromLightsailInstance(inst lsTypes.Instance) *Machine {
	machine := &Machine{
		Name:        aws.ToString(inst.Name),
		Id:          aws.ToString(inst.Name),
		DisplayName: aws.ToString(inst.Name),
		State:       "Unknown",
	}

	if inst.Location != nil {
		machine.Region = aws.ToString(inst.Location.AvailabilityZone)
	}

	if inst.BundleId != nil {
		machine.Size = aws.ToString(inst.BundleId)
	}

	if inst.BlueprintId != nil {
		machine.Image = aws.ToString(inst.BlueprintId)
	}

	if inst.BlueprintName != nil {
		machine.Os = aws.ToString(inst.BlueprintName)
	}

	if inst.PublicIpAddress != nil {
		machine.PublicIp = aws.ToString(inst.PublicIpAddress)
	}

	if inst.PrivateIpAddress != nil {
		machine.PrivateIp = aws.ToString(inst.PrivateIpAddress)
	}

	if inst.Hardware != nil {
		machine.CpuSize = strconv.Itoa(int(aws.ToInt32(inst.Hardware.CpuCount)))
		machine.MemSize = fmt.Sprintf("%.0f", aws.ToFloat32(inst.Hardware.RamSizeInGb))
	}

	if inst.State != nil && inst.State.Name != nil {
		switch aws.ToString(inst.State.Name) {
		case "running":
			machine.State = "Running"
		case "stopped":
			machine.State = "Stopped"
		case "pending":
			machine.State = "Starting"
		case "stopping":
			machine.State = "Stopping"
		default:
			machine.State = aws.ToString(inst.State.Name)
		}
	}

	for _, tag := range inst.Tags {
		machine.Tag += fmt.Sprintf("%s=%s,", aws.ToString(tag.Key), aws.ToString(tag.Value))
	}

	return machine
}

func (client MachineLightsailClient) GetMachines() ([]*Machine, error) {
	var allMachines []*Machine
	var pageToken *string

	for {
		input := &lightsail.GetInstancesInput{
			PageToken: pageToken,
		}
		output, err := client.Client.GetInstances(context.TODO(), input)
		if err != nil {
			return nil, err
		}

		for _, inst := range output.Instances {
			allMachines = append(allMachines, getMachineFromLightsailInstance(inst))
		}

		if output.NextPageToken == nil {
			break
		}
		pageToken = output.NextPageToken
	}

	return allMachines, nil
}

func (client MachineLightsailClient) GetMachine(name string) (*Machine, error) {
	input := &lightsail.GetInstanceInput{
		InstanceName: aws.String(name),
	}

	output, err := client.Client.GetInstance(context.TODO(), input)
	if err != nil {
		return nil, err
	}

	if output.Instance == nil {
		return nil, fmt.Errorf("Lightsail instance not found: %s", name)
	}

	return getMachineFromLightsailInstance(*output.Instance), nil
}

func (client MachineLightsailClient) UpdateMachineState(name string, state string) (bool, string, error) {
	switch state {
	case "Running":
		_, err := client.Client.StartInstance(context.TODO(), &lightsail.StartInstanceInput{
			InstanceName: aws.String(name),
		})
		if err != nil {
			return false, "", err
		}
	case "Stopped":
		_, err := client.Client.StopInstance(context.TODO(), &lightsail.StopInstanceInput{
			InstanceName: aws.String(name),
		})
		if err != nil {
			return false, "", err
		}
	case "Rebooting":
		_, err := client.Client.RebootInstance(context.TODO(), &lightsail.RebootInstanceInput{
			InstanceName: aws.String(name),
		})
		if err != nil {
			return false, "", err
		}
	default:
		return false, fmt.Sprintf("Unsupported state: %s", state), nil
	}

	return true, fmt.Sprintf("Lightsail instance: [%s]'s state has been successfully updated to: [%s]", name, state), nil
}

func (client MachineLightsailClient) CreateMachine(spec *CreateMachineSpec) (*Machine, error) {
	if spec.ImageID == "" {
		return nil, fmt.Errorf("imageId (blueprintId) is required for Lightsail instance creation")
	}

	bundleId := spec.InstanceType
	if bundleId == "" {
		bundleId = "nano_3_0"
	}

	region := spec.Region
	if region == "" {
		region = client.region
	}

	// Build tags
	var tags []lsTypes.Tag
	tags = append(tags, lsTypes.Tag{
		Key:   aws.String("Name"),
		Value: aws.String(spec.DisplayName),
	})
	if spec.OS != "" {
		tags = append(tags, lsTypes.Tag{
			Key:   aws.String("OS"),
			Value: aws.String(spec.OS),
		})
	}
	tags = append(tags, lsTypes.Tag{
		Key:   aws.String("ManagedBy"),
		Value: aws.String(managedBy),
	})
	for k, v := range spec.Tags {
		tags = append(tags, lsTypes.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	input := &lightsail.CreateInstancesInput{
		InstanceNames:    []string{spec.Name},
		AvailabilityZone: aws.String(region + "a"),
		BlueprintId:      aws.String(spec.ImageID),
		BundleId:         aws.String(bundleId),
		Tags:             tags,
	}

	output, err := client.Client.CreateInstances(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("failed to create Lightsail instance: %w", err)
	}

	if len(output.Operations) == 0 {
		return nil, fmt.Errorf("CreateInstances returned no operations")
	}

	// Fetch the created instance to get full details
	inst, err := client.GetMachine(spec.Name)
	if err != nil {
		// Instance is being created but may not be immediately available.
		// Return a minimal machine object from the spec.
		return &Machine{
			Name:        spec.Name,
			Id:          spec.Name,
			DisplayName: spec.DisplayName,
			Region:      region,
			Size:        bundleId,
			Image:       spec.ImageID,
			Os:          spec.OS,
			State:       "Starting",
		}, nil
	}

	return inst, nil
}
