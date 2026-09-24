// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

// ec2.go is Hanzo's own AWS EC2 account: where every org's hosted machines run.
//
// This process holds nothing that can sign an AWS request. Every EC2 call goes
// through hanzoai/egress (the registered carrier): the SDK builds the request
// with anonymous credentials, egress reads the account's descriptor from KMS
// (the role hanzo-compute, assumed with egress's own IAM identity), signs the
// request, sends it, and returns AWS's answer. Without egress, hosted compute
// refuses; there is no direct call. What stays here is configuration — region,
// subnet, security group, images — read by the key names below, and a call
// refuses, naming what is missing, rather than guess any of it.
//
// Every instance and root volume carries the org and the machine id that own it
// as tags, and every read filters on those tags, so one org never sees, stops or
// terminates another's machine.

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/hanzoai/egress/spend"

	"github.com/hanzoai/compute/conf"
	"github.com/hanzoai/compute/logs"
)

// The hosted account's configuration keys, read from app.conf (or an
// environment variable of the same name, which wins).
const (
	keyRegion        = "computeRegion"
	keySubnet        = "computeSubnet"
	keySecurityGroup = "computeSecurityGroup"
	keyImage         = "computeImage"
	keyGPUImage      = "computeGpuImage"
)

// envOf names the variable the repository's app.conf expands each key from.
var envOf = map[string]string{
	keyRegion:        "COMPUTE_REGION",
	keySubnet:        "COMPUTE_SUBNET",
	keySecurityGroup: "COMPUTE_SECURITY_GROUP",
	keyImage:         "COMPUTE_IMAGE",
	keyGPUImage:      "COMPUTE_GPU_IMAGE",
}

// hostedLabel is the account egress spends for hosted compute: the label of its
// descriptor in KMS, cloud/aws/hanzo-compute/credential.
const hostedLabel = "hanzo-compute"

// Tags every hosted instance and volume carries besides the tenant scope
// (orgTagKey, projectTagKey, appTagKey, kindTagKey).
const (
	machineTagKey = "hanzo-machine"
	managedByKey  = "managed-by"
	managedBy     = "hanzo-compute"
	nameTagKey    = "Name"
)

// hostedAccount is the hosted account as configured.
type hostedAccount struct {
	Region        string
	Subnet        string
	SecurityGroup string
	Image         string
	GPUImage      string
}

// hostedConfig reads the hosted account's configuration. It is read on every
// use, so a key set on a running process takes effect on the next call.
func hostedConfig() hostedAccount {
	get := func(key string) string { return strings.TrimSpace(conf.GetConfigString(key)) }
	return hostedAccount{
		Region:        get(keyRegion),
		Subnet:        get(keySubnet),
		SecurityGroup: get(keySecurityGroup),
		Image:         get(keyImage),
		GPUImage:      get(keyGPUImage),
	}
}

// unset names each key whose value is empty, with the variable it is read from.
func unset(pairs ...[2]string) []string {
	var out []string
	for _, kv := range pairs {
		if kv[0] == "" {
			out = append(out, kv[1]+" ("+envOf[kv[1]]+")")
		}
	}
	return out
}

// image is the machine image a launch of o boots, and its key: the GPU image for
// a GPU size, the default image for every other.
func (a hostedAccount) image(o offer) (string, string) {
	if o.gpu != nil {
		return a.GPUImage, keyGPUImage
	}
	return a.Image, keyImage
}

// launchable reports what the account is missing to launch o, naming each key.
func (a hostedAccount) launchable(o offer) error {
	image, imageKey := a.image(o)
	missing := unset(
		[2]string{a.Region, keyRegion},
		[2]string{a.Subnet, keySubnet},
		[2]string{a.SecurityGroup, keySecurityGroup},
		[2]string{image, imageKey},
	)
	if len(missing) > 0 {
		return fmt.Errorf("hosted compute cannot launch %s: %s not set", o.slug, strings.Join(missing, ", "))
	}
	if !carrierRegistered() {
		return errNoEgress
	}
	return nil
}

// errNoRegion is the account with no region: there is nothing to reach.
var errNoRegion = fmt.Errorf("hosted compute is not configured: %s (%s) is not set", keyRegion, envOf[keyRegion])

// errNoEgress is a configured account with no egress to reach it through. It is
// a refusal, not a reason to call AWS directly: this process holds nothing that
// signs, and is meant to.
var errNoEgress = errors.New("hosted compute needs egress: egressAddress is not set")

// hostedEC2 returns an EC2 client for the hosted account and the region it
// serves. The client is built from explicit options — the region, the carrier's
// http.Client, anonymous credentials — and never from the environment, so no
// AWS key, profile, endpoint override or metadata service this pod can see is
// consulted, and the SDK signs nothing: egress signs.
//
// It is built per operation because the carrier's client carries this
// process's IAM token as it stood when it was built.
func hostedEC2(ctx context.Context) (*ec2.Client, string, error) {
	region := hostedConfig().Region
	if region == "" {
		return nil, "", errNoRegion
	}
	if !carrierRegistered() {
		return nil, "", errNoEgress
	}
	hc, err := carried(Credential{Provider: "AWS", Name: hostedLabel, Region: region})
	if err != nil {
		return nil, "", fmt.Errorf("hosted compute: %w", err)
	}
	return carriedEC2(hc, region), region, nil
}

// carriedEC2 is an EC2 client whose every request goes out through hc unsigned.
// The SDK records anonymous credentials as none at all, and signs nothing.
//
// A refusal from egress is final rather than retried: it is egress deciding —
// no descriptor, a role it cannot assume, a caller it does not know — not a
// connection that dropped, and asking twice more only spends the caller's rate.
func carriedEC2(hc *http.Client, region string) *ec2.Client {
	return ec2.New(ec2.Options{
		Region:      region,
		HTTPClient:  hc,
		Credentials: aws.AnonymousCredentials{},
		Retryer: retry.NewStandard(func(o *retry.StandardOptions) {
			o.Retryables = append([]retry.IsErrorRetryable{final}, o.Retryables...)
		}),
	})
}

// final marks an egress refusal as not worth retrying, and says nothing of any
// other error.
var final = retry.IsErrorRetryableFunc(func(err error) aws.Ternary {
	if errors.Is(err, spend.ErrRefused) {
		return aws.FalseTernary
	}
	return aws.UnknownTernary
})

// ---- machine ids ----

// machineIDPattern is a hosted machine id: "m-" and twenty hex digits. Checking
// it before an id reaches a filter keeps EC2's filter wildcards (* and ?) out of
// every lookup.
var machineIDPattern = regexp.MustCompile(`^m-[0-9a-f]{20}$`)

// mintMachineID names a new hosted machine. The id is the machine's address on
// /v1/machines/{org}/{id}, its hanzo-machine tag, the idempotency token of its
// launch and the key of its launch debit.
func mintMachineID() string {
	var b [10]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return "m-" + hex.EncodeToString(b[:])
}

// validFilterValue reports whether s can be an EC2 filter value that matches
// exactly: an empty value or a wildcard would widen the match past one tenant.
func validFilterValue(s string) bool {
	return s != "" && !strings.ContainsAny(s, `*?\`)
}

// ---- reads ----

// pageSize is how many instances one DescribeInstances answer holds.
const pageSize = 50

// liveStates are the instance states a machine exists in. A terminated instance
// stays visible for about an hour after termination and is not a machine.
var liveStates = []string{"pending", "running", "stopping", "stopped", "shutting-down"}

func filter(name string, values ...string) ec2Types.Filter {
	return ec2Types.Filter{Name: aws.String(name), Values: values}
}

// describe returns every instance matching filters, across pages. A page is
// fifty instances: an instance's XML runs to several kilobytes once it carries
// network interfaces, volumes and tags, and fifty keeps one answer far under the
// megabyte egress reads.
func describe(ctx context.Context, api *ec2.Client, filters ...ec2Types.Filter) ([]ec2Types.Instance, error) {
	pages := ec2.NewDescribeInstancesPaginator(api, &ec2.DescribeInstancesInput{
		Filters:    filters,
		MaxResults: aws.Int32(pageSize),
	})
	var out []ec2Types.Instance
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Reservations {
			out = append(out, r.Instances...)
		}
	}
	return out, nil
}

// findInstance returns org's instance for machine id, or nil. The tags are
// checked again on the answer, so the result belongs to org whatever the filter
// matched.
func findInstance(ctx context.Context, api *ec2.Client, org, id string) (*ec2Types.Instance, error) {
	if !machineIDPattern.MatchString(id) || !validFilterValue(org) {
		return nil, nil
	}
	found, err := describe(ctx, api,
		filter("tag:"+orgTagKey, org),
		filter("tag:"+machineTagKey, id),
		filter("instance-state-name", liveStates...))
	if err != nil {
		return nil, err
	}
	var mine []ec2Types.Instance
	for _, inst := range found {
		tags := tagMap(inst.Tags)
		if tags[orgTagKey] == org && tags[machineTagKey] == id {
			mine = append(mine, inst)
		}
	}
	switch len(mine) {
	case 0:
		return nil, nil
	case 1:
		return &mine[0], nil
	}
	return nil, fmt.Errorf("machine %s names %d instances", id, len(mine))
}

// ---- the machine shape ----

func tagMap(tags []ec2Types.Tag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return m
}

// tagString renders an instance's Hanzo tags in the "key:value," form every tag
// reader in this package parses (tagValue, orgFromTag, MachineKind). Only Hanzo's
// own keys are carried, in a fixed order, and a value that could break the parse
// is dropped rather than rendered.
func tagString(tags map[string]string) string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		if strings.HasPrefix(k, "hanzo-") || k == managedByKey {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		if !safeTagField(k) || !safeTagField(tags[k]) {
			continue
		}
		b.WriteString(k + ":" + tags[k] + ",")
	}
	return b.String()
}

// machineStates is an instance state in the words every machine in this service
// uses, so the agent binding's "Running" test and the BYO providers agree.
var machineStates = map[ec2Types.InstanceStateName]string{
	ec2Types.InstanceStateNamePending:      "Starting",
	ec2Types.InstanceStateNameRunning:      "Running",
	ec2Types.InstanceStateNameStopping:     "Stopping",
	ec2Types.InstanceStateNameStopped:      "Stopped",
	ec2Types.InstanceStateNameShuttingDown: "Terminating",
	ec2Types.InstanceStateNameTerminated:   "Terminated",
}

// machineFromInstance is a hosted instance as a Machine. Its Name and Id are the
// machine id, never the instance id; the size's shape comes from the catalog.
func machineFromInstance(inst ec2Types.Instance, region string) *Machine {
	tags := tagMap(inst.Tags)
	id := tags[machineTagKey]
	m := &Machine{
		Owner:       tags[orgTagKey],
		Name:        id,
		Id:          id,
		DisplayName: cmp.Or(tags[nameTagKey], id),
		Region:      region,
		Size:        string(inst.InstanceType),
		Tag:         tagString(tags),
		Os:          "linux",
		PublicIp:    aws.ToString(inst.PublicIpAddress),
		PrivateIp:   aws.ToString(inst.PrivateIpAddress),
		instance:    aws.ToString(inst.InstanceId),
	}
	if inst.Placement != nil {
		m.Zone = aws.ToString(inst.Placement.AvailabilityZone)
	}
	if inst.State != nil {
		m.State = cmp.Or(machineStates[inst.State.Name], string(inst.State.Name))
	}
	// LaunchTime is the last start, which is the hour the launch or the start
	// debited, and the hour the sweep skips.
	if inst.LaunchTime != nil {
		m.CreatedTime = inst.LaunchTime.UTC().Format(time.RFC3339)
	}
	if o, ok := offerFor(m.Size); ok {
		m.CpuSize = strconv.Itoa(o.vcpus)
		m.MemSize = strconv.Itoa(o.memoryMB)
	}
	return m
}

// ---- writes ----

// launch is one hosted instance to run.
type launch struct {
	id       string
	offer    offer
	name     string
	tags     map[string]string
	userData string
}

// run starts one instance for l in the account and returns it.
//
// The machine id is the launch's ClientToken, so a retried RunInstances returns
// the instance the first attempt started instead of starting a second. The root
// volume is sized by the catalog, encrypted and deleted with the instance; the
// instance carries no IAM role, requires IMDSv2 and takes no customer tags —
// only the scope tags visor sets, on the instance and on its volume.
func run(ctx context.Context, api *ec2.Client, acct hostedAccount, l launch) (*ec2Types.Instance, error) {
	image, _ := acct.image(l.offer)
	root, err := rootDevice(ctx, api, image, l.offer)
	if err != nil {
		return nil, err
	}
	tags := make([]ec2Types.Tag, 0, len(l.tags)+1)
	tags = append(tags, ec2Types.Tag{Key: aws.String(nameTagKey), Value: aws.String(clip(l.name, 256))})
	keys := make([]string, 0, len(l.tags))
	for k := range l.tags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		tags = append(tags, ec2Types.Tag{Key: aws.String(k), Value: aws.String(l.tags[k])})
	}
	in := &ec2.RunInstancesInput{
		ImageId:          aws.String(image),
		InstanceType:     ec2Types.InstanceType(l.offer.slug),
		MinCount:         aws.Int32(1),
		MaxCount:         aws.Int32(1),
		ClientToken:      aws.String(l.id),
		SubnetId:         aws.String(acct.Subnet),
		SecurityGroupIds: []string{acct.SecurityGroup},
		BlockDeviceMappings: []ec2Types.BlockDeviceMapping{{
			DeviceName: aws.String(root),
			Ebs: &ec2Types.EbsBlockDevice{
				VolumeSize:          aws.Int32(int32(l.offer.diskGB)),
				VolumeType:          ec2Types.VolumeTypeGp3,
				Encrypted:           aws.Bool(true),
				DeleteOnTermination: aws.Bool(true),
			},
		}},
		MetadataOptions: &ec2Types.InstanceMetadataOptionsRequest{
			HttpEndpoint: ec2Types.InstanceMetadataEndpointStateEnabled,
			HttpTokens:   ec2Types.HttpTokensStateRequired,
		},
		TagSpecifications: []ec2Types.TagSpecification{
			{ResourceType: ec2Types.ResourceTypeInstance, Tags: tags},
			{ResourceType: ec2Types.ResourceTypeVolume, Tags: tags},
		},
	}
	if l.userData != "" {
		in.UserData = aws.String(base64.StdEncoding.EncodeToString([]byte(l.userData)))
	}
	if l.offer.burstable {
		in.CreditSpecification = &ec2Types.CreditSpecificationRequest{CpuCredits: aws.String("standard")}
	}
	out, err := api.RunInstances(ctx, in)
	if err != nil {
		return nil, refusal("launch "+l.id, err, l.offer.slug, acct.Region)
	}
	if len(out.Instances) != 1 {
		return nil, fmt.Errorf("launch %s: the cloud started %d instances", l.id, len(out.Instances))
	}
	return &out.Instances[0], nil
}

// rootDevice is the root device of image, checked against the size: the image
// must be x86_64, like every size in the catalog, and must fit the root volume
// the size's price includes.
func rootDevice(ctx context.Context, api *ec2.Client, image string, o offer) (string, error) {
	out, err := api.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{image}})
	if err != nil {
		return "", refusal("read image "+image, err, o.slug, "")
	}
	if len(out.Images) != 1 {
		return "", fmt.Errorf("image %s is not available to the hosted account", image)
	}
	img := out.Images[0]
	if img.Architecture != ec2Types.ArchitectureValuesX8664 {
		return "", fmt.Errorf("image %s is %s; hosted sizes are x86_64", image, img.Architecture)
	}
	root := aws.ToString(img.RootDeviceName)
	if root == "" {
		return "", fmt.Errorf("image %s names no root device", image)
	}
	for _, bd := range img.BlockDeviceMappings {
		if aws.ToString(bd.DeviceName) == root && bd.Ebs != nil && int64(aws.ToInt32(bd.Ebs.VolumeSize)) > o.diskGB {
			return "", fmt.Errorf("image %s needs a %d GB root volume; %s includes %d GB",
				image, aws.ToInt32(bd.Ebs.VolumeSize), o.slug, o.diskGB)
		}
	}
	return root, nil
}

// setState starts or stops inst.
func setState(ctx context.Context, api *ec2.Client, inst *ec2Types.Instance, running bool) error {
	ids := []string{aws.ToString(inst.InstanceId)}
	var err error
	if running {
		_, err = api.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: ids})
	} else {
		_, err = api.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: ids})
	}
	if err != nil {
		return refusal("change state", err, string(inst.InstanceType), "")
	}
	return nil
}

// terminate ends inst. Its root volume goes with it.
func terminate(ctx context.Context, api *ec2.Client, inst *ec2Types.Instance) error {
	_, err := api.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{aws.ToString(inst.InstanceId)}})
	if err != nil {
		return refusal("terminate", err, string(inst.InstanceType), "")
	}
	return nil
}

// quotaCodes are the answers that mean the account's quota does not cover the
// size. A GPU family starts at a quota of zero and runs nothing until AWS raises
// it, so this is the refusal every GPU launch gets until then.
var quotaCodes = map[string]bool{
	"VcpuLimitExceeded":     true,
	"InstanceLimitExceeded": true,
}

// capacityCodes are the answers that mean AWS has no hardware for the size in
// the region right now.
var capacityCodes = map[string]bool{
	"InsufficientInstanceCapacity": true,
	"InsufficientHostCapacity":     true,
	"InsufficientCapacity":         true,
}

// refusal turns an EC2 error into what a caller is told. The full error, which
// names the account and the upstream, is logged; the caller gets the error code
// at most.
func refusal(op string, err error, size, region string) error {
	logs.Warning("hosted compute: %s: %v", op, err)
	var api smithy.APIError
	hasCode := errors.As(err, &api)
	if errors.Is(err, spend.ErrRefused) {
		return fmt.Errorf("%s: %w", op, spend.ErrRefused)
	}
	if !hasCode {
		return fmt.Errorf("%s failed: the cloud did not answer", op)
	}
	where := size
	if region != "" {
		where = size + " in " + region
	}
	switch code := api.ErrorCode(); {
	case quotaCodes[code]:
		return fmt.Errorf("%s cannot launch yet: the hosted account's quota for this size is not granted (%s)", where, code)
	case capacityCodes[code]:
		return fmt.Errorf("no capacity for %s right now (%s)", where, code)
	default:
		return fmt.Errorf("%s refused (%s)", op, code)
	}
}

// clip bounds s to n runes.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
