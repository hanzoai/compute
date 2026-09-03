// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type MachineAwsClient struct {
	Client *ec2.Client
	cfg    aws.Config
	region string
	// carried is true when this process holds no key: the carrier signs, so the
	// SDK sends unsigned, and anything that needs the key HERE (an STS presign)
	// says so instead of producing an unsigned URL.
	carried bool
}

// newMachineAwsClient builds every AWS client over the carried transport. With a
// key it signs here; with none it sends anonymously and egress signs.
func newMachineAwsClient(accessKeyId string, accessKeySecret string, region string, hc *http.Client) (MachineAwsClient, error) {
	carried := accessKeyId == "" && accessKeySecret == ""
	var creds aws.CredentialsProvider = aws.AnonymousCredentials{}
	if !carried {
		creds = aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: accessKeyId, SecretAccessKey: accessKeySecret}, nil
		})
	}
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithHTTPClient(hc),
		config.WithCredentialsProvider(creds),
	)
	if err != nil {
		return MachineAwsClient{}, err
	}
	return MachineAwsClient{Client: ec2.NewFromConfig(cfg), cfg: cfg, region: region, carried: carried}, nil
}

// Kubernetes is EKS over the SAME config this machine client signs with: one
// credential, one transport, two nouns — the rule DigitalOcean set.
func (c MachineAwsClient) Kubernetes() KubernetesClientInterface {
	return &EKSClient{
		eks:     eks.NewFromConfig(c.cfg),
		iam:     iam.NewFromConfig(c.cfg),
		ec2:     c.Client,
		sts:     sts.NewPresignClient(sts.NewFromConfig(c.cfg)),
		region:  c.region,
		carried: c.carried,
	}
}

func getMachineFromAwsInstance(instance ec2Types.Instance) *Machine {
	machine := &Machine{
		Name:        *instance.InstanceId,
		Id:          *instance.InstanceId,
		Region:      *instance.Placement.AvailabilityZone,
		DisplayName: *instance.InstanceId,
	}

	if instance.InstanceType != "" {
		machine.Size = string(instance.InstanceType)
	}

	if len(instance.Tags) > 0 {
		for _, tag := range instance.Tags {
			machine.Tag += fmt.Sprintf("%s=%s,", *tag.Key, *tag.Value)
		}
	}

	machine.State = string(instance.State.Name)

	return machine
}

func (client MachineAwsClient) GetMachines() ([]*Machine, error) {
	input := &ec2.DescribeInstancesInput{}
	output, err := client.Client.DescribeInstances(context.TODO(), input)
	if err != nil {
		return nil, err
	}

	machines := []*Machine{}
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			machine := getMachineFromAwsInstance(instance)
			machines = append(machines, machine)
		}
	}

	return machines, nil
}

func (client MachineAwsClient) GetMachine(name string) (*Machine, error) {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{name},
	}

	output, err := client.Client.DescribeInstances(context.TODO(), input)
	if err != nil {
		return nil, err
	}

	if len(output.Reservations) == 0 || len(output.Reservations[0].Instances) == 0 {
		return nil, fmt.Errorf("Instance not found: %s", name)
	}

	instance := output.Reservations[0].Instances[0]
	return getMachineFromAwsInstance(instance), nil
}

func (client MachineAwsClient) UpdateMachineState(name string, state string) (bool, string, error) {
	switch state {
	case "Running":
		input := &ec2.StartInstancesInput{
			InstanceIds: []string{name},
		}
		_, err := client.Client.StartInstances(context.TODO(), input)
		if err != nil {
			return false, "", err
		}
	case "Stopped":
		input := &ec2.StopInstancesInput{
			InstanceIds: []string{name},
		}
		_, err := client.Client.StopInstances(context.TODO(), input)
		if err != nil {
			return false, "", err
		}
	default:
		return false, fmt.Sprintf("Unsupported state: %s", state), nil
	}

	return true, fmt.Sprintf("Instance: [%s]'s state has been successfully updated to: [%s]", name, state), nil
}

func (client MachineAwsClient) CreateMachine(spec *CreateMachineSpec) (*Machine, error) {
	if spec.ImageID == "" {
		return nil, fmt.Errorf("imageId is required for AWS instance creation")
	}

	instanceType := ec2Types.InstanceType(spec.InstanceType)
	if spec.InstanceType == "" {
		instanceType = ec2Types.InstanceTypeT3Medium
	}

	// Build tags
	var tags []ec2Types.Tag
	tags = append(tags, ec2Types.Tag{
		Key:   aws.String("Name"),
		Value: aws.String(spec.DisplayName),
	})
	if spec.OS != "" {
		tags = append(tags, ec2Types.Tag{
			Key:   aws.String("OS"),
			Value: aws.String(spec.OS),
		})
	}
	tags = append(tags, ec2Types.Tag{
		Key:   aws.String("ManagedBy"),
		Value: aws.String("hanzo-visor"),
	})
	for k, v := range spec.Tags {
		tags = append(tags, ec2Types.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	input := &ec2.RunInstancesInput{
		ImageId:      aws.String(spec.ImageID),
		InstanceType: instanceType,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		TagSpecifications: []ec2Types.TagSpecification{
			{
				ResourceType: ec2Types.ResourceTypeInstance,
				Tags:         tags,
			},
		},
	}

	output, err := client.Client.RunInstances(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("failed to launch EC2 instance: %w", err)
	}

	if len(output.Instances) == 0 {
		return nil, fmt.Errorf("RunInstances returned no instances")
	}

	return getMachineFromAwsInstance(output.Instances[0]), nil
}
