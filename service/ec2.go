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
// It holds no AWS key. The account is reached as the role `hanzo-compute`,
// assumed with a Hanzo IAM token: hanzo.id is an OIDC provider in the AWS
// account, the role trusts only sts:AssumeRoleWithWebIdentity for tokens of the
// IAM app `hanzo-compute`, and this process holds that app's client credential.
// Everything about the account is configuration, read by the key names below,
// and a call refuses, naming what is missing, rather than guess any of it.
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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/hanzoai/compute/conf"
	"github.com/hanzoai/compute/logs"
)

// The hosted account's configuration keys. Each is read from an environment
// variable of the same name, or from conf/app.conf, which reads it from the
// variable envOf names.
const (
	keyRegion          = "computeRegion"
	keySubnet          = "computeSubnet"
	keySecurityGroup   = "computeSecurityGroup"
	keyImage           = "computeImage"
	keyGPUImage        = "computeGpuImage"
	keyRoleARN         = "computeRoleArn"
	keyIAMClientID     = "computeIamClientId"
	keyIAMClientSecret = "computeIamClientSecret"
	keyIAMEndpoint     = "computeIamEndpoint"
)

var envOf = map[string]string{
	keyRegion:          "COMPUTE_REGION",
	keySubnet:          "COMPUTE_SUBNET",
	keySecurityGroup:   "COMPUTE_SECURITY_GROUP",
	keyImage:           "COMPUTE_IMAGE",
	keyGPUImage:        "COMPUTE_GPU_IMAGE",
	keyRoleARN:         "COMPUTE_ROLE_ARN",
	keyIAMClientID:     "COMPUTE_IAM_CLIENT_ID",
	keyIAMClientSecret: "COMPUTE_IAM_CLIENT_SECRET",
	keyIAMEndpoint:     "COMPUTE_IAM_ENDPOINT",
}

// hanzoIAM is the IAM the role's OIDC provider trusts, and where the web
// identity is minted unless computeIamEndpoint names another front of it.
const hanzoIAM = "https://hanzo.id"

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
	signer
}

// signer is what a call to the account is signed with: the region, the role,
// and the IAM client credential the role is assumed with.
type signer struct {
	Region       string
	RoleARN      string
	ClientID     string
	ClientSecret string
	IAM          string
}

// hostedConfig reads the hosted account's configuration. It is read on every
// use, so a key set on a running process takes effect on the next call.
func hostedConfig() hostedAccount {
	get := func(key string) string { return strings.TrimSpace(conf.GetConfigString(key)) }
	region := get(keyRegion)
	return hostedAccount{
		Region:        region,
		Subnet:        get(keySubnet),
		SecurityGroup: get(keySecurityGroup),
		Image:         get(keyImage),
		GPUImage:      get(keyGPUImage),
		signer: signer{
			Region:       region,
			RoleARN:      get(keyRoleARN),
			ClientID:     get(keyIAMClientID),
			ClientSecret: get(keyIAMClientSecret),
			IAM:          strings.TrimRight(cmp.Or(get(keyIAMEndpoint), hanzoIAM), "/"),
		},
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

// missing is every key a call to the account needs and does not have.
func (s signer) missing() []string {
	return unset(
		[2]string{s.Region, keyRegion},
		[2]string{s.RoleARN, keyRoleARN},
		[2]string{s.ClientID, keyIAMClientID},
		[2]string{s.ClientSecret, keyIAMClientSecret},
	)
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
	missing := append(a.signer.missing(), unset(
		[2]string{a.Subnet, keySubnet},
		[2]string{a.SecurityGroup, keySecurityGroup},
		[2]string{image, imageKey},
	)...)
	if len(missing) > 0 {
		return fmt.Errorf("hosted compute cannot launch %s: %s not set", o.slug, strings.Join(missing, ", "))
	}
	return nil
}

// errNoRegion is the account with no region: there is nothing to reach.
var errNoRegion = fmt.Errorf("hosted compute is not configured: %s (%s) is not set", keyRegion, envOf[keyRegion])

// hosted is the EC2 client for the configured signer, built once per signer so
// the assumed role's credentials are cached across calls rather than fetched
// per request, and rebuilt when the role or the client credential changes.
var hosted struct {
	sync.Mutex
	signer signer
	client *ec2.Client
}

// hostedEC2 returns the client for the hosted account and the region it serves.
func hostedEC2(ctx context.Context) (*ec2.Client, string, error) {
	s := hostedConfig().signer
	if s.Region == "" {
		return nil, "", errNoRegion
	}
	if missing := s.missing(); len(missing) > 0 {
		return nil, "", fmt.Errorf("hosted compute cannot reach AWS: %s not set", strings.Join(missing, ", "))
	}
	hosted.Lock()
	defer hosted.Unlock()
	if hosted.client != nil && hosted.signer == s {
		return hosted.client, s.Region, nil
	}
	cfg, err := awsConfig(ctx, s)
	if err != nil {
		return nil, "", err
	}
	hosted.client, hosted.signer = ec2.NewFromConfig(cfg), s
	return hosted.client, s.Region, nil
}

// roleWindow is how long before the assumed role's credentials expire they are
// replaced, so a call never goes out on credentials that lapse in flight.
const roleWindow = 5 * time.Minute

// awsConfig is the SDK configuration for s: the SDK's own loader for region,
// endpoints and retries, a bounded HTTP client under every call, and the role
// as the only credential.
//
// The loader is handed anonymous credentials, so it never resolves the SDK's
// default chain at all: that chain reads AWS_ACCESS_KEY_ID first, and this pod
// carries that variable for its own object store, so EC2 would otherwise be
// signed with a storage key. Nothing in it reaches the instance metadata
// service either.
func awsConfig(ctx context.Context, s signer) (aws.Config, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(s.Region),
		config.WithHTTPClient(directHTTP()),
		config.WithCredentialsProvider(aws.AnonymousCredentials{}),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("hosted compute: load AWS configuration: %w", err)
	}
	cfg.Credentials = aws.NewCredentialsCache(roleCredentials(cfg, s),
		func(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = roleWindow })
	return cfg, nil
}

// roleCredentials assumes the role with a Hanzo IAM client_credentials token as
// the web identity. The exchange is unsigned — AssumeRoleWithWebIdentity takes
// no AWS credential — so the STS client carries none.
func roleCredentials(cfg aws.Config, s signer) aws.CredentialsProvider {
	stsClient := sts.NewFromConfig(cfg, func(o *sts.Options) { o.Credentials = aws.AnonymousCredentials{} })
	token := iamToken{NewIdentity(s.IAM, s.ClientID, s.ClientSecret, "", directHTTP())}
	return assumed{stscreds.NewWebIdentityRoleProvider(stsClient, s.RoleARN, token,
		func(o *stscreds.WebIdentityRoleOptions) { o.RoleSessionName = managedBy })}
}

// errRole marks a call that failed because the role could not be assumed — IAM
// refused the client, or STS refused the token — rather than because EC2
// refused the call.
var errRole = errors.New("hosted compute could not assume its role")

// assumed is the role provider with its failures marked errRole.
type assumed struct{ aws.CredentialsProvider }

func (a assumed) Retrieve(ctx context.Context) (aws.Credentials, error) {
	creds, err := a.CredentialsProvider.Retrieve(ctx)
	if err != nil {
		return creds, fmt.Errorf("%w: %w", errRole, err)
	}
	return creds, nil
}

// iamToken is the web identity: the IAM app's client_credentials token, which
// Identity mints and replaces before it expires.
type iamToken struct{ identity *Identity }

// GetIdentityToken satisfies stscreds.IdentityTokenRetriever.
func (t iamToken) GetIdentityToken() ([]byte, error) {
	tok, err := t.identity.Token()
	if err != nil {
		return nil, fmt.Errorf("hosted compute: web identity: %w", err)
	}
	return []byte(tok), nil
}

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

// liveStates are the instance states a machine exists in. A terminated instance
// stays visible for about an hour after termination and is not a machine.
var liveStates = []string{"pending", "running", "stopping", "stopped", "shutting-down"}

func filter(name string, values ...string) ec2Types.Filter {
	return ec2Types.Filter{Name: aws.String(name), Values: values}
}

// describe returns every instance matching filters, across pages.
func describe(ctx context.Context, api *ec2.Client, filters ...ec2Types.Filter) ([]ec2Types.Instance, error) {
	pages := ec2.NewDescribeInstancesPaginator(api, &ec2.DescribeInstancesInput{
		Filters:    filters,
		MaxResults: aws.Int32(1000),
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
	if errors.Is(err, errRole) {
		if hasCode {
			return fmt.Errorf("%s: %w (%s)", op, errRole, api.ErrorCode())
		}
		return fmt.Errorf("%s: %w", op, errRole)
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
