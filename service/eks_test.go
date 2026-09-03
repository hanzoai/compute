// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package service

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// fakeEKS is one AWS account: its clusters and nodegroups, and a record of what
// it was asked to create, scale and delete. No key, no network.
type fakeEKS struct {
	mu         sync.Mutex
	clusters   map[string]*ekstypes.Cluster
	nodegroups map[string]*ekstypes.Nodegroup // cluster/name
	created    []*eks.CreateClusterInput
	pooled     []*eks.CreateNodegroupInput
	scaled     []*eks.UpdateNodegroupConfigInput
	dropped    []string
	deleted    []string
}

func newFakeEKS() *fakeEKS {
	return &fakeEKS{clusters: map[string]*ekstypes.Cluster{}, nodegroups: map[string]*ekstypes.Nodegroup{}}
}

func notFound(what string) error {
	return &ekstypes.ResourceNotFoundException{Message: aws.String(what + " not found")}
}

func (f *fakeEKS) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.clusters[aws.ToString(in.Name)]
	if !ok {
		return nil, notFound("cluster")
	}
	return &eks.DescribeClusterOutput{Cluster: c}, nil
}

func (f *fakeEKS) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ng, ok := f.nodegroups[aws.ToString(in.ClusterName)+"/"+aws.ToString(in.NodegroupName)]
	if !ok {
		return nil, notFound("nodegroup")
	}
	return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
}

func (f *fakeEKS) ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &eks.ListClustersOutput{}
	for n := range f.clusters {
		out.Clusters = append(out.Clusters, n)
	}
	return out, nil
}

func (f *fakeEKS) ListNodegroups(_ context.Context, in *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &eks.ListNodegroupsOutput{}
	for k, ng := range f.nodegroups {
		if strings.HasPrefix(k, aws.ToString(in.ClusterName)+"/") {
			out.Nodegroups = append(out.Nodegroups, aws.ToString(ng.NodegroupName))
		}
	}
	return out, nil
}

func (f *fakeEKS) CreateCluster(_ context.Context, in *eks.CreateClusterInput, _ ...func(*eks.Options)) (*eks.CreateClusterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in)
	c := &ekstypes.Cluster{
		Name: in.Name, Status: ekstypes.ClusterStatusCreating, Tags: in.Tags,
		ResourcesVpcConfig: &ekstypes.VpcConfigResponse{SubnetIds: in.ResourcesVpcConfig.SubnetIds},
	}
	f.clusters[aws.ToString(in.Name)] = c
	return &eks.CreateClusterOutput{Cluster: c}, nil
}

func (f *fakeEKS) DeleteCluster(_ context.Context, in *eks.DeleteClusterInput, _ ...func(*eks.Options)) (*eks.DeleteClusterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, aws.ToString(in.Name))
	delete(f.clusters, aws.ToString(in.Name))
	return &eks.DeleteClusterOutput{}, nil
}

func (f *fakeEKS) CreateNodegroup(_ context.Context, in *eks.CreateNodegroupInput, _ ...func(*eks.Options)) (*eks.CreateNodegroupOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pooled = append(f.pooled, in)
	ng := &ekstypes.Nodegroup{
		NodegroupName: in.NodegroupName, InstanceTypes: in.InstanceTypes, ScalingConfig: in.ScalingConfig,
		Labels: in.Labels, Tags: in.Tags, Status: ekstypes.NodegroupStatusCreating,
	}
	f.nodegroups[aws.ToString(in.ClusterName)+"/"+aws.ToString(in.NodegroupName)] = ng
	return &eks.CreateNodegroupOutput{Nodegroup: ng}, nil
}

func (f *fakeEKS) UpdateNodegroupConfig(_ context.Context, in *eks.UpdateNodegroupConfigInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scaled = append(f.scaled, in)
	return &eks.UpdateNodegroupConfigOutput{}, nil
}

func (f *fakeEKS) DeleteNodegroup(_ context.Context, in *eks.DeleteNodegroupInput, _ ...func(*eks.Options)) (*eks.DeleteNodegroupOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := aws.ToString(in.ClusterName) + "/" + aws.ToString(in.NodegroupName)
	f.dropped = append(f.dropped, key)
	delete(f.nodegroups, key)
	return &eks.DeleteNodegroupOutput{}, nil
}

type fakeIAM struct {
	roles    map[string]string // name -> arn
	created  []string
	attached []string
}

func (f *fakeIAM) CreateRole(_ context.Context, in *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
	name := aws.ToString(in.RoleName)
	if _, ok := f.roles[name]; ok {
		return nil, &iamtypes.EntityAlreadyExistsException{Message: aws.String("Role already exists")}
	}
	arn := "arn:aws:iam::123456789012:role/" + name
	f.roles[name] = arn
	f.created = append(f.created, name)
	return &iam.CreateRoleOutput{Role: &iamtypes.Role{Arn: aws.String(arn), RoleName: in.RoleName}}, nil
}

func (f *fakeIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	arn, ok := f.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	return &iam.GetRoleOutput{Role: &iamtypes.Role{Arn: aws.String(arn), RoleName: in.RoleName}}, nil
}

func (f *fakeIAM) AttachRolePolicy(_ context.Context, in *iam.AttachRolePolicyInput, _ ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error) {
	f.attached = append(f.attached, aws.ToString(in.RoleName)+"<-"+aws.ToString(in.PolicyArn))
	return &iam.AttachRolePolicyOutput{}, nil
}

type fakeEC2 struct {
	subnets   []ec2types.Subnet
	instances []ec2types.Instance
	filters   []ec2types.Filter
}

func (f *fakeEC2) DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{Subnets: f.subnets}, nil
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.filters = in.Filters
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: f.instances}}}, nil
}

func eksUnderTest(f *fakeEKS) (*EKSClient, *fakeIAM, *fakeEC2) {
	i := &fakeIAM{roles: map[string]string{}}
	e := &fakeEC2{subnets: []ec2types.Subnet{
		{SubnetId: aws.String("subnet-a"), AvailabilityZone: aws.String("us-east-1a")},
		{SubnetId: aws.String("subnet-b"), AvailabilityZone: aws.String("us-east-1b")},
	}}
	return &EKSClient{eks: f, iam: i, ec2: e, region: "us-east-1", wait: time.Second}, i, e
}

// ---- pure mappers ----

func TestEKSClusterRequest(t *testing.T) {
	spec := &CreateClusterSpec{Name: "acme-prod", Version: " 1.31 ", NodePool: CreateClusterNodePool{Size: "m6i.large", Count: 3}}
	in := buildEKSCluster(spec, []string{"managed-by:hanzo-visor", "hanzo-org:acme"}, "arn:role", []string{"subnet-a", "subnet-b"})
	if aws.ToString(in.Name) != "acme-prod" || aws.ToString(in.RoleArn) != "arn:role" || aws.ToString(in.Version) != "1.31" {
		t.Fatalf("identity mapping wrong: %+v", in)
	}
	if got := in.ResourcesVpcConfig.SubnetIds; len(got) != 2 || got[1] != "subnet-b" {
		t.Fatalf("subnets = %v", got)
	}
	if in.AccessConfig.AuthenticationMode != ekstypes.AuthenticationModeApiAndConfigMap {
		t.Fatalf("authentication mode = %q, want API_AND_CONFIG_MAP", in.AccessConfig.AuthenticationMode)
	}
	if in.Tags["hanzo-org"] != "acme" || in.Tags["managed-by"] != "hanzo-visor" {
		t.Fatalf("ownership tags not keyed: %v", in.Tags)
	}
	if v := buildEKSCluster(&CreateClusterSpec{Name: "x"}, nil, "r", nil).Version; v != nil {
		t.Fatalf("an empty version must be left to EKS, got %q", *v)
	}
}

func TestEKSScaling(t *testing.T) {
	sc := func(s *ekstypes.NodegroupScalingConfig) [3]int32 {
		return [3]int32{aws.ToInt32(s.MinSize), aws.ToInt32(s.MaxSize), aws.ToInt32(s.DesiredSize)}
	}
	if got := sc(scalingFor(&CreateNodePoolSpec{Count: 3})); got != [3]int32{3, 3, 3} {
		t.Errorf("fixed pool = %v, want pinned at 3", got)
	}
	if got := sc(scalingFor(&CreateNodePoolSpec{Count: 0})); got != [3]int32{0, 1, 0} {
		t.Errorf("empty pool = %v, want max floored at 1", got)
	}
	if got := sc(scalingFor(&CreateNodePoolSpec{AutoScale: true, MinNodes: 1, MaxNodes: 5, Count: 0})); got != [3]int32{1, 5, 1} {
		t.Errorf("autoscaled pool = %v, want desired clamped into [1,5]", got)
	}
	fixed := &ekstypes.NodegroupScalingConfig{MinSize: aws.Int32(3), MaxSize: aws.Int32(3), DesiredSize: aws.Int32(3)}
	if got := sc(scaled(fixed, 5)); got != [3]int32{5, 5, 5} {
		t.Errorf("scaling a fixed pool = %v, want it still fixed, at 5", got)
	}
	auto := &ekstypes.NodegroupScalingConfig{MinSize: aws.Int32(1), MaxSize: aws.Int32(5), DesiredSize: aws.Int32(2)}
	if got := sc(scaled(auto, 7)); got != [3]int32{1, 7, 7} {
		t.Errorf("scaling past an autoscaled pool's ceiling = %v, want the ceiling raised to 7", got)
	}
	if got := sc(scaled(auto, 3)); got != [3]int32{1, 5, 3} {
		t.Errorf("scaling inside the bounds = %v, want the bounds kept", got)
	}
}

func TestTagsRoundTrip(t *testing.T) {
	in := []string{"managed-by:hanzo-visor", "hanzo-org:acme", "bare"}
	m := tagMap(in)
	if m["hanzo-org"] != "acme" || m["bare"] != "" || len(m) != 3 {
		t.Fatalf("tagMap = %v", m)
	}
	out := tagList(m)
	if strings.Join(out, ",") != "bare,hanzo-org:acme,managed-by:hanzo-visor" {
		t.Fatalf("tagList = %v", out)
	}
	if !clusterHasTag(out, orgTag("acme")) {
		t.Fatal("the org tag must survive the trip through a keyed cloud")
	}
}

func TestEKSTokenEncoding(t *testing.T) {
	tok := eksToken("https://sts.us-east-1.amazonaws.com/?Action=GetCallerIdentity")
	if !strings.HasPrefix(tok, eksTokenPrefix) {
		t.Fatalf("token = %q, want the k8s-aws-v1. prefix", tok)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok, eksTokenPrefix))
	if err != nil || !strings.HasPrefix(string(raw), "https://sts.") {
		t.Fatalf("token body is not the base64url URL: %v %q", err, raw)
	}
}

// ---- account prerequisites ----

func TestEKSEnsureRoleIsIdempotent(t *testing.T) {
	c, i, _ := eksUnderTest(newFakeEKS())
	first, err := c.ensureRole(context.Background(), eksNodeRole, "ec2.amazonaws.com", eksNodePolicies)
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.ensureRole(context.Background(), eksNodeRole, "ec2.amazonaws.com", eksNodePolicies)
	if err != nil {
		t.Fatalf("a role that already exists is the state asked for, got %v", err)
	}
	if first != again || !strings.HasSuffix(first, "/"+eksNodeRole) {
		t.Fatalf("arn = %q then %q", first, again)
	}
	if len(i.created) != 1 {
		t.Fatalf("role created %d times", len(i.created))
	}
	if len(i.attached) != 2*len(eksNodePolicies) {
		t.Fatalf("policies attached %d times, want %d — attach is idempotent and runs every time", len(i.attached), 2*len(eksNodePolicies))
	}
}

func TestEKSSubnets(t *testing.T) {
	c, _, e := eksUnderTest(newFakeEKS())
	got, err := c.subnets(context.Background(), []string{"subnet-mine"})
	if err != nil || len(got) != 1 || got[0] != "subnet-mine" {
		t.Fatalf("named subnets must win: %v %v", got, err)
	}
	got, err = c.subnets(context.Background(), nil)
	if err != nil || strings.Join(got, ",") != "subnet-a,subnet-b" {
		t.Fatalf("default VPC subnets = %v %v", got, err)
	}
	e.subnets = e.subnets[:1]
	if _, err := c.subnets(context.Background(), nil); err == nil {
		t.Fatal("one zone must refuse: EKS will, later and less clearly")
	}
}

// ---- lifecycle ----

func TestEKSCreateReturnsCreatingAndSeedsOnceActive(t *testing.T) {
	f := newFakeEKS()
	c, i, _ := eksUnderTest(f)
	c.wait = time.Millisecond // the background seed gives up at once; the seed is driven below
	spec := &CreateClusterSpec{Name: "acme-prod", NodePool: CreateClusterNodePool{Size: "m6i.large", Count: 3}}
	tags := []string{"managed-by:hanzo-visor", orgTag("acme")}

	kc, err := c.CreateCluster(context.Background(), spec, tags)
	if err != nil {
		t.Fatal(err)
	}
	if kc.ID != "acme-prod" || kc.Status != "CREATING" || kc.Provider != providerAWS || !clusterHasTag(kc.Tags, orgTag("acme")) {
		t.Fatalf("created = %+v", kc)
	}
	if len(i.created) != 1 || i.created[0] != eksClusterRole {
		t.Fatalf("cluster role not ensured before create: %v", i.created)
	}
	if got := f.created[0].ResourcesVpcConfig.SubnetIds; strings.Join(got, ",") != "subnet-a,subnet-b" {
		t.Fatalf("cluster placed on %v, want the default VPC", got)
	}

	// The control plane comes up; the seed pool follows.
	f.mu.Lock()
	f.clusters["acme-prod"].Status = ekstypes.ClusterStatusActive
	f.mu.Unlock()
	c.wait = time.Second
	if err := c.seed(context.Background(), spec, tags); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pooled) != 1 {
		t.Fatalf("seed pools = %d", len(f.pooled))
	}
	ng := f.pooled[0]
	if aws.ToString(ng.NodegroupName) != "acme-prod-pool" || ng.InstanceTypes[0] != "m6i.large" || aws.ToInt32(ng.ScalingConfig.DesiredSize) != 3 {
		t.Fatalf("seed nodegroup = %+v", ng)
	}
	if strings.Join(ng.Subnets, ",") != "subnet-a,subnet-b" || !strings.HasSuffix(aws.ToString(ng.NodeRole), "/"+eksNodeRole) {
		t.Fatalf("seed nodegroup placement = %v role %v", ng.Subnets, aws.ToString(ng.NodeRole))
	}
	if ng.Tags["hanzo-org"] != "acme" {
		t.Fatalf("seed nodegroup lost its owner: %v", ng.Tags)
	}
}

func TestEKSScaleNodePool(t *testing.T) {
	f := newFakeEKS()
	f.nodegroups["acme-prod/gpu"] = &ekstypes.Nodegroup{
		NodegroupName: aws.String("gpu"), InstanceTypes: []string{"g5.xlarge"},
		ScalingConfig: &ekstypes.NodegroupScalingConfig{MinSize: aws.Int32(1), MaxSize: aws.Int32(5), DesiredSize: aws.Int32(2)},
	}
	c, _, _ := eksUnderTest(f)
	np, err := c.ScaleNodePool(context.Background(), "acme-prod", "gpu", 7)
	if err != nil {
		t.Fatal(err)
	}
	if np.Count != 7 || np.MaxNodes != 7 || np.MinNodes != 1 || !np.AutoScale || np.Size != "g5.xlarge" {
		t.Fatalf("scaled pool = %+v", np)
	}
	if got := f.scaled[0].ScalingConfig; aws.ToInt32(got.DesiredSize) != 7 || aws.ToInt32(got.MaxSize) != 7 {
		t.Fatalf("update sent %+v", got)
	}
}

func TestEKSDeleteClusterRemovesNodegroupsFirst(t *testing.T) {
	f := newFakeEKS()
	f.clusters["acme-prod"] = &ekstypes.Cluster{Name: aws.String("acme-prod"), Status: ekstypes.ClusterStatusActive}
	f.nodegroups["acme-prod/gpu"] = &ekstypes.Nodegroup{NodegroupName: aws.String("gpu")}
	c, _, _ := eksUnderTest(f)

	if err := c.DeleteCluster(context.Background(), "acme-prod"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	dropped := append([]string(nil), f.dropped...)
	f.mu.Unlock()
	if len(dropped) != 1 || dropped[0] != "acme-prod/gpu" {
		t.Fatalf("nodegroups deleted = %v", dropped)
	}
	// The nodegroup is gone (the fake's delete removes it), so the deferred
	// cluster delete goes through — driven here rather than raced from the goroutine.
	if err := c.dropAfter(context.Background(), "acme-prod", []string{"gpu"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) == 0 || f.deleted[0] != "acme-prod" {
		t.Fatalf("cluster deleted = %v", f.deleted)
	}

	// A cluster with no pools goes at once.
	f.clusters["bare"] = &ekstypes.Cluster{Name: aws.String("bare")}
	f.mu.Unlock()
	err := c.DeleteCluster(context.Background(), "bare")
	f.mu.Lock()
	if err != nil || f.deleted[len(f.deleted)-1] != "bare" {
		t.Fatalf("bare cluster delete: %v %v", err, f.deleted)
	}
}

func TestEKSGetClusterNodes(t *testing.T) {
	f := newFakeEKS()
	f.clusters["acme-prod"] = &ekstypes.Cluster{Name: aws.String("acme-prod"), Status: ekstypes.ClusterStatusActive, Tags: map[string]string{"hanzo-org": "acme"}}
	f.nodegroups["acme-prod/gpu"] = &ekstypes.Nodegroup{
		NodegroupName: aws.String("gpu"), InstanceTypes: []string{"g5.xlarge"},
		ScalingConfig: &ekstypes.NodegroupScalingConfig{MinSize: aws.Int32(2), MaxSize: aws.Int32(2), DesiredSize: aws.Int32(2)},
	}
	c, _, e := eksUnderTest(f)
	e.instances = []ec2types.Instance{{
		InstanceId: aws.String("i-1"), InstanceType: ec2types.InstanceTypeG5Xlarge,
		Placement: &ec2types.Placement{AvailabilityZone: aws.String("us-east-1a")},
		State:     &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		PrivateIpAddress: aws.String("10.0.0.5"),
	}}
	d, err := c.GetCluster(context.Background(), "acme-prod")
	if err != nil {
		t.Fatal(err)
	}
	if !clusterHasTag(d.Tags, orgTag("acme")) || d.Provider != providerAWS {
		t.Fatalf("detail = %+v", d.KubernetesCluster)
	}
	if len(d.NodePools) != 1 || d.NodePools[0].Count != 2 || d.NodePools[0].AutoScale {
		t.Fatalf("pools = %+v", d.NodePools[0])
	}
	if len(d.Nodes) != 1 || d.Nodes[0].Id != "i-1" || d.Nodes[0].Provider != providerAWS || d.Nodes[0].PrivateIp != "10.0.0.5" || d.Nodes[0].Tag != "eks-cluster:acme-prod" {
		t.Fatalf("nodes = %+v", d.Nodes)
	}
	if aws.ToString(e.filters[0].Name) != "tag:eks:cluster-name" || e.filters[0].Values[0] != "acme-prod" {
		t.Fatalf("nodes were not asked for by cluster: %+v", e.filters)
	}

	if _, err := c.GetCluster(context.Background(), "nope"); !IsNotFound(err) {
		t.Fatalf("a missing cluster must read as not found, got %v", err)
	}
}

// ---- credentials ----

// The presigner is the real one with a fake key: presigning is local, so this
// proves the token shape without a network or an account.
func TestEKSCredentials(t *testing.T) {
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIAFAKEFAKEFAKEFAKE", SecretAccessKey: "fake"}, nil
		})))
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeEKS()
	ca := base64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"))
	f.clusters["acme-prod"] = &ekstypes.Cluster{
		Name: aws.String("acme-prod"), Status: ekstypes.ClusterStatusActive,
		Endpoint:             aws.String("https://ABC.gr7.us-east-1.eks.amazonaws.com"),
		CertificateAuthority: &ekstypes.Certificate{Data: aws.String(ca)},
	}
	c, _, _ := eksUnderTest(f)
	c.sts = sts.NewPresignClient(sts.NewFromConfig(cfg))

	before := time.Now()
	creds, err := c.GetCredentials(context.Background(), "acme-prod")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Endpoint != "https://ABC.gr7.us-east-1.eks.amazonaws.com" || !strings.HasPrefix(string(creds.CAData), "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("endpoint/ca = %q %q", creds.Endpoint, creds.CAData)
	}
	if creds.Expiry.Before(before.Add(13*time.Minute)) || creds.Expiry.After(time.Now().Add(15*time.Minute)) {
		t.Fatalf("expiry %v is not ~14 minutes out", creds.Expiry)
	}
	if !strings.HasPrefix(creds.Token, eksTokenPrefix) {
		t.Fatalf("token = %q", creds.Token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(creds.Token, eksTokenPrefix))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "sts.us-east-1.amazonaws.com" || q.Get("Action") != "GetCallerIdentity" {
		t.Fatalf("presigned %s", u)
	}
	if !strings.HasPrefix(q.Get("X-Amz-Credential"), "AKIAFAKEFAKEFAKEFAKE/") {
		t.Fatalf("signed with %q, want the account key", q.Get("X-Amz-Credential"))
	}
	if !strings.Contains(q.Get("X-Amz-SignedHeaders"), eksClusterHeader) {
		t.Fatalf("signed headers %q do not bind the cluster — the authenticator would accept this token for any cluster", q.Get("X-Amz-SignedHeaders"))
	}
	if q.Get("X-Amz-Expires") != "60" {
		t.Fatalf("X-Amz-Expires = %q", q.Get("X-Amz-Expires"))
	}
	if q.Get("X-Amz-Signature") == "" {
		t.Fatal("unsigned")
	}

	c.carried = true
	if _, err := c.GetCredentials(context.Background(), "acme-prod"); err == nil {
		t.Fatal("a carried account cannot presign: the key is not here, and an unsigned token is worse than none")
	}
}

// ---- the registry ----

func TestAWSSpeaksKubernetesAndIsCarried(t *testing.T) {
	c, err := newMachineAwsClient("id", "secret", "us-east-1", directHTTP())
	if err != nil {
		t.Fatal(err)
	}
	k, ok := kubernetesFor(c)
	if !ok || k.Provider() != providerAWS {
		t.Fatalf("AWS must speak Kubernetes: %v %v", ok, k)
	}

	t.Cleanup(func() { RegisterCarrier(nil) })
	var saw Credential
	RegisterCarrier(func(c Credential) (*http.Client, error) { saw = c; return &http.Client{}, nil })
	mc, err := NewMachineClient(Credential{Provider: providerAWS, Region: "us-east-1"})
	if err != nil {
		t.Fatalf("carried AWS must build: %v", err)
	}
	if saw.Provider != providerAWS {
		t.Fatalf("carrier consulted for %q", saw.Provider)
	}
	if _, err := mc.(KubernetesCapable).Kubernetes().GetCredentials(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "carried") {
		t.Fatalf("carried EKS credentials must say why they cannot be minted, got %v", err)
	}
}
